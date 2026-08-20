import type { FastifyError, FastifyInstance, FastifyReply, FastifyRequest } from "fastify";
import {
  MAX_UPLOAD_SIZE_BYTES,
  MEDIA_CONTENT_TYPES,
  MIN_UPLOAD_SIZE_BYTES,
  SHA256_BASE64_PATTERN,
} from "../domain/media";
import { getErrorDefinition, isKnownErrorCode } from "../errors/errorCodes";
import type { GraphQLContext } from "../graphql/context";
import type { MediaUploadOperations } from "../usecase/mediaUploadUsecase";
import { runWithRequestCorrelation } from "../observability/requestContext";
import { recordMediaRejection, recordMediaSessionCreation } from "../observability/prometheus";
import { ServiceName } from "../observability/serviceName";

export interface MediaRouteDependencies {
  loadContext(userId: string | null, orgId: string | null): Promise<GraphQLContext>;
  usecase: MediaUploadOperations;
}

const uuidPattern = "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$";

const assetParamsSchema = {
  type: "object",
  additionalProperties: false,
  required: ["assetId"],
  properties: { assetId: { type: "string", pattern: uuidPattern } },
} as const;

const uploadParamsSchema = {
  type: "object",
  additionalProperties: false,
  required: ["assetId", "uploadId"],
  properties: {
    assetId: { type: "string", pattern: uuidPattern },
    uploadId: { type: "string", pattern: uuidPattern },
  },
} as const;

export function registerMediaRoutes(app: FastifyInstance, dependencies: MediaRouteDependencies): void {
  app.post(
    "/api/v1/assets/:assetId/media/uploads",
    {
      errorHandler: mediaFrameworkErrorHandler,
      onRequest: setMediaResponseSecurityHeaders,
      preValidation: [
        recordChecksumDeclarationRejection,
        rejectUnknownMediaBody(["filename", "contentType", "sizeBytes", "checksumSha256"]),
      ],
      schema: {
        params: assetParamsSchema,
        headers: {
          type: "object",
          required: ["idempotency-key"],
          properties: { "idempotency-key": { type: "string", pattern: uuidPattern } },
        },
        body: {
          type: "object",
          additionalProperties: false,
          required: ["filename", "contentType", "sizeBytes", "checksumSha256"],
          properties: {
            filename: { type: "string", minLength: 1, maxLength: 255 },
            contentType: { type: "string", enum: [...MEDIA_CONTENT_TYPES] },
            sizeBytes: { type: "integer", minimum: MIN_UPLOAD_SIZE_BYTES, maximum: MAX_UPLOAD_SIZE_BYTES },
            checksumSha256: { type: "string", pattern: SHA256_BASE64_PATTERN },
          },
        },
      },
    },
    async (request, reply) => {
      try {
        const params = request.params as { assetId: string };
        const body = request.body as {
          filename: string;
          contentType: "image/jpeg" | "image/png";
          sizeBytes: number;
          checksumSha256: string;
        };
        const retryKey = String(request.headers["idempotency-key"]);
        const ctx = await loadMediaContext(request, dependencies);
        if (!ctx.userId || !ctx.currentOrgId) throw routeError("UNAUTHENTICATED");
        const result = await runWithRequestCorrelation(request.correlation, () =>
          dependencies.usecase.createSession(ctx, params.assetId, {
            idempotencyKey: retryKey,
            filename: body.filename,
            contentType: body.contentType,
            sizeBytes: body.sizeBytes,
            checksumSha256: body.checksumSha256,
          }),
        );
        const { replayed, ...session } = result;
        if (replayed) {
          reply.header("Idempotency-Replayed", "true");
        } else {
          reply.header("Location", `/api/v1/assets/${params.assetId}/media/uploads/${session.uploadId}`);
        }
        recordMediaSessionCreation(replayed ? "replayed" : "created", body.sizeBytes);
        reply.status(replayed ? 200 : 201).send({ data: session });
      } catch (error) {
        sendMediaRouteError(request, reply, error);
      }
    },
  );

  app.get(
    "/api/v1/assets/:assetId/media/uploads/:uploadId",
    {
      errorHandler: mediaFrameworkErrorHandler,
      onRequest: setMediaResponseSecurityHeaders,
      schema: { params: uploadParamsSchema },
    },
    async (request, reply) => {
      try {
        const { assetId, uploadId } = request.params as { assetId: string; uploadId: string };
        const ctx = await loadMediaContext(request, dependencies);
        const result = await runWithRequestCorrelation(request.correlation, () =>
          dependencies.usecase.getSession(ctx, assetId, uploadId),
        );
        reply.send({ data: result });
      } catch (error) {
        sendMediaRouteError(request, reply, error);
      }
    },
  );

  app.post(
    "/api/v1/assets/:assetId/media/uploads/:uploadId/refresh",
    {
      errorHandler: mediaFrameworkErrorHandler,
      onRequest: setMediaResponseSecurityHeaders,
      preValidation: rejectUnknownMediaBody([]),
      schema: { params: uploadParamsSchema },
    },
    async (request, reply) => {
      try {
        const { assetId, uploadId } = request.params as { assetId: string; uploadId: string };
        const ctx = await loadMediaContext(request, dependencies);
        const result = await runWithRequestCorrelation(request.correlation, () =>
          dependencies.usecase.refreshSession(ctx, assetId, uploadId),
        );
        reply.send({ data: result });
      } catch (error) {
        sendMediaRouteError(request, reply, error);
      }
    },
  );

  app.delete(
    "/api/v1/assets/:assetId/media/uploads/:uploadId",
    {
      errorHandler: mediaFrameworkErrorHandler,
      onRequest: setMediaResponseSecurityHeaders,
      schema: { params: uploadParamsSchema },
    },
    async (request, reply) => {
      try {
        const { assetId, uploadId } = request.params as { assetId: string; uploadId: string };
        const ctx = await loadMediaContext(request, dependencies);
        await runWithRequestCorrelation(request.correlation, () =>
          dependencies.usecase.cancelSession(ctx, assetId, uploadId),
        );
        reply.status(204).send();
      } catch (error) {
        sendMediaRouteError(request, reply, error);
      }
    },
  );

  app.put(
    "/api/v1/assets/:assetId/media",
    {
      errorHandler: mediaFrameworkErrorHandler,
      onRequest: setMediaResponseSecurityHeaders,
      preValidation: rejectUnknownMediaBody(["uploadId"]),
      schema: {
        params: assetParamsSchema,
        body: {
          type: "object",
          additionalProperties: false,
          required: ["uploadId"],
          properties: { uploadId: { type: "string", pattern: uuidPattern } },
        },
      },
    },
    async (request, reply) => {
      try {
        const { assetId } = request.params as { assetId: string };
        const { uploadId } = request.body as { uploadId: string };
        const ctx = await loadMediaContext(request, dependencies);
        const result = await runWithRequestCorrelation(request.correlation, () =>
          dependencies.usecase.commit(ctx, assetId, uploadId),
        );
        if (result.replayed) reply.header("Idempotency-Replayed", "true");
        reply.status(result.replayed ? 200 : 202).send({
          data: {
            assetId: result.assetId,
            uploadId: result.uploadId,
            jobId: result.jobId,
            status: result.status,
            original: result.original,
            acceptedAt: result.acceptedAt,
          },
        });
      } catch (error) {
        sendMediaRouteError(request, reply, error);
      }
    },
  );

  app.get(
    "/api/v1/assets/:assetId/media/status",
    {
      errorHandler: mediaFrameworkErrorHandler,
      onRequest: setMediaResponseSecurityHeaders,
      schema: { params: assetParamsSchema },
    },
    async (request, reply) => {
      try {
        const { assetId } = request.params as { assetId: string };
        const ctx = await loadMediaContext(request, dependencies);
        const result = await runWithRequestCorrelation(request.correlation, () =>
          dependencies.usecase.getStatus(ctx, assetId),
        );
        reply.send({ data: result });
      } catch (error) {
        sendMediaRouteError(request, reply, error);
      }
    },
  );
}

async function setMediaResponseSecurityHeaders(_request: FastifyRequest, reply: FastifyReply): Promise<void> {
  reply.header("X-Content-Type-Options", "nosniff");
}

async function recordChecksumDeclarationRejection(request: FastifyRequest): Promise<void> {
  const body = request.body;
  if (!body || typeof body !== "object" || Array.isArray(body)) return;
  const checksum = (body as Record<string, unknown>).checksumSha256;
  if (typeof checksum !== "string" || !new RegExp(SHA256_BASE64_PATTERN).test(checksum)) {
    recordMediaRejection("checksum");
  }
}

function mediaFrameworkErrorHandler(error: FastifyError, request: FastifyRequest, reply: FastifyReply): void {
  sendMediaRouteError(request, reply, isCallerFrameworkError(error) ? routeError("BAD_REQUEST") : error);
}

/**
 * Fastify raises malformed JSON, an unparseable content type, and an oversized
 * body as plain 4xx errors with no `validation` property. Without this they fall
 * through to the dependency default and tell the caller to retry a request that
 * can never succeed.
 */
function isCallerFrameworkError(error: FastifyError): boolean {
  if (error.validation) return true;
  return typeof error.statusCode === "number" && error.statusCode >= 400 && error.statusCode < 500;
}

function rejectUnknownMediaBody(allowed: string[]) {
  const accepted = new Set(allowed);
  return async (request: FastifyRequest, reply: FastifyReply): Promise<void> => {
    const body = request.body;
    if (!body || typeof body !== "object" || Array.isArray(body)) return;
    if (Object.keys(body as Record<string, unknown>).some((key) => !accepted.has(key))) {
      const definition = getErrorDefinition("BAD_REQUEST");
      reply.status(400).send({
        error: {
          code: definition.code,
          number: definition.number,
          message: definition.message,
          service: ServiceName.ACCESS_CORE,
        },
      });
    }
  };
}

async function loadMediaContext(
  request: FastifyRequest,
  dependencies: MediaRouteDependencies,
): Promise<GraphQLContext> {
  const header = (name: "x-user-id" | "x-org-id") => {
    const value = request.headers[name];
    return typeof value === "string" ? value : null;
  };
  return dependencies.loadContext(header("x-user-id"), header("x-org-id"));
}

const statuses: Record<string, number> = {
  UNAUTHENTICATED: 401,
  FORBIDDEN: 403,
  BAD_USER_INPUT: 400,
  BAD_REQUEST: 400,
  MEDIA_UPLOAD_NOT_FOUND: 404,
  MEDIA_UPLOAD_IN_PROGRESS: 409,
  IDEMPOTENCY_KEY_REUSED: 409,
  UPLOAD_SESSION_EXPIRED: 410,
  MEDIA_PAYLOAD_TOO_LARGE: 413,
  MEDIA_TYPE_UNSUPPORTED: 415,
  MEDIA_OBJECT_MISMATCH: 409,
  MEDIA_STORAGE_UNAVAILABLE: 503,
  MEDIA_QUOTA_EXCEEDED: 413,
  MEDIA_RATE_LIMITED: 429,
  MEDIA_UPLOAD_STATE_CONFLICT: 409,
};

function routeError(code: string): Error {
  return Object.assign(new Error(code), { extensions: { code } });
}

function sendMediaRouteError(request: FastifyRequest, reply: FastifyReply, error: unknown): void {
  const candidate = error as {
    message?: unknown;
    extensions?: { code?: unknown; number?: unknown; retryAfterSeconds?: unknown };
  };
  const requestedCode = typeof candidate?.extensions?.code === "string" ? candidate.extensions.code : "INTERNAL_ERROR";
  const code = isKnownErrorCode(requestedCode) ? requestedCode : "INTERNAL_ERROR";
  const definition = getErrorDefinition(code);
  const status = statuses[code] ?? 503;
  if (code === "MEDIA_RATE_LIMITED") recordMediaRejection("rate_limited");
  else if (code === "MEDIA_QUOTA_EXCEEDED") recordMediaRejection("quota");
  else if (code === "UNAUTHENTICATED" || code === "FORBIDDEN") recordMediaRejection("authorization");
  else if (status >= 500) recordMediaRejection("dependency");
  if (status === 429 && typeof candidate?.extensions?.retryAfterSeconds === "number") {
    reply.header("Retry-After", String(candidate.extensions.retryAfterSeconds));
  }
  reply.status(status).send({
    error: {
      code: definition.code,
      number: definition.number,
      message: definition.message,
      traceId: request.correlation?.traceId,
      requestId: request.correlation?.requestId,
      service: ServiceName.ACCESS_CORE,
    },
  });
}

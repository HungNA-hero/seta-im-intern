import { GraphQLError } from "graphql";
import { mediaAssetFetch, unwrap204, unwrapEnvelope } from "../clients/assetClient";
import { config } from "../config";
import type {
  GoMediaCommitAcceptance,
  GoMediaStatus,
  GoUploadSession,
  MediaCommitAcceptance,
  MediaContentType,
  MediaStatus,
  UploadSession,
  UploadSessionCreation,
} from "../domain/media";
import {
  isAdmittedContentType,
  isAdmittedSize,
  isValidBase64Sha256,
  toProcessingStatus,
  toMediaStatus,
  toUploadSession,
} from "../domain/media";
import type { GraphQLContext } from "../graphql/context";
import { recordMediaRejection } from "../observability/prometheus";
import { assertCan, assertOrgMember } from "../graphql/context";
import {
  createAuthorizedAssetFetch,
  type AssetAuthorizationPrecondition,
  type AuthorizedAssetFetch,
} from "./authorizedAssetFetch";

export interface CreateMediaSessionInput {
  idempotencyKey: string;
  filename: string;
  contentType: MediaContentType;
  sizeBytes: number;
  checksumSha256: string;
}

export interface MediaUploadOperations {
  createSession(ctx: GraphQLContext, assetId: string, input: CreateMediaSessionInput): Promise<UploadSessionCreation>;
  getSession(ctx: GraphQLContext, assetId: string, uploadId: string): Promise<UploadSession>;
  refreshSession(ctx: GraphQLContext, assetId: string, uploadId: string): Promise<UploadSession>;
  cancelSession(ctx: GraphQLContext, assetId: string, uploadId: string): Promise<void>;
  commit(ctx: GraphQLContext, assetId: string, uploadId: string): Promise<MediaCommitAcceptance>;
  getStatus(ctx: GraphQLContext, assetId: string): Promise<MediaStatus>;
}

export interface MediaSessionRateLimiter {
  consumeSessionCreation(input: { userId: string; orgId: string }): Promise<void>;
}

export interface MediaUploadUsecaseDependencies {
  authorizedAsset: AuthorizedAssetFetch;
  rateLimiter: MediaSessionRateLimiter;
  requireHTTPS?: boolean;
}

const writeMedia = (assetId: string) => ({
  action: "write" as const,
  resourceType: "metadata_item" as const,
  resourceId: assetId,
});

const readMedia = (assetId: string) => ({
  action: "read" as const,
  resourceType: "metadata_item" as const,
  resourceId: assetId,
});

const alreadyAuthorized: AssetAuthorizationPrecondition[] = [];

export function createMediaUploadUsecase({
  authorizedAsset,
  rateLimiter,
  requireHTTPS = false,
}: MediaUploadUsecaseDependencies): MediaUploadOperations {
  function requireMember(
    ctx: GraphQLContext,
  ): asserts ctx is GraphQLContext & { userId: string; currentOrgId: string } {
    assertOrgMember(ctx);
  }

  async function request(
    ctx: GraphQLContext,
    assetId: string,
    path: string,
    init: {
      method?: "GET" | "POST" | "PUT" | "DELETE";
      body?: Record<string, unknown>;
      idempotencyKey?: string;
    } = {},
    preconditions: AssetAuthorizationPrecondition[] = [writeMedia(assetId)],
  ): Promise<Response> {
    requireMember(ctx);
    return authorizedAsset.authorizedFetch({
      ctx,
      orgId: ctx.currentOrgId,
      path,
      preconditions,
      init,
    });
  }

  return {
    async createSession(ctx, assetId, input) {
      requireMember(ctx);
      if (
        !input.filename ||
        !isAdmittedContentType(input.contentType) ||
        !isAdmittedSize(input.sizeBytes) ||
        !isValidBase64Sha256(input.checksumSha256)
      ) {
        throw new GraphQLError("Invalid media upload declaration", { extensions: { code: "BAD_USER_INPUT" } });
      }
      await authorizedAsset.assertPreconditions({
        ctx,
        orgId: ctx.currentOrgId,
        preconditions: [writeMedia(assetId)],
      });
      await rateLimiter.consumeSessionCreation({ userId: ctx.userId, orgId: ctx.currentOrgId });
      const response = await request(
        ctx,
        assetId,
        `/internal/api/v1/metadata-items/${assetId}/media/uploads`,
        {
          method: "POST",
          idempotencyKey: input.idempotencyKey,
          body: {
            filename: input.filename,
            content_type: input.contentType,
            size_bytes: input.sizeBytes,
            checksum_sha256: input.checksumSha256,
          },
        },
        alreadyAuthorized,
      );
      const replayed = response.status === 200;
      const session = await unwrapEnvelope(
        response,
        "data",
        (raw) => toUploadSession(assertGoUploadSession(raw, requireHTTPS), `/api/v1/assets/${assetId}/media`),
        "Create media upload session",
      );
      return { ...session, replayed };
    },

    async getSession(ctx, assetId, uploadId) {
      const response = await request(
        ctx,
        assetId,
        `/internal/api/v1/metadata-items/${assetId}/media/uploads/${uploadId}`,
      );
      return unwrapEnvelope(
        response,
        "data",
        (raw) => toUploadSession(assertGoUploadSession(raw, requireHTTPS), `/api/v1/assets/${assetId}/media`),
        "Get media upload session",
      );
    },

    async refreshSession(ctx, assetId, uploadId) {
      const response = await request(
        ctx,
        assetId,
        `/internal/api/v1/metadata-items/${assetId}/media/uploads/${uploadId}/refresh`,
        { method: "POST" },
      );
      return unwrapEnvelope(
        response,
        "data",
        (raw) => toUploadSession(assertGoUploadSession(raw, requireHTTPS), `/api/v1/assets/${assetId}/media`),
        "Refresh media upload session",
      );
    },

    async cancelSession(ctx, assetId, uploadId) {
      const response = await request(
        ctx,
        assetId,
        `/internal/api/v1/metadata-items/${assetId}/media/uploads/${uploadId}`,
        { method: "DELETE" },
      );
      await unwrap204(response, "Cancel media upload session");
    },

    async commit(ctx, assetId, uploadId) {
      const response = await request(ctx, assetId, `/internal/api/v1/metadata-items/${assetId}/media`, {
        method: "PUT",
        body: { upload_id: uploadId },
      });
      const replayed = response.status === 200;
      const result = await unwrapEnvelope(response, "data", assertGoCommitAcceptance, "Commit media upload");
      const status = toProcessingStatus(result.status);
      if (status !== "QUEUED") throwInvalidAssetMediaResponse();
      return {
        assetId: result.asset_id,
        uploadId: result.upload_id,
        jobId: result.job_id,
        status,
        original: {
          filename: result.original.filename,
          contentType: result.original.content_type,
          sizeBytes: result.original.size_bytes,
        },
        acceptedAt: result.accepted_at,
        replayed,
      };
    },

    async getStatus(ctx, assetId) {
      const response = await request(ctx, assetId, `/internal/api/v1/metadata-items/${assetId}/media/status`, {}, [
        readMedia(assetId),
      ]);
      return unwrapEnvelope(
        response,
        "data",
        (raw) => toMediaStatus(assertGoMediaStatus(raw, requireHTTPS)),
        "Get media status",
      );
    },
  };
}

function assertRecord(value: unknown): asserts value is Record<string, any> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throwInvalidAssetMediaResponse();
}

function assertGoUploadSession(value: unknown, requireHTTPS: boolean): GoUploadSession {
  assertRecord(value);
  if (
    typeof value.upload_id !== "string" ||
    typeof value.asset_id !== "string" ||
    typeof value.state !== "string" ||
    typeof value.session_expires_at !== "string" ||
    (value.upload !== null && value.upload !== undefined && typeof value.upload !== "object")
  ) {
    throwInvalidAssetMediaResponse();
  }
  if (value.upload !== null && value.upload !== undefined) {
    assertRecord(value.upload);
    if (
      !hasOnlyKeys(value.upload, ["protocol", "method", "url", "headers", "credential_expires_at"]) ||
      value.upload.protocol !== "HTTP" ||
      value.upload.method !== "PUT" ||
      typeof value.upload.credential_expires_at !== "string" ||
      !isStringRecord(value.upload.headers)
    ) {
      throwInvalidAssetMediaResponse();
    }
    assertSafeUploadHeaders(value.upload.headers);
    assertSecureReturnedURL(value.upload.url, requireHTTPS);
  }
  return value as GoUploadSession;
}

function assertGoCommitAcceptance(value: unknown): GoMediaCommitAcceptance {
  assertRecord(value);
  assertRecord(value.original);
  if (
    typeof value.asset_id !== "string" ||
    typeof value.upload_id !== "string" ||
    typeof value.job_id !== "string" ||
    typeof value.status !== "string" ||
    typeof value.accepted_at !== "string" ||
    typeof value.original.filename !== "string" ||
    !isAdmittedContentType(value.original.content_type) ||
    !isAdmittedSize(value.original.size_bytes)
  ) {
    throwInvalidAssetMediaResponse();
  }
  return value as GoMediaCommitAcceptance;
}

function assertGoMediaStatus(value: unknown, requireHTTPS: boolean): GoMediaStatus {
  assertRecord(value);
  if (
    !hasOnlyKeys(value, [
      "asset_id",
      "upload_id",
      "job_id",
      "status",
      "attempt_count",
      "stage",
      "original",
      "outputs",
      "error",
      "accepted_at",
      "started_at",
      "completed_at",
      "failed_at",
    ])
  )
    throwInvalidAssetMediaResponse();
  assertRecord(value.original);
  if (
    !hasOnlyKeys(value.original, ["filename", "declared_content_type", "detected_content_type", "size_bytes", "sha256"])
  )
    throwInvalidAssetMediaResponse();
  if (
    typeof value.asset_id !== "string" ||
    typeof value.upload_id !== "string" ||
    typeof value.job_id !== "string" ||
    typeof value.status !== "string" ||
    !Number.isInteger(value.attempt_count) ||
    value.attempt_count < 0 ||
    value.attempt_count > 3 ||
    (value.stage !== null && value.stage !== undefined && typeof value.stage !== "string") ||
    typeof value.original.filename !== "string" ||
    !isAdmittedContentType(value.original.declared_content_type) ||
    (value.original.detected_content_type !== null &&
      value.original.detected_content_type !== undefined &&
      !isAdmittedContentType(value.original.detected_content_type)) ||
    !Number.isSafeInteger(value.original.size_bytes) ||
    value.original.size_bytes < 1 ||
    (value.original.sha256 !== null &&
      value.original.sha256 !== undefined &&
      (typeof value.original.sha256 !== "string" || !/^[0-9a-f]{64}$/.test(value.original.sha256))) ||
    typeof value.accepted_at !== "string" ||
    !nullableString(value.started_at) ||
    !nullableString(value.completed_at) ||
    !nullableString(value.failed_at)
  ) {
    throwInvalidAssetMediaResponse();
  }

  const status = toProcessingStatus(value.status);
  const stage = value.stage === null || value.stage === undefined ? null : value.stage.toUpperCase();
  if (status === "PROCESSING") {
    if (stage !== "VALIDATING" && stage !== "TRANSFORMING") throwInvalidAssetMediaResponse();
  } else if (stage !== null) {
    throwInvalidAssetMediaResponse();
  }

  if (value.outputs !== null && value.outputs !== undefined) {
    assertRecord(value.outputs);
    if (
      !hasOnlyKeys(value.outputs, ["thumbnail", "web", "expires_at"]) ||
      typeof value.outputs.expires_at !== "string"
    ) {
      throwInvalidAssetMediaResponse();
    }
    assertGoMediaOutput(value.outputs.thumbnail, 256, requireHTTPS);
    assertGoMediaOutput(value.outputs.web, 1080, requireHTTPS);
  }
  if ((status === "COMPLETED") !== (value.outputs !== null && value.outputs !== undefined)) {
    throwInvalidAssetMediaResponse();
  }

  if (value.error !== null && value.error !== undefined) {
    assertRecord(value.error);
    if (
      !hasOnlyKeys(value.error, ["code", "message"]) ||
      typeof value.error.code !== "string" ||
      typeof value.error.message !== "string"
    ) {
      throwInvalidAssetMediaResponse();
    }
  }
  if ((status === "FAILED") !== (value.error !== null && value.error !== undefined)) {
    throwInvalidAssetMediaResponse();
  }
  return value as GoMediaStatus;
}

function assertGoMediaOutput(value: unknown, bound: number, requireHTTPS: boolean): void {
  assertRecord(value);
  if (
    !hasOnlyKeys(value, ["url", "width", "height", "size_bytes", "content_type"]) ||
    typeof value.url !== "string" ||
    !Number.isInteger(value.width) ||
    value.width < 1 ||
    value.width > bound ||
    !Number.isInteger(value.height) ||
    value.height < 1 ||
    value.height > bound ||
    !Number.isSafeInteger(value.size_bytes) ||
    value.size_bytes < 1 ||
    !isAdmittedContentType(value.content_type)
  ) {
    throwInvalidAssetMediaResponse();
  }
  assertSecureReturnedURL(value.url, requireHTTPS);
}

function assertSecureReturnedURL(value: unknown, requireHTTPS: boolean): void {
  if (typeof value !== "string" || value.length > 8192) {
    recordMediaRejection("descriptor");
    throwInvalidAssetMediaResponse();
  }
  try {
    const parsed = new URL(value);
    const allowedProtocol = parsed.protocol === "https:" || (!requireHTTPS && parsed.protocol === "http:");
    if (!allowedProtocol || !parsed.hostname || parsed.username || parsed.password) {
      recordMediaRejection("descriptor");
      throwInvalidAssetMediaResponse();
    }
  } catch (error) {
    if (error instanceof GraphQLError) throw error;
    recordMediaRejection("descriptor");
    throwInvalidAssetMediaResponse();
  }
}

function assertSafeUploadHeaders(headers: Record<string, string>): void {
  const allowed = new Set(["content-type", "content-length", "if-none-match", "x-amz-checksum-sha256"]);
  const normalized = new Map<string, string>();
  for (const [name, value] of Object.entries(headers)) {
    const lowerName = name.toLowerCase();
    if (
      !allowed.has(lowerName) ||
      normalized.has(lowerName) ||
      name.length > 64 ||
      value.length > 4096 ||
      /[\r\n]/.test(name) ||
      /[\r\n]/.test(value)
    ) {
      rejectInvalidDescriptor();
    }
    normalized.set(lowerName, value);
  }
  if (
    !isAdmittedContentType(normalized.get("content-type")) ||
    !isCanonicalAdmittedContentLength(normalized.get("content-length")) ||
    normalized.get("if-none-match") !== "*" ||
    (normalized.has("x-amz-checksum-sha256") && !isValidBase64Sha256(normalized.get("x-amz-checksum-sha256") ?? ""))
  ) {
    rejectInvalidDescriptor();
  }
}

function isCanonicalAdmittedContentLength(value: string | undefined): boolean {
  return value !== undefined && /^[1-9]\d{0,7}$/.test(value) && isAdmittedSize(Number(value));
}

function rejectInvalidDescriptor(): never {
  recordMediaRejection("descriptor");
  throwInvalidAssetMediaResponse();
}

function isStringRecord(value: unknown): value is Record<string, string> {
  return (
    value !== null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Object.values(value).every((entry) => typeof entry === "string")
  );
}

function hasOnlyKeys(value: Record<string, any>, allowed: string[]): boolean {
  const accepted = new Set(allowed);
  return Object.keys(value).every((key) => accepted.has(key));
}

function nullableString(value: unknown): boolean {
  return value === null || value === undefined || typeof value === "string";
}

function throwInvalidAssetMediaResponse(): never {
  throw new GraphQLError("Asset Core returned an invalid media response", { extensions: { code: "INTERNAL_ERROR" } });
}

const authorizedMediaAsset = createAuthorizedAssetFetch({
  authorization: { assertAllowed: assertCan },
  transport: { request: mediaAssetFetch },
});

export function createAuthorizedMediaUploadUsecase(rateLimiter: MediaSessionRateLimiter): MediaUploadOperations {
  return createMediaUploadUsecase({
    authorizedAsset: authorizedMediaAsset,
    rateLimiter,
    requireHTTPS: config.mediaRequireHTTPS,
  });
}

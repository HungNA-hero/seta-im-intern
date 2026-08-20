import Fastify, { FastifyInstance, FastifyReply, FastifyRequest } from "fastify";
import { createYoga } from "graphql-yoga";
import { schema } from "./graphql/schema";
import { GraphQLContext, loadRequestContext } from "./graphql/context";
import { maskGraphQLError } from "./graphql/errorMasking";
import { logRequestCompletion } from "./observability/requestLogging";
import { renderPrometheusMetrics } from "./observability/prometheus";
import { config } from "./config";
import {
  createRequestCorrelation,
  RequestCorrelation,
  runWithRequestCorrelation,
} from "./observability/requestContext";
import { registerMediaRoutes } from "./rest/mediaRoutes";
import { mediaRateLimiter } from "./rest/mediaRateLimit";
import { createAuthorizedMediaUploadUsecase } from "./usecase/mediaUploadUsecase";

const mediaUploadUsecase = createAuthorizedMediaUploadUsecase(mediaRateLimiter);

interface YogaServerContext {
  fastifyRequest: FastifyRequest;
}

export interface MediaTransportSecurityOptions {
  requireHTTPS: boolean;
  allowedOrigins: readonly string[];
  trustedProxies?: readonly string[];
}

const mediaPathPattern = /^\/api\/v1\/assets\/[^/]+\/media(?:\/|$)/;
const mediaCorsMethods = new Set(["GET", "POST", "PUT", "DELETE"]);
const mediaCorsHeaders = new Set(["content-type", "idempotency-key", "x-user-id", "x-org-id"]);

function normalizePeerAddress(address: string | undefined): string | undefined {
  if (address?.startsWith("::ffff:")) return address.slice("::ffff:".length);
  return address;
}

function isTrustedImmediatePeer(request: FastifyRequest, trustedProxies: readonly string[]): boolean {
  const peer = normalizePeerAddress(request.raw.socket.remoteAddress);
  return peer !== undefined && trustedProxies.some((candidate) => normalizePeerAddress(candidate) === peer);
}

function forwardedProtocol(request: FastifyRequest): string | undefined {
  const value = request.headers["x-forwarded-proto"];
  const flattened = Array.isArray(value) ? value.at(-1) : value;
  return flattened?.split(",").at(-1)?.trim().toLowerCase();
}

function isSecureMediaRequest(request: FastifyRequest, trustedProxies: readonly string[]): boolean {
  if ("encrypted" in request.raw.socket && request.raw.socket.encrypted === true) return true;
  return isTrustedImmediatePeer(request, trustedProxies) && forwardedProtocol(request) === "https";
}

function sendTransportError(reply: FastifyReply, status: 400 | 403): void {
  const forbidden = status === 403;
  reply.status(status).send({
    error: {
      code: forbidden ? "FORBIDDEN" : "BAD_REQUEST",
      number: forbidden ? 2003 : 1001,
      message: forbidden ? "The requested action is not permitted" : "Request could not be accepted",
      service: "access-core",
    },
  });
}

function requestedCorsHeaders(request: FastifyRequest): string[] {
  const raw = request.headers["access-control-request-headers"];
  if (typeof raw !== "string") return [];
  return raw
    .split(",")
    .map((header) => header.trim().toLowerCase())
    .filter(Boolean);
}

function enforceMediaCors(request: FastifyRequest, reply: FastifyReply, allowedOrigins: readonly string[]): boolean {
  const origin = request.headers.origin;
  const isPreflight = request.method === "OPTIONS";
  if (typeof origin !== "string" || !allowedOrigins.includes(origin)) {
    if (origin !== undefined || isPreflight) sendTransportError(reply, 403);
    return origin === undefined && !isPreflight;
  }

  reply.header("Access-Control-Allow-Origin", origin);
  reply.header("Vary", "Origin");
  if (!isPreflight) return true;

  const requestedMethod = request.headers["access-control-request-method"];
  const headers = requestedCorsHeaders(request);
  if (
    typeof requestedMethod !== "string" ||
    !mediaCorsMethods.has(requestedMethod.toUpperCase()) ||
    headers.some((header) => !mediaCorsHeaders.has(header))
  ) {
    reply.removeHeader("Access-Control-Allow-Origin");
    sendTransportError(reply, 403);
    return false;
  }

  reply.header("Access-Control-Allow-Methods", [...mediaCorsMethods].join(", "));
  reply.header("Access-Control-Allow-Headers", [...mediaCorsHeaders].join(", "));
  reply.status(204).send();
  return false;
}

export function mediaTransportSecurityHook(options: MediaTransportSecurityOptions) {
  return async (request: FastifyRequest, reply: FastifyReply): Promise<void> => {
    const path = request.url.split("?", 1)[0];
    if (!mediaPathPattern.test(path)) return;

    const secure = isSecureMediaRequest(request, options.trustedProxies ?? []);
    if (options.requireHTTPS && !secure) {
      sendTransportError(reply, 400);
      return;
    }
    if (secure) {
      reply.header("Strict-Transport-Security", "max-age=31536000; includeSubDomains");
    }
    if (!enforceMediaCors(request, reply, options.allowedOrigins)) return;
  };
}

declare module "fastify" {
  interface FastifyRequest {
    correlation: RequestCorrelation;
  }
}

export async function buildServer(): Promise<FastifyInstance> {
  const app = Fastify({
    logger: true,
    disableRequestLogging: true,
    trustProxy: config.trustedProxies.length > 0 ? config.trustedProxies : false,
  });
  app.decorateRequest("correlation", null);

  app.addHook("onRequest", async (request) => {
    request.correlation = createRequestCorrelation(request.headers);
  });

  app.addHook(
    "onRequest",
    mediaTransportSecurityHook({
      requireHTTPS: config.mediaRequireHTTPS,
      allowedOrigins: config.mediaAllowedOrigins,
      trustedProxies: config.trustedProxies,
    }),
  );

  app.addHook("onResponse", async (request, reply) => {
    logRequestCompletion(request, reply, config.metricsEnabled);
  });

  const yoga = createYoga<YogaServerContext, GraphQLContext>({
    schema,
    graphqlEndpoint: "/graphql",
    logging: false,
    maskedErrors: { maskError: maskGraphQLError },
    context: (ctx) => {
      const header = (name: string) => (ctx.fastifyRequest.headers[name] as string | undefined) ?? null;
      return loadRequestContext(header("x-user-id"), header("x-org-id"));
    },
  });

  app.route({
    url: "/graphql",
    method: ["GET", "POST", "OPTIONS"],
    handler: async (req, reply) => {
      const response = await runWithRequestCorrelation(req.correlation, () =>
        yoga.fetch(
          `http://${req.headers.host}${req.url}`,
          {
            method: req.method,
            headers: req.headers as HeadersInit,
            body: req.method !== "GET" && req.method !== "HEAD" ? JSON.stringify(req.body) : undefined,
          },
          { fastifyRequest: req },
        ),
      );
      response.headers.forEach((value, key) => reply.header(key, value));
      reply.status(response.status);
      reply.send(await response.text());
    },
  });

  if (config.mediaUploadEnabled) {
    registerMediaRoutes(app, {
      loadContext: loadRequestContext,
      usecase: mediaUploadUsecase,
    });
  }

  app.get("/health", async () => ({ status: "ok" }));

  if (config.metricsEnabled) {
    app.get("/metrics", async (_request, reply) => {
      reply.type("text/plain; version=0.0.4; charset=utf-8");
      return renderPrometheusMetrics();
    });
  }

  return app;
}

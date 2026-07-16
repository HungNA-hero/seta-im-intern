import Fastify, { FastifyInstance, FastifyRequest } from "fastify";
import { createYoga } from "graphql-yoga";
import { GraphQLError } from "graphql";
import { schema } from "./graphql/schema";
import { GraphQLContext, loadRequestContext } from "./graphql/context";
import { getErrorDefinition, isKnownErrorCode } from "./errorCodes";
import {
  createRequestCorrelation,
  getRequestCorrelation,
  RequestCorrelation,
  recordRequestError,
  runWithRequestCorrelation,
  isTraceId,
} from "./observability/requestContext";

interface YogaServerContext {
  fastifyRequest: FastifyRequest;
}

declare module "fastify" {
  interface FastifyRequest {
    correlation: RequestCorrelation;
  }
}

export function maskGraphQLError(error: unknown, fallbackMessage: string): Error {
  const executionError = error instanceof GraphQLError ? error : undefined;
  const visited = new Set<unknown>();
  let current: unknown = error;

  while (current && typeof current === "object" && !visited.has(current)) {
    visited.add(current);
    const candidate = current as {
      name?: unknown;
      message?: unknown;
      extensions?: { code?: unknown; service?: unknown; traceId?: unknown };
      originalError?: unknown;
      cause?: unknown;
    };
    const code = candidate.extensions?.code;
    if (
      candidate.name === "GraphQLError" &&
      typeof code === "string" &&
      isKnownErrorCode(code)
    ) {
      const definition = getErrorDefinition(code);
      const service =
        candidate.extensions?.service === "asset-core"
          ? "asset-core"
          : "access-core";
      const traceId =
        candidate.extensions?.traceId ?? executionError?.extensions?.traceId;
      const correlationTraceId =
        isTraceId(traceId)
          ? traceId
          : getRequestCorrelation()?.traceId;
      recordRequestError(definition.code, definition.number);
      return new GraphQLError(
        definition.message || fallbackMessage,
        {
          nodes: executionError?.nodes,
          path: executionError?.path,
          extensions: {
            code: definition.code,
            number: definition.number,
            traceId: correlationTraceId,
            service,
          },
        },
      );
    }
    current = candidate.originalError ?? candidate.cause;
  }

  const definition = getErrorDefinition("INTERNAL_ERROR");
  recordRequestError(definition.code, definition.number);
  return new GraphQLError(definition.message || fallbackMessage, {
    extensions: {
      code: definition.code,
      number: definition.number,
      traceId: getRequestCorrelation()?.traceId,
      service: "access-core",
    },
  });
}

export async function buildServer(): Promise<FastifyInstance> {
  const app = Fastify({ logger: true, disableRequestLogging: true });
  app.decorateRequest("correlation", null);

  app.addHook("onRequest", async (request) => {
    request.correlation = createRequestCorrelation(request.headers);
  });

  app.addHook("onResponse", async (request, reply) => {
    const correlation = request.correlation;
    const result =
      correlation.errorCode !== undefined
        ? correlation.errorCode === "INTERNAL_ERROR"
          ? "failure"
          : "validation_error"
        : reply.statusCode >= 500
          ? "failure"
          : reply.statusCode >= 400
            ? "denied"
            : "success";
    request.log.info(
      {
        service: "access-core",
        traceId: correlation.traceId,
        requestId: correlation.requestId,
        operation: request.routeOptions.url ?? request.method,
        durationMs: Math.max(0, Date.now() - correlation.startedAt),
        result,
        errorCode: correlation.errorCode,
        errorNumber: correlation.errorNumber,
        http: {
          method: request.method,
          route: request.routeOptions.url ?? request.url.split("?")[0],
          status: reply.statusCode,
        },
      },
      "request completed",
    );
  });

  const yoga = createYoga<YogaServerContext, GraphQLContext>({
    schema,
    graphqlEndpoint: "/graphql",
    logging: false,
    maskedErrors: { maskError: maskGraphQLError },
    context: (ctx) => {
      const h = (k: string) =>
        (ctx.fastifyRequest.headers[k] as string | undefined) ?? null;
      return loadRequestContext(
        h("x-user-id"),
        h("x-org-id"),
        ctx.fastifyRequest.correlation,
      );
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
            body:
              req.method !== "GET" && req.method !== "HEAD"
                ? JSON.stringify(req.body)
                : undefined,
          },
          { fastifyRequest: req },
        ),
      );
      response.headers.forEach((value, key) => reply.header(key, value));
      reply.status(response.status);
      reply.send(await response.text());
    },
  });

  app.get("/health", async () => ({ status: "ok" }));

  return app;
}

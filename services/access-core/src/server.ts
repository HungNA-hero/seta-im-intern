import Fastify, { FastifyInstance } from "fastify";
import { createYoga } from "graphql-yoga";
import { GraphQLError } from "graphql";
import { schema } from "./graphql/schema";
import { loadRequestContext } from "./graphql/context";

/** Error codes whose messages are safe to expose through the public GraphQL API. */
const PUBLIC_ERROR_CODES = new Set([
  "BAD_USER_INPUT",
  "UNAUTHENTICATED",
  "FORBIDDEN",
  "NOT_FOUND",
  "CONFLICT",
]);

/** Preserves approved API errors while masking unexpected resolver failures. */
function maskGraphQLError(error: unknown, fallbackMessage: string): Error {
  const executionError = error instanceof GraphQLError ? error : undefined;
  const visited = new Set<unknown>();
  let current: unknown = error;

  // GraphQL execution and directive wrappers can create more than one originalError layer.
  while (current && typeof current === "object" && !visited.has(current)) {
    visited.add(current);
    const candidate = current as {
      name?: unknown;
      message?: unknown;
      extensions?: { code?: unknown };
      originalError?: unknown;
      cause?: unknown;
    };
    const code = candidate.extensions?.code;
    if (
      candidate.name === "GraphQLError" &&
      typeof code === "string" &&
      PUBLIC_ERROR_CODES.has(code)
    ) {
      return new GraphQLError(
        typeof candidate.message === "string"
          ? candidate.message
          : fallbackMessage,
        {
          nodes: executionError?.nodes,
          path: executionError?.path,
          extensions: { code },
        },
      );
    }
    current = candidate.originalError ?? candidate.cause;
  }

  return new GraphQLError(fallbackMessage, {
    extensions: { code: "INTERNAL_SERVER_ERROR" },
  });
}

export async function buildServer(): Promise<FastifyInstance> {
  const app = Fastify({ logger: true });

  const yoga = createYoga({
    schema,
    graphqlEndpoint: "/graphql",
    logging: false,
    maskedErrors: { maskError: maskGraphQLError },
    context: (ctx: any) => {
      const h = (k: string) =>
        (ctx.fastifyRequest?.headers[k] as string | undefined) ?? null;
      return loadRequestContext(h("x-user-id"), h("x-org-id"));
    },
  });

  app.route({
    url: "/graphql",
    method: ["GET", "POST", "OPTIONS"],
    handler: async (req, reply) => {
      const response = await yoga.fetch(
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
      );
      response.headers.forEach((value, key) => reply.header(key, value));
      reply.status(response.status);
      reply.send(await response.text());
    },
  });

  app.get("/health", async () => ({ status: "ok" }));

  return app;
}

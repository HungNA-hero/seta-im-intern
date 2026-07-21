import Fastify, { FastifyInstance, FastifyRequest } from "fastify";
import { createYoga } from "graphql-yoga";
import { schema } from "./graphql/schema";
import { GraphQLContext, loadRequestContext } from "./graphql/context";
import { maskGraphQLError } from "./graphql/errorMasking";
import { logRequestCompletion } from "./observability/requestLogging";
import {
  createRequestCorrelation,
  RequestCorrelation,
  runWithRequestCorrelation,
} from "./observability/requestContext";

interface YogaServerContext {
  fastifyRequest: FastifyRequest;
}

declare module "fastify" {
  interface FastifyRequest {
    correlation: RequestCorrelation;
  }
}

export async function buildServer(): Promise<FastifyInstance> {
  const app = Fastify({ logger: true, disableRequestLogging: true });
  app.decorateRequest("correlation", null);

  app.addHook("onRequest", async (request) => {
    request.correlation = createRequestCorrelation(request.headers);
  });

  app.addHook("onResponse", async (request, reply) => {
    logRequestCompletion(request, reply);
  });

  const yoga = createYoga<YogaServerContext, GraphQLContext>({
    schema,
    graphqlEndpoint: "/graphql",
    logging: false,
    maskedErrors: { maskError: maskGraphQLError },
    context: (ctx) => {
      const h = (k: string) =>
        (ctx.fastifyRequest.headers[k] as string | undefined) ?? null;
      return loadRequestContext(h("x-user-id"), h("x-org-id"));
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

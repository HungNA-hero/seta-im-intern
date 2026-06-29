import Fastify, { FastifyInstance } from "fastify";
import { createYoga } from "graphql-yoga";
import { schema, GraphQLContext, PolicyGuard } from "./graphql/schema";

export type ServerDependencies = {
  policyGuard?: PolicyGuard;
};

export async function buildServer(
  dependencies: ServerDependencies = {},
): Promise<FastifyInstance> {
  const fastify = Fastify({ logger: true });

  const yoga = createYoga<GraphQLContext>({
    schema,
    graphqlEndpoint: "/graphql",
    logging: false,
    context: (req) => {
      return {
        requester: req.request.headers.get("x-user-id"),
        currentOrg: req.request.headers.get("x-org-id"),
        policyGuard: dependencies.policyGuard,
      };
    },
  });

  fastify.route({
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
      );
      response.headers.forEach((value, key) => reply.header(key, value));
      reply.status(response.status);
      const text = await response.text();
      reply.send(text);
    },
  });

  fastify.get("/health", async () => ({ status: "ok" }));

  return fastify;
}

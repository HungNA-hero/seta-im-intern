import Fastify, { FastifyInstance } from "fastify";
import { createYoga } from "graphql-yoga";
import { schema } from "./graphql/schema";
import { loadRequestContext } from "./graphql/context";

export async function buildServer(): Promise<FastifyInstance> {
  const app = Fastify({ logger: true });

  const yoga = createYoga<GraphQLContext>({
    schema,
    graphqlEndpoint: "/graphql",
    logging: false,
    context: (ctx: any) => {
      const h = (k: string) =>
        (ctx.fastifyRequest?.headers[k] as string | undefined) ?? null;
      return loadRequestContext(h("x-requester-id"), h("x-org-id"));
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

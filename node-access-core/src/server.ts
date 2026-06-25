import Fastify, { FastifyInstance } from 'fastify';
import { createYoga }              from 'graphql-yoga';
import { schema }                  from './graphql/schema';

export async function buildServer(): Promise<FastifyInstance> {
  const fastify = Fastify({ logger: true });

  const yoga = createYoga({
    schema,
    graphqlEndpoint: '/graphql',
    logging: false,
  });

  fastify.route({
    url:    '/graphql',
    method: ['GET', 'POST', 'OPTIONS'],
    handler: async (req, reply) => {
      const response = await yoga.handleNodeRequest(req.raw, {});
      response.headers.forEach((value, key) => reply.header(key, value));
      reply.status(response.status);
      reply.send(response.body);
    },
  });

  fastify.get('/health', async () => ({ status: 'ok' }));

  return fastify;
}

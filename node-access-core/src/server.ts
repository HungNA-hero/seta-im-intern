import Fastify, { FastifyInstance } from 'fastify';
import { createYoga }              from 'graphql-yoga';
import { schema }                  from './graphql/schema';

export async function buildServer(): Promise<FastifyInstance> {
  const fastify = Fastify({ logger: true });

  fastify.get('/health', async () => ({ status: 'ok' }));

  return fastify;
}

import 'fastify';

declare module 'fastify' {
  interface FastifyRequest {
    requesterId: string | null;
    currentOrgId: string | null;
  }
}

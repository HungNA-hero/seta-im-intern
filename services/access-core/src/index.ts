import './config';
import { buildServer } from './server';
import { assertRuntimeConfig, config } from './config';
import { prisma }      from './db/prisma';

async function main() {
  assertRuntimeConfig();
  try {
    await prisma.$queryRaw`SELECT 1`;
    console.log('Connected to access_db successfully');
  } catch (err) {
    console.warn('Could not connect to access_db at startup:', err);
  }

  const server = await buildServer();
  await server.listen({ port: config.port, host: config.host });
}

main().catch((err) => {
  console.error('Fatal startup error:', err);
  process.exit(1);
});

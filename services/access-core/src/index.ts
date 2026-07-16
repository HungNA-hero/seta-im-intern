import './config';
import { buildServer } from './server';
import { assertRuntimeConfig, config } from './config';
import { prisma }      from './db/prisma';

function logStartup(level: "info" | "warn" | "error", message: string, error?: unknown) {
  process.stdout.write(`${JSON.stringify({
    level,
    service: "access-core",
    message,
    error: error instanceof Error ? error.message : undefined,
    timestamp: new Date().toISOString(),
  })}\n`);
}

async function main() {
  assertRuntimeConfig();
  try {
    await prisma.$queryRaw`SELECT 1`;
    logStartup("info", "connected to access_db successfully");
  } catch (err) {
    logStartup("warn", "could not connect to access_db at startup", err);
  }

  const server = await buildServer();
  await server.listen({ port: config.port, host: config.host });
}

main().catch((err) => {
  logStartup("error", "fatal startup error", err);
  process.exit(1);
});

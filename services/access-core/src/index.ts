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

  let shuttingDown = false;
  const shutdown = async (signal: NodeJS.Signals) => {
    if (shuttingDown) return;
    shuttingDown = true;
    logStartup("info", `received ${signal}; shutting down gracefully`);

    const forceExit = setTimeout(() => {
      logStartup("error", "graceful shutdown timed out");
      process.exit(1);
    }, 10_000);
    forceExit.unref();

    try {
      await server.close();
      await prisma.$disconnect();
      logStartup("info", "graceful shutdown complete");
    } catch (err) {
      logStartup("error", "graceful shutdown failed", err);
      process.exitCode = 1;
    } finally {
      clearTimeout(forceExit);
    }
  };

  (["SIGTERM", "SIGINT"] as const).forEach((signal) =>
    process.once(signal, () => void shutdown(signal)),
  );
}

main().catch((err) => {
  logStartup("error", "fatal startup error", err);
  process.exit(1);
});

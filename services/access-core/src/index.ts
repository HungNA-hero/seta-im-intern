import "./config";
import { buildServer } from "./server";
import { assertRuntimeConfig, config } from "./config";
import { prisma } from "./db/prisma";
import { registerGracefulShutdown } from "./lifecycle";
import { ServiceName } from "./observability/serviceName";
import { startCacheInvalidator } from "./eventing/cacheInvalidator";
import { closeRedisConsumerClient } from "./cache/redisClient";
import { disableAssetBreakerCapacityLog, shutdownAssetBreaker } from "./clients/assetBreaker";

function logStartup(level: "info" | "warn" | "error", message: string, error?: unknown) {
  process.stdout.write(
    `${JSON.stringify({
      level,
      service: ServiceName.ACCESS_CORE,
      message,
      error: error instanceof Error ? error.message : undefined,
      timestamp: new Date().toISOString(),
    })}\n`,
  );
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

  const cacheInvalidator = startCacheInvalidator();

  registerGracefulShutdown(
    [
      { name: "server", close: () => server.close() },
      {
        name: "cacheInvalidator",
        close: async () => {
          cacheInvalidator.stop();
          await closeRedisConsumerClient();
        },
      },
      {
        name: "assetBreaker",
        immediate: () => disableAssetBreakerCapacityLog(),
        close: () => shutdownAssetBreaker(),
      },
      { name: "prisma", close: () => prisma.$disconnect() },
    ],
    logStartup,
  );
}

main().catch((err) => {
  logStartup("error", "fatal startup error", err);
  process.exit(1);
});

type Logger = (level: "info" | "warn" | "error", message: string, error?: unknown) => void;

type ShutdownTarget = { name: string; close: () => Promise<unknown> };

export function registerGracefulShutdown(
  targets: ShutdownTarget[],
  log: Logger,
  timeoutMs = 10_000,
) {
  let shuttingDown = false;
  const shutdown = async (signal: NodeJS.Signals) => {
    if (shuttingDown) return;
    shuttingDown = true;
    log("info", `received ${signal}; shutting down gracefully`);

    const forceExit = setTimeout(() => {
      log("error", "graceful shutdown timed out");
      process.exit(1);
    }, timeoutMs);
    forceExit.unref();

    try {
      for (const target of targets) {
        await target.close();
      }
      log("info", "graceful shutdown complete");
    } catch (err) {
      log("error", "graceful shutdown failed", err);
      process.exitCode = 1;
    } finally {
      clearTimeout(forceExit);
    }
  };

  (["SIGTERM", "SIGINT"] as const).forEach((signal) =>
    process.once(signal, () => void shutdown(signal)),
  );
}

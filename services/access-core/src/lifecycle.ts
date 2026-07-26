type Logger = (level: "info" | "warn" | "error", message: string, error?: unknown) => void;

type ShutdownTarget = {
  name: string;
  close: () => Promise<unknown> | void;
  /** Runs synchronously the instant shutdown begins, before any target's `close`
   * (which may block on request drain). Use for state that must stop immediately
   * rather than wait its turn in the sequential close order. */
  immediate?: () => void;
};

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

    for (const target of targets) {
      target.immediate?.();
    }

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

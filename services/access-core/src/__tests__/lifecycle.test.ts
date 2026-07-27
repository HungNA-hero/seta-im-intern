import { afterEach, describe, expect, it, vi } from "vitest";
import { registerGracefulShutdown } from "../lifecycle";

describe("registerGracefulShutdown", () => {
  afterEach(() => {
    process.removeAllListeners("SIGTERM");
    process.removeAllListeners("SIGINT");
  });

  it("runs every target's immediate hook synchronously before any close resolves", async () => {
    const order: string[] = [];
    let resolveClose!: () => void;
    const closeGate = new Promise<void>((resolve) => {
      resolveClose = resolve;
    });

    registerGracefulShutdown(
      [
        {
          name: "slow",
          immediate: () => order.push("slow:immediate"),
          close: async () => {
            order.push("slow:close:start");
            await closeGate;
            order.push("slow:close:end");
          },
        },
        {
          name: "fast",
          immediate: () => order.push("fast:immediate"),
          close: () => {
            order.push("fast:close");
          },
        },
      ],
      () => {},
    );

    process.emit("SIGTERM");
    await Promise.resolve();
    await Promise.resolve();

    // Both immediate hooks must have already run — including "fast"'s, which
    // is registered after "slow" — before "slow"'s close is even allowed to
    // finish. This is what guarantees a capacity-log kill switch fires ahead
    // of a slow, blocking close such as server.close()'s request drain.
    expect(order).toEqual([
      "slow:immediate",
      "fast:immediate",
      "slow:close:start",
    ]);

    resolveClose();
    await closeGate;
    await Promise.resolve();
    await Promise.resolve();
    expect(order).toContain("fast:close");
  });

  it("runs shutdown exactly once even if the signal fires twice", async () => {
    const log = vi.fn();
    const close = vi.fn().mockResolvedValue(undefined);
    registerGracefulShutdown([{ name: "x", close }], log);

    process.emit("SIGTERM");
    process.emit("SIGTERM");
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(close).toHaveBeenCalledTimes(1);
  });
});

import { configDefaults, defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    environment: "node",
    include: ["src/**/*.test.ts"],
    exclude: [...configDefaults.exclude, "src/__tests__/**/*.e2e.test.ts"],
    // Live-Redis-gated tests share one Redis instance and call flushdb()
    // in beforeEach; running test files in parallel races those flushes
    // against each other's in-flight assertions.
    fileParallelism: false,
  },
});

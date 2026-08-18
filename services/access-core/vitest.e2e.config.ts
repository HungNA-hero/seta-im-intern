import { configDefaults, defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    environment: "node",
    include: ["src/__tests__/**/*.e2e.test.ts"],
    exclude: configDefaults.exclude,
    // The e2e suites share one Redis, both Postgres databases, and the `E2E:%`
    // fixture prefix, so running their files in parallel races every reset.
    fileParallelism: false,
    testTimeout: 30_000,
    hookTimeout: 30_000,
  },
});

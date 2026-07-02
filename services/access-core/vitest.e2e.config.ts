import { configDefaults, defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    environment: "node",
    include: ["src/__tests__/**/*.e2e.test.ts"],
    exclude: configDefaults.exclude,
    testTimeout: 30_000,
    hookTimeout: 30_000,
  },
});

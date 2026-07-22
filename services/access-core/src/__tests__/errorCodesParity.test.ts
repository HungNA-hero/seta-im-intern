import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, test } from "vitest";
import { errorDefinitions } from "../errors/errorCodes";

const goRegistryPath = resolve(
  process.cwd(),
  "../asset-core/internal/delivery/http/errorcodes.go",
);

describe("error code registry parity", () => {
  test("keeps Access Core and Asset Core error definitions identical", () => {
    const goSource = readFileSync(goRegistryPath, "utf8");
    const goDefinitions = [...goSource.matchAll(
      /"([A-Z_]+)":\s*\{"[A-Z_]+",\s*(\d+),\s*"([^"]+)"\}/g,
    )].map(([, code, number, message]) => ({
      code,
      number: Number(number),
      message,
    }));

    expect(goDefinitions).toEqual(errorDefinitions);
  });
});

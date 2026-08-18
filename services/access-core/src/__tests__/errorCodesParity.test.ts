import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, test } from "vitest";
import { errorDefinitions } from "../errors/errorCodes";

const goRegistryPath = resolve(process.cwd(), "../asset-core/internal/delivery/http/errorcodes.go");

describe("error code registry parity", () => {
  test("keeps Access Core and Asset Core error definitions identical", () => {
    const goSource = readFileSync(goRegistryPath, "utf8");
    const goDefinitions = [...goSource.matchAll(/"([A-Z_]+)":\s*\{"[A-Z_]+",\s*(\d+),\s*"([^"]+)"\}/g)].map(
      ([, code, number, message]) => ({
        code,
        number: Number(number),
        message,
      }),
    );

    expect(goDefinitions).toEqual(errorDefinitions);
  });

  test("defines media codes 6001 through 6015 contiguously on both sides", () => {
    const goSource = readFileSync(goRegistryPath, "utf8");
    const mediaDefinitions = errorDefinitions.filter(
      (definition) => definition.number >= 6001 && definition.number <= 6015,
    );

    expect(mediaDefinitions.map((definition) => definition.number)).toEqual(
      Array.from({ length: 15 }, (_, index) => 6001 + index),
    );

    for (const definition of mediaDefinitions) {
      expect(goSource).toContain(`"${definition.code}"`);
      expect(goSource).toContain(`${definition.number}, "${definition.message}"`);
    }
  });

  test("keeps every error number unique", () => {
    const numbers = errorDefinitions.map((definition) => definition.number);
    expect(new Set(numbers).size).toBe(numbers.length);
  });

  test("keeps notification isolation internal to Asset Core", () => {
    const goSource = readFileSync(goRegistryPath, "utf8");

    expect(goSource).toContain("MEDIA_NOTIFICATION_ISOLATED");
    expect(errorDefinitions.some((definition) => definition.code === "MEDIA_NOTIFICATION_ISOLATED")).toBe(false);
  });
});

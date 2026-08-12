import { describe, expect, test, vi } from "vitest";
import { GraphQLError } from "graphql";
import { createAssetErrorMapper } from "../clients/assetErrorMapper";
import { createAssetResponseDecoder } from "../clients/assetResponseDecoder";

const ASSET_SERVICE = "asset-core";
const TRACE_ID = "0af7651916cd43dd8448eb211c80319c";
const FOLDER_NOT_FOUND = { code: "FOLDER_NOT_FOUND", number: 3001, message: "Folder not found" };

function createMapper() {
  const createDependencyError = vi.fn(() => new GraphQLError("Internal", { extensions: { code: "INTERNAL_ERROR" } }));
  const mapper = createAssetErrorMapper({
    assetServiceName: ASSET_SERVICE,
    isKnownErrorCode: (code): code is string => code === FOLDER_NOT_FOUND.code,
    isTraceId: (value): value is string => value === TRACE_ID,
    getErrorDefinition: () => FOLDER_NOT_FOUND,
    createDependencyError,
  });
  return { mapper, createDependencyError };
}

function envelope(error: Record<string, unknown>) {
  return new Response(JSON.stringify({ error }), { status: 404 });
}

const wellFormed = {
  code: FOLDER_NOT_FOUND.code,
  number: FOLDER_NOT_FOUND.number,
  traceId: TRACE_ID,
  service: ASSET_SERVICE,
};

describe("asset-core error envelope contract", () => {
  test("translates a well-formed envelope into the shared code, number and trace id", async () => {
    const { mapper, createDependencyError } = createMapper();

    await expect(mapper(envelope(wellFormed))).rejects.toMatchObject({
      message: FOLDER_NOT_FOUND.message,
      extensions: {
        code: FOLDER_NOT_FOUND.code,
        number: FOLDER_NOT_FOUND.number,
        traceId: TRACE_ID,
        service: ASSET_SERVICE,
      },
    });
    expect(createDependencyError).not.toHaveBeenCalled();
  });

  test("falls back to a dependency error for an unrecognized code", async () => {
    const { mapper, createDependencyError } = createMapper();

    await expect(mapper(envelope({ ...wellFormed, code: "SOMETHING_NEW" }))).rejects.toMatchObject({
      extensions: expect.objectContaining({ code: "INTERNAL_ERROR" }),
    });
    expect(createDependencyError).toHaveBeenCalled();
  });

  test("refuses an envelope whose number disagrees with the shared table", async () => {
    const { mapper, createDependencyError } = createMapper();

    await expect(mapper(envelope({ ...wellFormed, number: 9999 }))).rejects.toMatchObject({
      extensions: expect.objectContaining({ code: "INTERNAL_ERROR" }),
    });
    expect(createDependencyError).toHaveBeenCalled();
  });

  test("refuses an envelope attributed to another service", async () => {
    const { mapper } = createMapper();

    await expect(mapper(envelope({ ...wellFormed, service: "someone-else" }))).rejects.toMatchObject({
      extensions: expect.objectContaining({ code: "INTERNAL_ERROR" }),
    });
  });

  test("refuses an envelope carrying a malformed trace id", async () => {
    const { mapper } = createMapper();

    await expect(mapper(envelope({ ...wellFormed, traceId: "not-a-trace" }))).rejects.toMatchObject({
      extensions: expect.objectContaining({ code: "INTERNAL_ERROR" }),
    });
  });

  test("falls back to a dependency error when the body is not JSON", async () => {
    const { mapper, createDependencyError } = createMapper();

    await expect(mapper(new Response("<html>gateway timeout</html>", { status: 504 }))).rejects.toMatchObject({
      extensions: expect.objectContaining({ code: "INTERNAL_ERROR" }),
    });
    expect(createDependencyError).toHaveBeenCalled();
  });
});

describe("asset-core response decoder", () => {
  const throwResponseError = vi.fn(async (): Promise<never> => {
    throw new GraphQLError("upstream", { extensions: { code: "INTERNAL_ERROR" } });
  });
  const decoder = createAssetResponseDecoder(throwResponseError);

  function ok(body: unknown) {
    return new Response(JSON.stringify(body), { status: 200 });
  }

  test("unwraps the named envelope key through the mapper", async () => {
    await expect(decoder.unwrap(ok({ item: { id: "a" } }), "item", (raw) => raw.id, "failed")).resolves.toBe("a");
  });

  test("rejects a response missing the expected key", async () => {
    await expect(decoder.unwrap(ok({ other: 1 }), "item", (raw) => raw, "failed")).rejects.toMatchObject({
      message: "failed: unexpected response format",
      extensions: expect.objectContaining({ code: "INTERNAL_ERROR" }),
    });
  });

  test("rejects a list envelope whose value is not an array", async () => {
    await expect(decoder.unwrapList(ok({ items: "nope" }), "items", (raw) => raw, "failed")).rejects.toMatchObject({
      message: "failed: unexpected response format",
    });
  });

  test("accepts an empty list, which is absent data rather than a malformed envelope", async () => {
    await expect(decoder.unwrapList(ok({ items: [] }), "items", (raw) => raw, "failed")).resolves.toEqual([]);
  });

  test("accepts 204 as success and refuses any other status", async () => {
    await expect(decoder.assertNoContent(new Response(null, { status: 204 }), "failed")).resolves.toBe(true);
    await expect(decoder.assertNoContent(new Response(null, { status: 200 }), "failed")).rejects.toThrow();
  });
});

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, test } from "vitest";
import { parse } from "yaml";
import { errorDefinitions } from "../errors/errorCodes";

const contractPath = resolve(process.cwd(), "../../specs/006-media-upload-processing/contracts/media-api.openapi.yaml");

type JsonRecord = Record<string, unknown>;

const document = parse(readFileSync(contractPath, "utf8")) as JsonRecord;

/**
 * Resolves a local `#/...` reference. Remote references are rejected rather
 * than fetched: a contract test that reaches the network stops being a contract
 * test.
 */
function resolveRef(ref: string): JsonRecord {
  if (!ref.startsWith("#/")) {
    throw new Error(`only local references are allowed, found: ${ref}`);
  }
  let node: unknown = document;
  for (const segment of ref.slice(2).split("/")) {
    if (typeof node !== "object" || node === null) {
      throw new Error(`unresolvable reference: ${ref}`);
    }
    node = (node as JsonRecord)[segment];
  }
  if (typeof node !== "object" || node === null) {
    throw new Error(`unresolvable reference: ${ref}`);
  }
  return node as JsonRecord;
}

function schema(name: string): JsonRecord {
  return resolveRef(`#/components/schemas/${name}`);
}

function properties(name: string): JsonRecord {
  return (schema(name).properties ?? {}) as JsonRecord;
}

function collectRefs(node: unknown, found: string[] = []): string[] {
  if (Array.isArray(node)) {
    for (const item of node) collectRefs(item, found);
    return found;
  }
  if (typeof node === "object" && node !== null) {
    for (const [key, value] of Object.entries(node)) {
      if (key === "$ref" && typeof value === "string") {
        found.push(value);
      } else {
        collectRefs(value, found);
      }
    }
  }
  return found;
}

describe("media OpenAPI contract", () => {
  test("is an OpenAPI 3.1 document", () => {
    expect(document.openapi).toBe("3.1.0");
  });

  test("resolves every local reference and uses no remote ones", () => {
    const refs = collectRefs(document);
    expect(refs.length).toBeGreaterThan(0);
    for (const ref of refs) {
      expect(ref.startsWith("#/")).toBe(true);
      expect(() => resolveRef(ref)).not.toThrow();
    }
  });

  test("declares every media route Access Core owns", () => {
    const paths = Object.keys((document.paths ?? {}) as JsonRecord);
    expect(paths).toEqual(
      expect.arrayContaining([
        "/api/v1/assets/{assetId}/media/uploads",
        "/api/v1/assets/{assetId}/media/uploads/{uploadId}",
        "/api/v1/assets/{assetId}/media/uploads/{uploadId}/refresh",
        "/api/v1/assets/{assetId}/media",
      ]),
    );
    expect(paths).not.toContain("/api/v1/assets/{assetId}/media/status");
  });

  test("supports cancellation on the session route", () => {
    const sessionRoute = ((document.paths ?? {}) as JsonRecord)[
      "/api/v1/assets/{assetId}/media/uploads/{uploadId}"
    ] as JsonRecord;

    expect(Object.keys(sessionRoute)).toEqual(expect.arrayContaining(["get", "delete"]));
  });

  test("requires a client-declared SHA-256 at session creation", () => {
    const request = schema("CreateUploadSessionRequest");
    expect(request.required).toEqual(
      expect.arrayContaining(["filename", "contentType", "sizeBytes", "checksumSha256"]),
    );
    expect(request.additionalProperties).toBe(false);
  });

  test("rejects transfer capability negotiation fields", () => {
    const requestProperties = Object.keys(properties("CreateUploadSessionRequest"));
    for (const obsolete of ["strategy", "capabilities", "transferStrategy", "parts", "partManifest"]) {
      expect(requestProperties).not.toContain(obsolete);
    }
  });

  test("exposes exactly one presigned upload descriptor with signed headers", () => {
    const descriptor = schema("PresignedUploadDescriptor");
    expect(descriptor.required).toEqual(["protocol", "method", "url", "headers", "credentialExpiresAt"]);
    expect(descriptor.additionalProperties).toBe(false);

    const descriptorProperties = properties("PresignedUploadDescriptor");
    expect((descriptorProperties.method as JsonRecord).const).toBe("PUT");
    expect((descriptorProperties.protocol as JsonRecord).const).toBe("HTTP");
  });

  test("commits with the upload identity alone", () => {
    const request = schema("CommitUploadRequest");
    expect(request.required).toEqual(["uploadId"]);
    expect(Object.keys(properties("CommitUploadRequest"))).toEqual(["uploadId"]);
    expect(request.additionalProperties).toBe(false);
  });

  test("describes durable acceptance without an unavailable status URL or internal replay marker", () => {
    expect(schema("AcceptedJob").required).toEqual([
      "assetId",
      "uploadId",
      "jobId",
      "status",
      "original",
      "acceptedAt",
    ]);
    expect(Object.keys(properties("AcceptedJob"))).toEqual([
      "assetId",
      "uploadId",
      "jobId",
      "status",
      "original",
      "acceptedAt",
    ]);
  });

  test("bounds the two outputs to their documented boxes", () => {
    const thumbnail = properties("ThumbnailMediaOutput");
    expect((thumbnail.width as JsonRecord).maximum).toBe(256);
    expect((thumbnail.height as JsonRecord).maximum).toBe(256);

    const web = properties("WebMediaOutput");
    expect((web.width as JsonRecord).maximum).toBe(1080);
    expect((web.height as JsonRecord).maximum).toBe(1080);

    expect(schema("MediaOutputs").required).toEqual(["thumbnail", "web", "expiresAt"]);
  });

  test("never exposes a raw object key or storage URL", () => {
    const serialized = JSON.stringify(document);
    for (const leaked of ["rawObjectKey", "raw_object_key", "rawUrl", "originalUrl"]) {
      expect(serialized).not.toContain(leaked);
    }
  });

  test("bounds the reported attempt count to the three permitted attempts", () => {
    const attemptCount = properties("MediaStatus").attemptCount as JsonRecord;
    expect(attemptCount.minimum).toBe(0);
    expect(attemptCount.maximum).toBe(3);
  });

  test("publishes only status values Access Core can map", () => {
    const statusProperties = properties("MediaStatus");
    expect((statusProperties.status as JsonRecord).enum).toEqual(["QUEUED", "PROCESSING", "COMPLETED", "FAILED"]);
  });

  test("every documented media error code exists in the shared registry", () => {
    const serialized = JSON.stringify(document);
    const referenced = [
      ...serialized.matchAll(
        /"(MEDIA_[A-Z_]+|UPLOAD_SESSION_EXPIRED|INVALID_IMAGE|IDEMPOTENCY_KEY_REUSED|IMAGE_DIMENSIONS_EXCEEDED|PROCESSING_TIMEOUT)"/g,
      ),
    ].map(([, code]) => code);
    const known = new Set(errorDefinitions.map((definition) => definition.code));

    for (const code of new Set(referenced)) {
      expect(known.has(code)).toBe(true);
    }
  });
});

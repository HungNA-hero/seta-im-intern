import { describe, expect, test } from "vitest";
import {
  MAX_UPLOAD_SIZE_BYTES,
  MEDIA_CONTENT_TYPES,
  MIN_UPLOAD_SIZE_BYTES,
  isAdmittedContentType,
  isAdmittedSize,
  isValidBase64Sha256,
  toMediaStatus,
  toPresignedUploadDescriptor,
  toProcessingStage,
  toProcessingStatus,
  toUploadSession,
  toUploadSessionState,
} from "../domain/media";
import { errorDefinitions } from "../errors/errorCodes";
import type { GoMediaStatus, GoUploadSession } from "../domain/media";

const goSession: GoUploadSession = {
  upload_id: "11111111-1111-1111-1111-111111111111",
  asset_id: "22222222-2222-2222-2222-222222222222",
  state: "created",
  session_expires_at: "2026-08-15T00:00:00Z",
  created_at: "2026-08-14T00:00:00Z",
  upload: {
    protocol: "HTTP",
    method: "PUT",
    url: "https://storage.example/seta-media/raw/22222222/object?X-Amz-Signature=abc",
    headers: { "x-amz-checksum-sha256": "a".repeat(43) + "=" },
    credential_expires_at: "2026-08-14T01:00:00Z",
  },
};

describe("media public contract", () => {
  test("publishes exactly one presigned descriptor and never rewrites what was signed", () => {
    const descriptor = toPresignedUploadDescriptor(goSession.upload);

    expect(descriptor).toEqual({
      protocol: "HTTP",
      method: "PUT",
      url: goSession.upload?.url,
      headers: goSession.upload?.headers,
      credentialExpiresAt: goSession.upload?.credential_expires_at,
    });
  });

  test("omits the descriptor once a session leaves the created state", () => {
    expect(toPresignedUploadDescriptor(null)).toBeNull();
    expect(toPresignedUploadDescriptor(undefined)).toBeNull();
  });

  test("exposes no raw object key or storage identity on a session", () => {
    const session = toUploadSession(goSession, "/api/v1/assets/x/media");
    const serialized = JSON.stringify(session);

    for (const leaked of ["rawObjectKey", "raw_object_key", "rawUrl", "originalUrl", "bucket"]) {
      expect(serialized).not.toContain(leaked);
    }
    expect(Object.keys(session).sort()).toEqual(
      ["assetId", "commitUrl", "createdAt", "sessionExpiresAt", "state", "upload", "uploadId"].sort(),
    );
  });

  test("admits only the documented session states", () => {
    expect(toUploadSessionState("created")).toBe("CREATED");
    expect(toUploadSessionState("committed")).toBe("COMMITTED");
    expect(() => toUploadSessionState("cancelled")).toThrow(/unrecognized upload session state/);
  });

  test("admits only the four documented processing statuses", () => {
    for (const status of ["queued", "processing", "completed", "failed"]) {
      expect(toProcessingStatus(status)).toBe(status.toUpperCase());
    }
    expect(() => toProcessingStatus("PENDING")).toThrow(/unrecognized media processing status/);
  });

  test("admits only the two documented processing stages, and treats absence as null", () => {
    expect(toProcessingStage("validating")).toBe("VALIDATING");
    expect(toProcessingStage("transforming")).toBe("TRANSFORMING");
    expect(toProcessingStage(null)).toBeNull();
    expect(toProcessingStage("")).toBeNull();
    expect(() => toProcessingStage("uploading")).toThrow(/unrecognized media processing stage/);
  });

  test("bounds declared uploads to the documented size window", () => {
    expect(MIN_UPLOAD_SIZE_BYTES).toBe(1);
    expect(MAX_UPLOAD_SIZE_BYTES).toBe(50_000_000);

    expect(isAdmittedSize(MIN_UPLOAD_SIZE_BYTES)).toBe(true);
    expect(isAdmittedSize(MAX_UPLOAD_SIZE_BYTES)).toBe(true);
    expect(isAdmittedSize(0)).toBe(false);
    expect(isAdmittedSize(MAX_UPLOAD_SIZE_BYTES + 1)).toBe(false);
    expect(isAdmittedSize(1.5)).toBe(false);
  });

  test("admits only JPEG and PNG", () => {
    expect(MEDIA_CONTENT_TYPES).toEqual(["image/jpeg", "image/png"]);
    expect(isAdmittedContentType("image/gif")).toBe(false);
    expect(isAdmittedContentType("image/webp")).toBe(false);
  });

  test("requires a strictly formed base64 SHA-256 declaration", () => {
    expect(isValidBase64Sha256("a".repeat(43) + "=")).toBe(true);
    expect(isValidBase64Sha256("a".repeat(43))).toBe(false);
    expect(isValidBase64Sha256("a".repeat(64))).toBe(false);
    expect(isValidBase64Sha256("not base64!")).toBe(false);
    expect(isValidBase64Sha256(undefined)).toBe(false);
  });

  test("maps a failure to a safe code that exists in the shared registry", () => {
    const failed: GoMediaStatus = {
      asset_id: goSession.asset_id,
      upload_id: goSession.upload_id,
      job_id: "33333333-3333-3333-3333-333333333333",
      status: "failed",
      stage: null,
      attempt_count: 3,
      original: {
        filename: "small-64x64.png",
        declared_content_type: "image/png",
        size_bytes: 1024,
      },
      error: { code: "MEDIA_PROCESSING_FAILED", message: "Media processing failed" },
      outputs: null,
      accepted_at: "2026-08-14T00:00:00Z",
      completed_at: null,
    };

    const status = toMediaStatus(failed);
    const known = new Set(errorDefinitions.map((definition) => definition.code));

    expect(status.status).toBe("FAILED");
    expect(status.error).not.toBeNull();
    expect(known.has(status.error?.code ?? "")).toBe(true);
  });

  test("registers every media error code the routes can return", () => {
    const known = new Set(errorDefinitions.map((definition) => definition.code));

    for (const code of [
      "MEDIA_UPLOAD_NOT_FOUND",
      "MEDIA_UPLOAD_IN_PROGRESS",
      "IDEMPOTENCY_KEY_REUSED",
      "UPLOAD_SESSION_EXPIRED",
      "MEDIA_PAYLOAD_TOO_LARGE",
      "MEDIA_TYPE_UNSUPPORTED",
      "MEDIA_OBJECT_MISMATCH",
      "MEDIA_QUOTA_EXCEEDED",
      "MEDIA_RATE_LIMITED",
      "MEDIA_UPLOAD_STATE_CONFLICT",
    ]) {
      expect(known.has(code)).toBe(true);
    }
  });
});

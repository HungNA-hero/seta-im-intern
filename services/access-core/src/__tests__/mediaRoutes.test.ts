import Fastify from "fastify";
import { describe, expect, test, vi } from "vitest";
import type { UploadSession } from "../domain/media";
import { registerMediaRoutes } from "../rest/mediaRoutes";

const assetId = "33333333-3333-4333-8333-333333333333";
const uploadId = "44444444-4444-4444-8444-444444444444";
const retryKey = "55555555-5555-4555-8555-555555555555";
const memberContext = {
  userId: "11111111-1111-4111-8111-111111111111",
  currentOrgId: "22222222-2222-4222-8222-222222222222",
  isMember: true,
  roles: ["member"],
  olpEnabled: false,
};

function session(): UploadSession {
  return {
    uploadId,
    assetId,
    state: "CREATED",
    upload: {
      protocol: "HTTP",
      method: "PUT",
      url: "https://uploads.example.test/media/raw/exact.png?X-Amz-Signature=opaque",
      headers: {
        "Content-Type": "image/png",
        "If-None-Match": "*",
        "X-Amz-Checksum-Sha256": "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      },
      credentialExpiresAt: "2026-08-13T11:00:00Z",
    },
    sessionExpiresAt: "2026-08-14T10:00:00Z",
    commitUrl: `/api/v1/assets/${assetId}/media`,
  };
}

async function buildHarness() {
  const app = Fastify();
  const usecase = {
    createSession: vi.fn().mockResolvedValue({ ...session(), replayed: false }),
    getSession: vi.fn().mockResolvedValue(session()),
    refreshSession: vi.fn().mockResolvedValue(session()),
    cancelSession: vi.fn().mockResolvedValue(undefined),
    commit: vi.fn().mockResolvedValue({
      assetId,
      uploadId,
      jobId: "66666666-6666-4666-8666-666666666666",
      status: "QUEUED",
      original: { filename: "photo.png", contentType: "image/png", sizeBytes: 7 },
      acceptedAt: "2026-08-13T10:01:00Z",
      replayed: false,
    }),
    getStatus: vi.fn().mockResolvedValue({
      assetId,
      uploadId,
      jobId: "66666666-6666-4666-8666-666666666666",
      status: "QUEUED",
      attemptCount: 0,
      stage: null,
      original: {
        filename: "photo.png",
        declaredContentType: "image/png",
        detectedContentType: null,
        sizeBytes: 7,
        sha256: null,
      },
      outputs: null,
      error: null,
      acceptedAt: "2026-08-13T10:01:00Z",
      startedAt: null,
      completedAt: null,
      failedAt: null,
    }),
  };
  registerMediaRoutes(app, {
    loadContext: vi.fn().mockResolvedValue(memberContext),
    usecase,
  });
  await app.ready();
  return { app, usecase };
}

describe("media REST routes", () => {
  test("creates one presigned session and passes the signed descriptor through unchanged", async () => {
    const { app, usecase } = await buildHarness();
    const response = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: {
        "x-user-id": memberContext.userId,
        "x-org-id": memberContext.currentOrgId,
        "idempotency-key": retryKey,
      },
      payload: {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      },
    });

    expect(response.statusCode).toBe(201);
    expect(response.headers.location).toBe(`/api/v1/assets/${assetId}/media/uploads/${uploadId}`);
    expect(usecase.createSession).toHaveBeenCalledWith(memberContext, assetId, {
      idempotencyKey: retryKey,
      filename: "photo.png",
      contentType: "image/png",
      sizeBytes: 7,
      checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
    });
    expect(response.json().data.upload).toEqual(session().upload);
    expect(response.json().data).not.toHaveProperty("replayed");
  });

  test("a replayed retry answers 200 with the replay header instead of a second creation", async () => {
    const { app, usecase } = await buildHarness();
    usecase.createSession.mockResolvedValue({ ...session(), replayed: true });

    const response = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: {
        "x-user-id": memberContext.userId,
        "x-org-id": memberContext.currentOrgId,
        "idempotency-key": retryKey,
      },
      payload: {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      },
    });

    expect(response.statusCode).toBe(200);
    expect(response.headers["idempotency-replayed"]).toBe("true");
    expect(response.headers.location).toBeUndefined();
    expect(response.json().data.uploadId).toBe(uploadId);
  });

  test("rejects a file body field instead of proxying bytes", async () => {
    const { app, usecase } = await buildHarness();
    const response = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: {
        "x-user-id": memberContext.userId,
        "x-org-id": memberContext.currentOrgId,
        "idempotency-key": retryKey,
      },
      payload: {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
        file: "base64-body-must-not-pass",
      },
    });

    expect(response.statusCode).toBe(400);
    expect(usecase.createSession).not.toHaveBeenCalled();
  });

  test("commit bypasses creation rate limiting and returns durable 202 metadata", async () => {
    const { app, usecase } = await buildHarness();
    const response = await app.inject({
      method: "PUT",
      url: `/api/v1/assets/${assetId}/media`,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
      payload: { uploadId },
    });

    expect(response.statusCode).toBe(202);
    expect(response.headers.location).toBeUndefined();
    expect(response.headers["retry-after"]).toBeUndefined();
    expect(usecase.commit).toHaveBeenCalledWith(memberContext, assetId, uploadId);
    expect(response.json().data).toEqual({
      assetId,
      uploadId,
      jobId: "66666666-6666-4666-8666-666666666666",
      status: "QUEUED",
      original: { filename: "photo.png", contentType: "image/png", sizeBytes: 7 },
      acceptedAt: "2026-08-13T10:01:00Z",
    });
    expect(response.json().data).not.toHaveProperty("replayed");
  });

  test("a repeated commit returns the stable acceptance with the replay header", async () => {
    const { app, usecase } = await buildHarness();
    usecase.commit.mockResolvedValue({
      assetId,
      uploadId,
      jobId: "66666666-6666-4666-8666-666666666666",
      status: "QUEUED",
      original: { filename: "photo.png", contentType: "image/png", sizeBytes: 7 },
      acceptedAt: "2026-08-13T10:01:00Z",
      replayed: true,
    });

    const response = await app.inject({
      method: "PUT",
      url: `/api/v1/assets/${assetId}/media`,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
      payload: { uploadId },
    });

    expect(response.statusCode).toBe(200);
    expect(response.headers["idempotency-replayed"]).toBe("true");
    expect(response.json()).toEqual({
      data: {
        assetId,
        uploadId,
        jobId: "66666666-6666-4666-8666-666666666666",
        status: "QUEUED",
        original: { filename: "photo.png", contentType: "image/png", sizeBytes: 7 },
        acceptedAt: "2026-08-13T10:01:00Z",
      },
    });
  });

  test.each([
    [
      "missing idempotency key",
      `/api/v1/assets/${assetId}/media/uploads`,
      {},
      {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      },
    ],
    [
      "malformed asset id",
      "/api/v1/assets/not-a-uuid/media/uploads",
      { "idempotency-key": retryKey },
      {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      },
    ],
    [
      "malformed checksum",
      `/api/v1/assets/${assetId}/media/uploads`,
      { "idempotency-key": retryKey },
      {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "not-a-sha256",
      },
    ],
  ])("maps Fastify validation for %s to the safe media envelope", async (_case, url, extraHeaders, payload) => {
    const { app, usecase } = await buildHarness();
    const response = await app.inject({
      method: "POST",
      url,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId, ...extraHeaders },
      payload,
    });

    expect(response.statusCode).toBe(400);
    expect(response.json()).toEqual({
      error: {
        code: "BAD_REQUEST",
        number: 1001,
        message: expect.any(String),
        service: "access-core",
      },
    });
    expect(response.json()).not.toHaveProperty("statusCode");
    expect(usecase.createSession).not.toHaveBeenCalled();
  });

  test("returns the authoritative status with explicit nullable fields", async () => {
    const { app, usecase } = await buildHarness();
    const response = await app.inject({
      method: "GET",
      url: `/api/v1/assets/${assetId}/media/status`,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
    });

    expect(response.statusCode).toBe(200);
    expect(usecase.getStatus).toHaveBeenCalledWith(memberContext, assetId);
    expect(response.json()).toEqual({
      data: {
        assetId,
        uploadId,
        jobId: "66666666-6666-4666-8666-666666666666",
        status: "QUEUED",
        attemptCount: 0,
        stage: null,
        original: {
          filename: "photo.png",
          declaredContentType: "image/png",
          detectedContentType: null,
          sizeBytes: 7,
          sha256: null,
        },
        outputs: null,
        error: null,
        acceptedAt: "2026-08-13T10:01:00Z",
        startedAt: null,
        completedAt: null,
        failedAt: null,
      },
    });
  });

  test.each([
    ["validating", "PROCESSING", "VALIDATING"],
    ["transforming", "PROCESSING", "TRANSFORMING"],
  ])("returns %s processing stage", async (_name, status, stage) => {
    const { app, usecase } = await buildHarness();
    usecase.getStatus.mockResolvedValue({
      ...(await usecase.getStatus()),
      status,
      attemptCount: stage === "VALIDATING" ? 1 : 2,
      stage,
      startedAt: "2026-08-13T10:01:05Z",
    });

    const response = await app.inject({
      method: "GET",
      url: `/api/v1/assets/${assetId}/media/status`,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
    });

    expect(response.statusCode).toBe(200);
    expect(response.json().data).toMatchObject({ status, stage });
  });

  test("returns completed derivative metadata and safe failed status without internal detail", async () => {
    const { app, usecase } = await buildHarness();
    const base = await usecase.getStatus();
    usecase.getStatus.mockResolvedValueOnce({
      ...base,
      status: "COMPLETED",
      attemptCount: 2,
      outputs: {
        thumbnail: {
          url: "https://media.example.test/thumbnail?signature=opaque",
          width: 256,
          height: 128,
          sizeBytes: 900,
          contentType: "image/png",
        },
        web: {
          url: "https://media.example.test/web?signature=opaque",
          width: 1080,
          height: 540,
          sizeBytes: 1800,
          contentType: "image/png",
        },
        expiresAt: "2026-08-13T10:16:00Z",
      },
      completedAt: "2026-08-13T10:01:20Z",
    });
    let response = await app.inject({
      method: "GET",
      url: `/api/v1/assets/${assetId}/media/status`,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
    });
    expect(response.headers["content-type"]).toMatch(/^application\/json/);
    expect(response.headers["x-content-type-options"]).toBe("nosniff");
    expect(response.json().data).toMatchObject({
      status: "COMPLETED",
      outputs: { thumbnail: { sizeBytes: 900 }, web: { sizeBytes: 1800 } },
      error: null,
    });

    usecase.getStatus.mockResolvedValueOnce({
      ...base,
      status: "FAILED",
      attemptCount: 3,
      outputs: null,
      error: { code: "MEDIA_PROCESSING_FAILED", message: "Media processing failed" },
      failedAt: "2026-08-13T10:01:20Z",
    });
    response = await app.inject({
      method: "GET",
      url: `/api/v1/assets/${assetId}/media/status`,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
    });
    expect(response.json().data).toMatchObject({
      status: "FAILED",
      outputs: null,
      error: { code: "MEDIA_PROCESSING_FAILED", message: "Media processing failed" },
    });
    expect(JSON.stringify(response.json())).not.toContain("parser");
  });

  test("maps cross-tenant status denial to scoped not-found", async () => {
    const { app, usecase } = await buildHarness();
    usecase.getStatus.mockRejectedValue(
      Object.assign(new Error("not in scope"), { extensions: { code: "MEDIA_UPLOAD_NOT_FOUND", number: 6001 } }),
    );

    const response = await app.inject({
      method: "GET",
      url: `/api/v1/assets/${assetId}/media/status`,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
    });

    expect(response.statusCode).toBe(404);
    expect(response.json()).toMatchObject({ error: { code: "MEDIA_UPLOAD_NOT_FOUND", number: 6001 } });
  });

  test("maps quota rejection before exposing an upload descriptor", async () => {
    const { app, usecase } = await buildHarness();
    usecase.createSession.mockRejectedValue(
      Object.assign(new Error("Organization media quota exceeded"), {
        extensions: { code: "MEDIA_QUOTA_EXCEEDED", number: 6013 },
      }),
    );
    const response = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: {
        "x-user-id": memberContext.userId,
        "x-org-id": memberContext.currentOrgId,
        "idempotency-key": retryKey,
      },
      payload: {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 50_000_000,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      },
    });

    expect(response.statusCode).toBe(413);
    expect(response.json()).toMatchObject({ error: { code: "MEDIA_QUOTA_EXCEEDED", number: 6013 } });
    expect(response.json()).not.toHaveProperty("data.upload");
  });

  test("a rate-limited creation answers 429 with the whole-second Retry-After the limiter asked for", async () => {
    const { app, usecase } = await buildHarness();
    usecase.createSession.mockRejectedValue(
      Object.assign(new Error("Too many upload sessions"), {
        extensions: { code: "MEDIA_RATE_LIMITED", number: 6014, retryAfterSeconds: 30 },
      }),
    );
    const response = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: {
        "x-user-id": memberContext.userId,
        "x-org-id": memberContext.currentOrgId,
        "idempotency-key": retryKey,
      },
      payload: {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      },
    });

    expect(response.statusCode).toBe(429);
    expect(response.headers["retry-after"]).toBe("30");
    expect(response.json()).toMatchObject({ error: { code: "MEDIA_RATE_LIMITED", number: 6014 } });
    expect(response.json()).not.toHaveProperty("data");
  });

  test.each([1, 50_000_000])("uses the same single PRESIGNED descriptor at the %i-byte boundary", async (sizeBytes) => {
    const { app, usecase } = await buildHarness();
    const response = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: {
        "x-user-id": memberContext.userId,
        "x-org-id": memberContext.currentOrgId,
        "idempotency-key": retryKey,
      },
      payload: {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      },
    });

    expect(response.statusCode).toBe(201);
    expect(usecase.createSession).toHaveBeenCalledWith(memberContext, assetId, expect.objectContaining({ sizeBytes }));
    expect(response.json().data.upload).toEqual(session().upload);
    expect(response.json().data).not.toHaveProperty("strategy");
    expect(response.json().data).not.toHaveProperty("multipart");
  });

  test.each([
    ["capabilities", { multipart: true }],
    ["strategy", "MULTIPART"],
    ["manifest", [{ part: 1 }]],
    ["file", "body-must-bypass-services"],
  ])("rejects obsolete create field %s instead of negotiating it", async (field, value) => {
    const { app, usecase } = await buildHarness();
    const response = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: {
        "x-user-id": memberContext.userId,
        "x-org-id": memberContext.currentOrgId,
        "idempotency-key": retryKey,
      },
      payload: {
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
        [field]: value,
      },
    });

    expect(response.statusCode).toBe(400);
    expect(usecase.createSession).not.toHaveBeenCalled();
  });

  test("refresh returns the opaque PRESIGNED descriptor without consuming creation limits", async () => {
    const { app, usecase } = await buildHarness();
    const response = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads/${uploadId}/refresh`,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
    });

    expect(response.statusCode).toBe(200);
    expect(response.json().data.upload).toEqual(session().upload);
    expect(usecase.refreshSession).toHaveBeenCalledWith(memberContext, assetId, uploadId);
  });

  test.each(["capabilities", "strategy", "manifest"])(
    "rejects obsolete refresh field %s rather than negotiating a transfer",
    async (field) => {
      const { app, usecase } = await buildHarness();
      const response = await app.inject({
        method: "POST",
        url: `/api/v1/assets/${assetId}/media/uploads/${uploadId}/refresh`,
        headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
        payload: { [field]: field === "strategy" ? "MULTIPART" : {} },
      });

      expect(response.statusCode).toBe(400);
      expect(usecase.refreshSession).not.toHaveBeenCalled();
    },
  );

  test.each(["strategy", "manifest", "checksumSha256"])(
    "commit accepts uploadId only and rejects %s",
    async (field) => {
      const { app, usecase } = await buildHarness();
      const response = await app.inject({
        method: "PUT",
        url: `/api/v1/assets/${assetId}/media`,
        headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
        payload: { uploadId, [field]: field === "strategy" ? "MULTIPART" : {} },
      });

      expect(response.statusCode).toBe(400);
      expect(usecase.commit).not.toHaveBeenCalled();
    },
  );

  test("DELETE cancels without creation rate limiting and returns 204", async () => {
    const { app, usecase } = await buildHarness();
    const response = await app.inject({
      method: "DELETE",
      url: `/api/v1/assets/${assetId}/media/uploads/${uploadId}`,
      headers: { "x-user-id": memberContext.userId, "x-org-id": memberContext.currentOrgId },
    });

    expect(response.statusCode).toBe(204);
    expect(response.body).toBe("");
    expect(usecase.cancelSession).toHaveBeenCalledWith(memberContext, assetId, uploadId);
  });
});

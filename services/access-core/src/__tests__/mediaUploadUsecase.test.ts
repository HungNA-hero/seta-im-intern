import { describe, expect, test, vi } from "vitest";
import { createAuthorizedAssetFetch } from "../usecase/authorizedAssetFetch";
import { createMediaUploadUsecase } from "../usecase/mediaUploadUsecase";

const memberContext = {
  userId: "11111111-1111-4111-8111-111111111111",
  currentOrgId: "22222222-2222-4222-8222-222222222222",
  isMember: true,
  roles: ["member"],
  olpEnabled: false,
};

const assetId = "33333333-3333-4333-8333-333333333333";
const uploadId = "44444444-4444-4444-8444-444444444444";
const retryKey = "55555555-5555-4555-8555-555555555555";

function sessionResponse(url = "https://uploads.example.test/media/raw/exact.png?X-Amz-Signature=opaque") {
  return new Response(
    JSON.stringify({
      data: {
        upload_id: uploadId,
        asset_id: assetId,
        state: "created",
        upload: {
          protocol: "HTTP",
          method: "PUT",
          url,
          headers: {
            "Content-Type": "image/png",
            "Content-Length": "7",
            "If-None-Match": "*",
            "X-Amz-Checksum-Sha256": "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
          },
          credential_expires_at: "2026-08-13T11:00:00Z",
        },
        session_expires_at: "2026-08-14T10:00:00Z",
        created_at: "2026-08-13T10:00:00Z",
      },
    }),
    { status: 201, headers: { "Idempotency-Replayed": "false" } },
  );
}

function queuedStatusResponse(overrides: Record<string, unknown> = {}) {
  return new Response(
    JSON.stringify({
      data: {
        asset_id: assetId,
        upload_id: uploadId,
        job_id: "66666666-6666-4666-8666-666666666666",
        status: "queued",
        attempt_count: 0,
        stage: null,
        original: {
          filename: "photo.png",
          declared_content_type: "image/png",
          detected_content_type: null,
          size_bytes: 7,
          sha256: null,
        },
        outputs: null,
        error: null,
        accepted_at: "2026-08-19T10:59:00Z",
        started_at: null,
        completed_at: null,
        failed_at: null,
        ...overrides,
      },
    }),
    { status: 200 },
  );
}

function createHarness(response: Response, requireHTTPS = false) {
  const order: string[] = [];
  const assertAllowed = vi.fn(async () => {
    order.push("authorize");
  });
  const request = vi.fn(async () => {
    order.push("delegate");
    return response;
  });
  const rateLimiter = {
    consumeSessionCreation: vi.fn(async () => {
      order.push("rate-limit");
    }),
  };
  const authorizedAsset = createAuthorizedAssetFetch({
    authorization: { assertAllowed },
    transport: { request },
  });
  return {
    order,
    assertAllowed,
    request,
    rateLimiter,
    usecase: createMediaUploadUsecase({ authorizedAsset, rateLimiter, requireHTTPS }),
  };
}

describe("media upload usecase", () => {
  test("rejects an insecure upload descriptor when HTTPS is required", async () => {
    const harness = createHarness(
      sessionResponse("http://uploads.example.test/media/raw/exact.png?X-Amz-Signature=opaque"),
      true,
    );

    await expect(
      harness.usecase.createSession(memberContext, assetId, {
        idempotencyKey: retryKey,
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      }),
    ).rejects.toMatchObject({ extensions: { code: "INTERNAL_ERROR" } });
  });

  test("rejects upload descriptors that expose credentials or uncontracted headers", async () => {
    const response = sessionResponse();
    const payload = JSON.parse(await response.text());
    payload.data.upload.headers.Authorization = "Bearer must-not-reach-the-client";
    const harness = createHarness(new Response(JSON.stringify(payload), { status: 201 }));

    await expect(
      harness.usecase.createSession(memberContext, assetId, {
        idempotencyKey: retryKey,
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      }),
    ).rejects.toMatchObject({ extensions: { code: "INTERNAL_ERROR" } });
  });

  test("rejects insecure rendition descriptors when HTTPS is required", async () => {
    const response = new Response(
      JSON.stringify({
        data: {
          asset_id: assetId,
          upload_id: uploadId,
          job_id: "66666666-6666-4666-8666-666666666666",
          status: "completed",
          attempt_count: 1,
          stage: null,
          original: {
            filename: "photo.png",
            declared_content_type: "image/png",
            detected_content_type: "image/png",
            size_bytes: 2048,
            sha256: "2a".repeat(32),
          },
          outputs: {
            thumbnail: {
              url: "http://media.example.test/thumbnail?signature=opaque",
              width: 256,
              height: 128,
              size_bytes: 900,
              content_type: "image/png",
            },
            web: {
              url: "https://media.example.test/web?signature=opaque",
              width: 1080,
              height: 540,
              size_bytes: 1800,
              content_type: "image/png",
            },
            expires_at: "2026-08-19T11:15:00Z",
          },
          error: null,
          accepted_at: "2026-08-19T10:59:00Z",
          started_at: "2026-08-19T10:59:30Z",
          completed_at: "2026-08-19T10:59:55Z",
          failed_at: null,
        },
      }),
      { status: 200 },
    );
    const harness = createHarness(response, true);

    await expect(harness.usecase.getStatus(memberContext, assetId)).rejects.toMatchObject({
      extensions: { code: "INTERNAL_ERROR" },
    });
  });

  test("status polling performs a fresh write authorization and maps trusted output metadata", async () => {
    const response = new Response(
      JSON.stringify({
        data: {
          asset_id: assetId,
          upload_id: uploadId,
          job_id: "66666666-6666-4666-8666-666666666666",
          status: "completed",
          attempt_count: 2,
          stage: null,
          original: {
            filename: "photo.png",
            declared_content_type: "image/png",
            detected_content_type: "image/png",
            size_bytes: 2048,
            sha256: "2a".repeat(32),
          },
          outputs: {
            thumbnail: {
              url: "https://media.example.test/thumbnail?signature=opaque",
              width: 256,
              height: 128,
              size_bytes: 900,
              content_type: "image/png",
            },
            web: {
              url: "https://media.example.test/web?signature=opaque",
              width: 1080,
              height: 540,
              size_bytes: 1800,
              content_type: "image/png",
            },
            expires_at: "2026-08-19T11:15:00Z",
          },
          error: null,
          accepted_at: "2026-08-19T10:59:00Z",
          started_at: "2026-08-19T10:59:30Z",
          completed_at: "2026-08-19T10:59:55Z",
          failed_at: null,
        },
      }),
      { status: 200 },
    );
    const harness = createHarness(response);

    const status = await harness.usecase.getStatus(memberContext, assetId);

    expect(harness.order).toEqual(["authorize", "delegate"]);
    expect(harness.assertAllowed).toHaveBeenCalledWith({
      userId: memberContext.userId,
      action: "write",
      resourceType: "metadata_item",
      resourceId: assetId,
      orgId: memberContext.currentOrgId,
    });
    expect(harness.request).toHaveBeenCalledWith(
      `/internal/api/v1/metadata-items/${assetId}/media/status`,
      expect.objectContaining({}),
    );
    expect(status).toMatchObject({
      assetId,
      uploadId,
      status: "COMPLETED",
      attemptCount: 2,
      stage: null,
      outputs: {
        thumbnail: { width: 256, height: 128, sizeBytes: 900, contentType: "image/png" },
        web: { width: 1080, height: 540, sizeBytes: 1800, contentType: "image/png" },
        expiresAt: "2026-08-19T11:15:00Z",
      },
      failedAt: null,
    });
  });

  test("permission revocation denies the next status poll before Asset Core transport", async () => {
    const harness = createHarness(queuedStatusResponse());

    await expect(harness.usecase.getStatus(memberContext, assetId)).resolves.toMatchObject({ status: "QUEUED" });
    const revoked = new Error("permission revoked");
    harness.assertAllowed.mockRejectedValueOnce(revoked);

    await expect(harness.usecase.getStatus(memberContext, assetId)).rejects.toBe(revoked);
    expect(harness.assertAllowed).toHaveBeenCalledTimes(2);
    expect(harness.request).toHaveBeenCalledTimes(1);
  });

  test.each([
    ["attempt count above the schema bound", { attempt_count: 4 }],
    ["unexpected internal storage field", { raw_object_key: "raw/org/asset/private.png" }],
  ])("rejects %s instead of proxying an invalid status", async (_name, overrides) => {
    const harness = createHarness(queuedStatusResponse(overrides));

    await expect(harness.usecase.getStatus(memberContext, assetId)).rejects.toMatchObject({
      extensions: { code: "INTERNAL_ERROR" },
    });
  });

  test("authorizes write on the metadata item before delegating session creation", async () => {
    const harness = createHarness(sessionResponse());

    const session = await harness.usecase.createSession(memberContext, assetId, {
      idempotencyKey: retryKey,
      filename: "photo.png",
      contentType: "image/png",
      sizeBytes: 7,
      checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
    });

    expect(harness.order).toEqual(["authorize", "rate-limit", "delegate"]);
    expect(harness.assertAllowed).toHaveBeenCalledWith({
      userId: memberContext.userId,
      action: "write",
      resourceType: "metadata_item",
      resourceId: assetId,
      orgId: memberContext.currentOrgId,
    });
    expect(harness.request).toHaveBeenCalledWith(
      `/internal/api/v1/metadata-items/${assetId}/media/uploads`,
      expect.objectContaining({
        method: "POST",
        body: {
          filename: "photo.png",
          content_type: "image/png",
          size_bytes: 7,
          checksum_sha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
        },
      }),
    );
    expect(session.upload?.url).toContain("X-Amz-Signature=opaque");
    expect(session.upload?.headers["If-None-Match"]).toBe("*");
    expect(session.replayed).toBe(false);
  });

  test("a matched idempotency key is reported as a replay rather than a second session", async () => {
    const replay = new Response(await sessionResponse().text(), {
      status: 200,
      headers: { "Idempotency-Replayed": "true" },
    });
    const harness = createHarness(replay);

    const session = await harness.usecase.createSession(memberContext, assetId, {
      idempotencyKey: retryKey,
      filename: "photo.png",
      contentType: "image/png",
      sizeBytes: 7,
      checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
    });

    expect(session.replayed).toBe(true);
    expect(session.uploadId).toBe(uploadId);
  });

  test("denied permission issues no upload authority", async () => {
    const transport = { request: vi.fn() };
    const denied = new Error("forbidden");
    const rateLimiter = { consumeSessionCreation: vi.fn() };
    const authorizedAsset = createAuthorizedAssetFetch({
      authorization: { assertAllowed: vi.fn().mockRejectedValue(denied) },
      transport,
    });
    const usecase = createMediaUploadUsecase({ authorizedAsset, rateLimiter });

    await expect(
      usecase.createSession(memberContext, assetId, {
        idempotencyKey: retryKey,
        filename: "photo.png",
        contentType: "image/png",
        sizeBytes: 7,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      }),
    ).rejects.toBe(denied);
    expect(rateLimiter.consumeSessionCreation).not.toHaveBeenCalled();
    expect(transport.request).not.toHaveBeenCalled();
  });

  test("changed retry metadata is delegated under the same key and preserves the safe conflict", async () => {
    const conflict = new Response(
      JSON.stringify({
        error: {
          code: "IDEMPOTENCY_KEY_REUSED",
          number: 6003,
          message: "safe",
          traceId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
          service: "asset-core",
        },
      }),
      { status: 409 },
    );
    const harness = createHarness(conflict);

    await expect(
      harness.usecase.createSession(memberContext, assetId, {
        idempotencyKey: retryKey,
        filename: "changed.png",
        contentType: "image/png",
        sizeBytes: 8,
        checksumSha256: "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
      }),
    ).rejects.toMatchObject({ extensions: { code: "IDEMPOTENCY_KEY_REUSED", number: 6003 } });
    expect(harness.order).toEqual(["authorize", "rate-limit", "delegate"]);
  });

  test("commit is permission-first and sends only the upload identity", async () => {
    const acceptedAt = "2026-08-13T10:01:00Z";
    const response = new Response(
      JSON.stringify({
        data: {
          asset_id: assetId,
          upload_id: uploadId,
          job_id: "66666666-6666-4666-8666-666666666666",
          status: "queued",
          original: {
            filename: "photo.png",
            content_type: "image/png",
            size_bytes: 7,
          },
          accepted_at: acceptedAt,
        },
      }),
      { status: 202 },
    );
    const harness = createHarness(response);

    const acceptance = await harness.usecase.commit(memberContext, assetId, uploadId);

    expect(harness.order).toEqual(["authorize", "delegate"]);
    expect(harness.request).toHaveBeenCalledWith(
      `/internal/api/v1/metadata-items/${assetId}/media`,
      expect.objectContaining({ method: "PUT", body: { upload_id: uploadId } }),
    );
    expect(acceptance).toMatchObject({ assetId, uploadId, status: "QUEUED", acceptedAt });
  });

  test("a repeated commit preserves the durable acceptance and reports replay", async () => {
    const acceptedAt = "2026-08-13T10:01:00Z";
    const response = new Response(
      JSON.stringify({
        data: {
          asset_id: assetId,
          upload_id: uploadId,
          job_id: "66666666-6666-4666-8666-666666666666",
          status: "queued",
          original: {
            filename: "photo.png",
            content_type: "image/png",
            size_bytes: 7,
          },
          accepted_at: acceptedAt,
        },
      }),
      { status: 200 },
    );
    const harness = createHarness(response);

    const acceptance = await harness.usecase.commit(memberContext, assetId, uploadId);

    expect(acceptance).toEqual({
      assetId,
      uploadId,
      jobId: "66666666-6666-4666-8666-666666666666",
      status: "QUEUED",
      original: { filename: "photo.png", contentType: "image/png", sizeBytes: 7 },
      acceptedAt,
      replayed: true,
    });
  });

  test("cancel is permission-first and delegates the exact DELETE route", async () => {
    const harness = createHarness(new Response(null, { status: 204 }));

    await harness.usecase.cancelSession(memberContext, assetId, uploadId);

    expect(harness.order).toEqual(["authorize", "delegate"]);
    expect(harness.assertAllowed).toHaveBeenCalledWith({
      userId: memberContext.userId,
      action: "write",
      resourceType: "metadata_item",
      resourceId: assetId,
      orgId: memberContext.currentOrgId,
    });
    expect(harness.request).toHaveBeenCalledWith(
      `/internal/api/v1/metadata-items/${assetId}/media/uploads/${uploadId}`,
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  test("non-member context is rejected before authorization or delegation", async () => {
    const harness = createHarness(sessionResponse());
    const nonMember = { ...memberContext, isMember: false };

    await expect(harness.usecase.getSession(nonMember, assetId, uploadId)).rejects.toMatchObject({
      extensions: { code: "FORBIDDEN" },
    });
    expect(harness.assertAllowed).not.toHaveBeenCalled();
    expect(harness.request).not.toHaveBeenCalled();
  });
});

import { createHash, randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import path from "node:path";
import type { FastifyInstance } from "fastify";
import { Client } from "pg";
import { afterAll, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { createCanDoMock } from "./helpers/canDoMock";

const { mockCanDo, mockFilterAllowedResourceIds } = vi.hoisted(() => ({
  mockCanDo: vi.fn(),
  mockFilterAllowedResourceIds: vi.fn(),
}));

vi.mock("../authz/decision", () => createCanDoMock(mockCanDo, mockFilterAllowedResourceIds));

import { closeRedisClient, getRedisClient } from "../cache/redisClient";
import { prisma } from "../db/prisma";
import { buildServer } from "../server";

const liveMediaE2E = process.env.MEDIA_E2E === "1";
const userId = "00000000-0000-0000-0000-000000000001";
const orgId = "00000000-0000-0000-0000-000000000010";
const otherOrgId = "00000000-0000-0000-0000-000000000099";
const folderId = "10000000-0000-0000-0000-000000000002";

describe.skipIf(!liveMediaE2E)("US1 PRESIGNED upload end to end", () => {
  let app: FastifyInstance;
  let assetDb: Client;
  let assetId: string;
  let bytes: Buffer;
  let checksumSha256: string;

  beforeAll(async () => {
    bytes = await readFile(path.resolve(__dirname, "../../../asset-core/testdata/media/valid/small-64x64.png"));
    checksumSha256 = createHash("sha256").update(bytes).digest("base64");
    assetId = randomUUID();
    assetDb = new Client({
      connectionString: process.env.ASSET_DB_URL ?? "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db",
    });
    await assetDb.connect();
    await assetDb.query("INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES ($1, $2, $3, $4)", [
      assetId,
      folderId,
      `E2E: media ${assetId}`,
      userId,
    ]);
    app = await buildServer();
    await app.ready();
  });

  afterAll(async () => {
    await deleteMediaRowsForE2EAssets();
    await app?.close();
    await closeRedisClient();
    await prisma.$disconnect();
    await assetDb?.end();
  });

  // Every media table references metadata_items with ON DELETE RESTRICT, so
  // media rows left behind here would block the shared `E2E:%` asset cleanup
  // that the other end-to-end suites run before their own fixtures.
  async function deleteMediaRowsForE2EAssets(): Promise<void> {
    if (!assetDb) return;
    const ownedAssets = "SELECT id FROM metadata_items WHERE title LIKE 'E2E:%'";
    await assetDb.query(
      `UPDATE metadata_items SET active_media_version_id = NULL, pending_media_version_id = NULL
       WHERE title LIKE 'E2E:%'`,
    );
    await assetDb.query(
      `DELETE FROM media_job_outbox WHERE job_id IN (
         SELECT id FROM media_processing_jobs WHERE asset_id IN (${ownedAssets}))`,
    );
    await assetDb.query(`DELETE FROM media_processing_jobs WHERE asset_id IN (${ownedAssets})`);
    await assetDb.query(
      `DELETE FROM media_outputs WHERE version_id IN (
         SELECT id FROM asset_media_versions WHERE asset_id IN (${ownedAssets}))`,
    );
    await assetDb.query(`DELETE FROM asset_media_versions WHERE asset_id IN (${ownedAssets})`);
    await assetDb.query(`DELETE FROM media_upload_sessions WHERE asset_id IN (${ownedAssets})`);
  }

  beforeEach(async () => {
    vi.restoreAllMocks();
    mockCanDo.mockReset();
    mockCanDo.mockResolvedValue({ allowed: true, reason: null });
    mockFilterAllowedResourceIds.mockReset();
    await clearSessionRateLimitCounters();
  });

  // Session creation counters are per fixed test identity and survive both the
  // minute window and the process, so a second run inside the same window would
  // otherwise see 429 instead of the behaviour under test.
  async function clearSessionRateLimitCounters(): Promise<void> {
    const redis = getRedisClient();
    const keys = await redis.keys("media:session-create:*");
    if (keys.length > 0) {
      await redis.del(...keys);
    }
  }

  async function createAsset(label: string): Promise<string> {
    const id = randomUUID();
    await assetDb.query("INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES ($1, $2, $3, $4)", [
      id,
      folderId,
      `E2E: ${label} ${id}`,
      userId,
    ]);
    return id;
  }

  async function createSessionFor(
    targetAssetId: string,
    declaration: { filename: string; sizeBytes: number; checksumSha256: string } = {
      filename: "small-64x64.png",
      sizeBytes: bytes.length,
      checksumSha256,
    },
  ) {
    return app.inject({
      method: "POST",
      url: `/api/v1/assets/${targetAssetId}/media/uploads`,
      headers: { "x-user-id": userId, "x-org-id": orgId, "idempotency-key": randomUUID() },
      payload: { ...declaration, contentType: "image/png" },
    });
  }

  test("bytes go only to private storage and commit creates one durable queued acceptance", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const identityHeaders = { "x-user-id": userId, "x-org-id": orgId };
    const uploadBody = Uint8Array.from(bytes).buffer;

    const bodyAttempt = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: { ...identityHeaders, "idempotency-key": randomUUID() },
      payload: {
        filename: "small-64x64.png",
        contentType: "image/png",
        sizeBytes: bytes.length,
        checksumSha256,
        file: bytes.toString("base64"),
      },
    });
    expect(bodyAttempt.statusCode).toBe(400);
    expect(fetchSpy).not.toHaveBeenCalled();

    const createResponse = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: { ...identityHeaders, "idempotency-key": randomUUID() },
      payload: {
        filename: "small-64x64.png",
        contentType: "image/png",
        sizeBytes: bytes.length,
        checksumSha256,
      },
    });
    expect(createResponse.statusCode).toBe(201);
    const session = createResponse.json().data as {
      uploadId: string;
      upload: { method: string; url: string; headers: Record<string, string> };
      rawUrl?: string;
      original?: { url?: string };
    };
    expect(session.upload.method).toBe("PUT");
    expect(new URL(session.upload.url).port).toBe("9000");
    expect(session.rawUrl).toBeUndefined();
    expect(session.original?.url).toBeUndefined();

    const uploadResponse = await fetch(session.upload.url, {
      method: session.upload.method,
      headers: session.upload.headers,
      body: uploadBody,
    });
    expect(uploadResponse.ok).toBe(true);

    const commitResponse = await app.inject({
      method: "PUT",
      url: `/api/v1/assets/${assetId}/media`,
      headers: identityHeaders,
      payload: { uploadId: session.uploadId },
    });
    expect(commitResponse.statusCode).toBe(202);
    expect(commitResponse.headers.location).toBeUndefined();
    const accepted = commitResponse.json().data as Record<string, unknown>;
    expect(accepted).toMatchObject({ assetId, uploadId: session.uploadId, status: "QUEUED" });
    expect(accepted).not.toHaveProperty("outputs");
    expect(accepted).not.toHaveProperty("sha256");

    const repeatedCommit = await app.inject({
      method: "PUT",
      url: `/api/v1/assets/${assetId}/media`,
      headers: identityHeaders,
      payload: { uploadId: session.uploadId },
    });
    expect(repeatedCommit.statusCode).toBe(200);
    expect(repeatedCommit.json().data.jobId).toBe(accepted.jobId);

    const exactRawURL = `http://127.0.0.1:9000/seta-media/raw/${orgId}/${assetId}/${session.uploadId}/original.png`;
    const anonymousRawRead = await fetch(exactRawURL);
    expect(anonymousRawRead.status).toBe(403);
    const anonymousBucketList = await fetch("http://127.0.0.1:9000/seta-media?list-type=2");
    expect(anonymousBucketList.status).toBe(403);

    const transferCalls = fetchSpy.mock.calls.filter(([url]) => new URL(String(url)).port === "9000");
    expect(transferCalls).toHaveLength(3);
    expect(transferCalls.filter(([, init]) => init?.body === uploadBody)).toHaveLength(1);
    const serviceCalls = fetchSpy.mock.calls.filter(([url]) => new URL(String(url)).port === "8080");
    expect(serviceCalls.length).toBeGreaterThanOrEqual(3);
    for (const [, init] of serviceCalls) {
      expect(init?.body).not.toBe(bytes);
      expect(init?.body).not.toBe(uploadBody);
      expect(String(init?.body ?? "")).not.toContain(bytes.toString("base64"));
    }
  });

  test("cross-tenant and permission-denied callers receive no upload authority", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const crossTenant = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: { "x-user-id": userId, "x-org-id": otherOrgId, "idempotency-key": randomUUID() },
      payload: {
        filename: "small-64x64.png",
        contentType: "image/png",
        sizeBytes: bytes.length,
        checksumSha256,
      },
    });
    expect(crossTenant.statusCode).toBe(403);
    expect(fetchSpy).not.toHaveBeenCalled();

    mockCanDo.mockResolvedValue({ allowed: false, reason: "denied" });
    const denied = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: { "x-user-id": userId, "x-org-id": orgId, "idempotency-key": randomUUID() },
      payload: {
        filename: "small-64x64.png",
        contentType: "image/png",
        sizeBytes: bytes.length,
        checksumSha256,
      },
    });
    expect(denied.statusCode).toBe(403);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  test("1-byte and 50,000,000-byte declarations use one PRESIGNED strategy and obsolete negotiation is rejected", async () => {
    for (const sizeBytes of [1, 50_000_000]) {
      const boundaryAssetId = await createAsset(`boundary-${sizeBytes}`);
      const response = await createSessionFor(boundaryAssetId, {
        filename: "boundary.png",
        sizeBytes,
        checksumSha256: createHash("sha256")
          .update(Buffer.alloc(Math.min(sizeBytes, 1)))
          .digest("base64"),
      });
      expect(response.statusCode).toBe(201);
      expect(response.json().data).toMatchObject({
        assetId: boundaryAssetId,
        state: "CREATED",
        upload: { protocol: "HTTP", method: "PUT" },
      });
      expect(response.json().data).not.toHaveProperty("strategy");
      expect(response.json().data).not.toHaveProperty("multipart");
    }

    const obsoleteAssetId = await createAsset("obsolete-negotiation");
    for (const extra of [
      { capabilities: { multipart: true } },
      { strategy: "MULTIPART" },
      { manifest: [{ part: 1 }] },
    ]) {
      const response = await app.inject({
        method: "POST",
        url: `/api/v1/assets/${obsoleteAssetId}/media/uploads`,
        headers: { "x-user-id": userId, "x-org-id": orgId, "idempotency-key": randomUUID() },
        payload: {
          filename: "small-64x64.png",
          contentType: "image/png",
          sizeBytes: bytes.length,
          checksumSha256,
          ...extra,
        },
      });
      expect(response.statusCode).toBe(400);
    }
  });

  test("refresh retries the complete file and preserves checksum-bound no-overwrite authority", async () => {
    const retryAssetId = await createAsset("refresh-complete-retry");
    const created = await createSessionFor(retryAssetId);
    expect(created.statusCode).toBe(201);
    const original = created.json().data as {
      uploadId: string;
      sessionExpiresAt: string;
      upload: { url: string; method: string; headers: Record<string, string>; credentialExpiresAt: string };
    };

    const refreshedResponse = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${retryAssetId}/media/uploads/${original.uploadId}/refresh`,
      headers: { "x-user-id": userId, "x-org-id": orgId },
    });
    expect(refreshedResponse.statusCode).toBe(200);
    const refreshed = refreshedResponse.json().data as typeof original;
    expect(refreshed.uploadId).toBe(original.uploadId);
    expect(refreshed.sessionExpiresAt).toBe(original.sessionExpiresAt);
    expect(refreshed.upload.headers["X-Amz-Checksum-Sha256"]).toBe(checksumSha256);

    let interrupted: Response | undefined;
    try {
      interrupted = await fetch(refreshed.upload.url, {
        method: refreshed.upload.method,
        headers: refreshed.upload.headers,
        body: Uint8Array.from(bytes.subarray(0, Math.max(1, bytes.length - 1))).buffer,
      });
    } catch {
      // Undici detects the signed full-file Content-Length mismatch before the
      // transport finishes, which is the expected interrupted-transfer result.
    }
    if (interrupted) expect(interrupted.ok).toBe(false);

    const complete = await fetch(refreshed.upload.url, {
      method: refreshed.upload.method,
      headers: refreshed.upload.headers,
      body: Uint8Array.from(bytes).buffer,
    });
    expect(complete.ok).toBe(true);

    const overwrite = await fetch(refreshed.upload.url, {
      method: refreshed.upload.method,
      headers: refreshed.upload.headers,
      body: Uint8Array.from(bytes).buffer,
    });
    expect(overwrite.status).toBe(412);

    const mismatchAssetId = await createAsset("checksum-mismatch");
    const mismatchSession = (await createSessionFor(mismatchAssetId)).json().data as typeof original;
    const wrongBytes = Buffer.from(bytes);
    wrongBytes[wrongBytes.length - 1] ^= 0xff;
    const mismatch = await fetch(mismatchSession.upload.url, {
      method: mismatchSession.upload.method,
      headers: mismatchSession.upload.headers,
      body: Uint8Array.from(wrongBytes).buffer,
    });
    expect(mismatch.ok).toBe(false);
  });

  test("a lost successful PUT response is recovered by exact-object HEAD before commit", async () => {
    const lostResponseAssetId = await createAsset("lost-put-response");
    const created = await createSessionFor(lostResponseAssetId);
    const session = created.json().data as {
      uploadId: string;
      upload: { url: string; method: string; headers: Record<string, string> };
    };

    // Treat the response as lost after the storage write completes.
    await fetch(session.upload.url, {
      method: session.upload.method,
      headers: session.upload.headers,
      body: Uint8Array.from(bytes).buffer,
    });

    const refresh = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${lostResponseAssetId}/media/uploads/${session.uploadId}/refresh`,
      headers: { "x-user-id": userId, "x-org-id": orgId },
    });
    expect(refresh.statusCode).toBe(409);
    expect(refresh.json()).toMatchObject({ error: { code: "MEDIA_UPLOAD_STATE_CONFLICT" } });

    const commit = await app.inject({
      method: "PUT",
      url: `/api/v1/assets/${lostResponseAssetId}/media`,
      headers: { "x-user-id": userId, "x-org-id": orgId },
      payload: { uploadId: session.uploadId },
    });
    expect(commit.statusCode).toBe(202);
  });

  test("cancel is idempotent, frees the asset immediately, and quarantines the abandoned exact object", async () => {
    const cancelledAssetId = await createAsset("cancelled-upload");
    const created = await createSessionFor(cancelledAssetId);
    expect(created.statusCode).toBe(201);
    const session = created.json().data as {
      uploadId: string;
      upload: { url: string; method: string; headers: Record<string, string> };
    };
    const transfer = await fetch(session.upload.url, {
      method: session.upload.method,
      headers: session.upload.headers,
      body: Uint8Array.from(bytes).buffer,
    });
    expect(transfer.ok).toBe(true);

    for (let attempt = 0; attempt < 2; attempt += 1) {
      const cancelled = await app.inject({
        method: "DELETE",
        url: `/api/v1/assets/${cancelledAssetId}/media/uploads/${session.uploadId}`,
        headers: { "x-user-id": userId, "x-org-id": orgId },
      });
      expect(cancelled.statusCode).toBe(204);
      expect(cancelled.body).toBe("");
    }

    const replacement = await createSessionFor(cancelledAssetId);
    expect(replacement.statusCode).toBe(201);
    expect(replacement.json().data.uploadId).not.toBe(session.uploadId);

    // Cancellation never blocks on storage. The original exact object stays
    // quarantined and immutable until T097's bounded cleanup loop reclaims it.
    const quarantinedOverwrite = await fetch(session.upload.url, {
      method: session.upload.method,
      headers: session.upload.headers,
      body: Uint8Array.from(bytes).buffer,
    });
    expect(quarantinedOverwrite.status).toBe(412);
  });
});

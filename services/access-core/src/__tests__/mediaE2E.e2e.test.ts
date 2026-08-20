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
const liveMediaWorker = process.env.MEDIA_WORKER_E2E === "1";
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
    // Deleting session rows does not release what they reserved, so without this
    // every run leaks its declared bytes — including the 50,000,000-byte
    // admission case — until the organization can no longer accept an upload.
    await assetDb.query(`UPDATE organization_media_usage SET reserved_raw_bytes = 0 WHERE org_id = $1`, [orgId]);
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

describe.skipIf(!liveMediaE2E || !liveMediaWorker)("US2 rendition pipeline end to end", () => {
  let app: FastifyInstance;
  let assetDb: Client;
  let landscape: Buffer;
  let landscapeChecksum: string;
  let hostile: Buffer;
  let hostileChecksum: string;

  const claimToTerminalBudgetMs = 90_000;

  beforeAll(async () => {
    landscape = await readFile(
      path.resolve(__dirname, "../../../asset-core/testdata/media/valid/landscape-2048x1152.jpg"),
    );
    landscapeChecksum = createHash("sha256").update(landscape).digest("base64");
    hostile = await readFile(
      path.resolve(__dirname, "../../../asset-core/testdata/media/hostile/jpeg-trailing-payload.jpg"),
    );
    hostileChecksum = createHash("sha256").update(hostile).digest("base64");
    assetDb = new Client({
      connectionString: process.env.ASSET_DB_URL ?? "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db",
    });
    await assetDb.connect();
    app = await buildServer();
    await app.ready();
  });

  afterAll(async () => {
    await cleanupProcessedAssets();
    await app?.close();
    await closeRedisClient();
    await prisma.$disconnect();
    await assetDb?.end();
  });

  beforeEach(() => {
    mockCanDo.mockResolvedValue({ allowed: true });
  });

  async function cleanupProcessedAssets(): Promise<void> {
    if (!assetDb) return;
    const owned = "SELECT id FROM metadata_items WHERE title LIKE 'E2E-US2:%'";
    await assetDb.query(
      `UPDATE metadata_items SET active_media_version_id = NULL, pending_media_version_id = NULL
       WHERE title LIKE 'E2E-US2:%'`,
    );
    await assetDb.query(
      `DELETE FROM media_job_outbox WHERE job_id IN (
         SELECT id FROM media_processing_jobs WHERE asset_id IN (${owned}))`,
    );
    await assetDb.query(`DELETE FROM media_processing_jobs WHERE asset_id IN (${owned})`);
    await assetDb.query(
      `DELETE FROM media_outputs WHERE version_id IN (
         SELECT id FROM asset_media_versions WHERE asset_id IN (${owned}))`,
    );
    await assetDb.query(`DELETE FROM asset_media_versions WHERE asset_id IN (${owned})`);
    await assetDb.query(`DELETE FROM media_upload_sessions WHERE asset_id IN (${owned})`);
    await assetDb.query(`DELETE FROM metadata_items WHERE title LIKE 'E2E-US2:%'`);
  }

  async function createProcessedAsset(label: string): Promise<string> {
    const id = randomUUID();
    await assetDb.query("INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES ($1, $2, $3, $4)", [
      id,
      folderId,
      `E2E-US2: ${label} ${id}`,
      userId,
    ]);
    return id;
  }

  async function clearRateLimitCounters(): Promise<void> {
    const redis = getRedisClient();
    const keys = await redis.keys("media:session-create:*");
    if (keys.length > 0) {
      await redis.del(...keys);
    }
  }

  async function uploadAndCommit(
    targetAssetId: string,
    payload: Buffer,
    checksum: string,
    filename: string,
    contentType: string,
  ): Promise<{ uploadId: string; jobId: string; committedAt: number }> {
    await clearRateLimitCounters();
    const identityHeaders = { "x-user-id": userId, "x-org-id": orgId };
    const created = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${targetAssetId}/media/uploads`,
      headers: { ...identityHeaders, "idempotency-key": randomUUID() },
      payload: { filename, contentType, sizeBytes: payload.length, checksumSha256: checksum },
    });
    expect(created.statusCode).toBe(201);
    const session = created.json().data as {
      uploadId: string;
      upload: { method: string; url: string; headers: Record<string, string> };
    };

    const transfer = await fetch(session.upload.url, {
      method: session.upload.method,
      headers: session.upload.headers,
      body: Uint8Array.from(payload).buffer,
    });
    expect(transfer.ok).toBe(true);

    const commit = await app.inject({
      method: "PUT",
      url: `/api/v1/assets/${targetAssetId}/media`,
      headers: identityHeaders,
      payload: { uploadId: session.uploadId },
    });
    expect(commit.statusCode).toBe(202);
    return { uploadId: session.uploadId, jobId: commit.json().data.jobId as string, committedAt: Date.now() };
  }

  async function waitForTerminalJob(
    targetAssetId: string,
    jobId: string,
  ): Promise<{ status: string; elapsedMs: number; data: Record<string, any> }> {
    const startedAt = Date.now();
    while (Date.now() - startedAt < claimToTerminalBudgetMs) {
      const response = await app.inject({
        method: "GET",
        url: `/api/v1/assets/${targetAssetId}/media/status`,
        headers: { "x-user-id": userId, "x-org-id": orgId },
      });
      if (response.statusCode !== 200) {
        throw new Error(`status poll returned ${response.statusCode}: ${response.body}`);
      }
      const data = response.json().data as Record<string, any>;
      if (data.jobId !== jobId) {
        throw new Error(`status poll returned job ${String(data.jobId)}, want ${jobId}`);
      }
      const status = String(data.status).toLowerCase();
      if (status === "completed" || status === "failed") {
        return { status, elapsedMs: Date.now() - startedAt, data };
      }
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
    throw new Error(`job ${jobId} did not reach a terminal state within ${claimToTerminalBudgetMs}ms`);
  }

  test(
    "a committed upload yields exactly two safe derivatives, atomic activation, and a retained private raw",
    async () => {
      const processedAssetId = await createProcessedAsset("rendition");
      const { uploadId, jobId, committedAt } = await uploadAndCommit(
        processedAssetId,
        landscape,
        landscapeChecksum,
        "landscape-2048x1152.jpg",
        "image/jpeg",
      );

      const terminal = await waitForTerminalJob(processedAssetId, jobId);
      expect(terminal.status).toBe("completed");
      expect(Date.now() - committedAt).toBeLessThan(claimToTerminalBudgetMs);
      expect(terminal.data).toMatchObject({
        assetId: processedAssetId,
        uploadId,
        jobId,
        status: "COMPLETED",
        outputs: {
          thumbnail: { width: expect.any(Number), height: expect.any(Number), sizeBytes: expect.any(Number) },
          web: { width: expect.any(Number), height: expect.any(Number), sizeBytes: expect.any(Number) },
          expiresAt: expect.any(String),
        },
        error: null,
      });
      expect(new Date(terminal.data.outputs.expiresAt).getTime() - Date.now()).toBeLessThanOrEqual(15 * 60 * 1000);
      expect(new Date(terminal.data.outputs.expiresAt).getTime()).toBeGreaterThan(Date.now());

      const version = await assetDb.query<{
        id: string;
        status: string;
        detected_content_type: string;
        source_width: number;
        source_height: number;
        sha256: Buffer;
      }>(
        `SELECT id, status, detected_content_type, source_width, source_height, sha256
         FROM asset_media_versions WHERE asset_id = $1`,
        [processedAssetId],
      );
      expect(version.rows).toHaveLength(1);
      expect(version.rows[0].status).toBe("completed");
      expect(version.rows[0].detected_content_type).toBe("image/jpeg");
      expect(version.rows[0].source_width).toBe(2048);
      expect(version.rows[0].source_height).toBe(1152);
      expect(version.rows[0].sha256.toString("base64")).toBe(landscapeChecksum);

      const outputs = await assetDb.query<{
        kind: string;
        width: number;
        height: number;
        content_type: string;
        object_key: string;
        size_bytes: string;
      }>(
        "SELECT kind, width, height, content_type, object_key, size_bytes FROM media_outputs WHERE version_id = $1 ORDER BY kind",
        [version.rows[0].id],
      );
      expect(outputs.rows).toHaveLength(2);
      expect(outputs.rows.map((row) => row.kind)).toEqual(["thumbnail", "web"]);

      const thumbnail = outputs.rows[0];
      const web = outputs.rows[1];
      expect(Math.max(thumbnail.width, thumbnail.height)).toBe(256);
      expect(Math.max(web.width, web.height)).toBe(1080);
      expect(thumbnail.width / thumbnail.height).toBeCloseTo(2048 / 1152, 1);
      expect(web.width / web.height).toBeCloseTo(2048 / 1152, 1);
      for (const output of outputs.rows) {
        expect(output.content_type).toBe("image/jpeg");
        expect(output.object_key.startsWith(`processed/${orgId}/${processedAssetId}/`)).toBe(true);
        expect(Number(output.size_bytes)).toBeGreaterThan(0);
        expect(Number(output.size_bytes)).toBeLessThan(landscape.length);
      }

      const pointers = await assetDb.query<{
        active_media_version_id: string | null;
        pending_media_version_id: string | null;
      }>("SELECT active_media_version_id, pending_media_version_id FROM metadata_items WHERE id = $1", [
        processedAssetId,
      ]);
      expect(pointers.rows[0].active_media_version_id).toBe(version.rows[0].id);
      expect(pointers.rows[0].pending_media_version_id).toBeNull();

      const rawURL = `http://127.0.0.1:9000/seta-media/raw/${orgId}/${processedAssetId}/${uploadId}/original.jpg`;
      expect((await fetch(rawURL)).status).toBe(403);
      for (const output of outputs.rows) {
        expect((await fetch(`http://127.0.0.1:9000/seta-media/${output.object_key}`)).status).toBe(403);
      }
    },
    claimToTerminalBudgetMs + 30_000,
  );

  test(
    "hostile content fails the version deterministically and stores no derivative",
    async () => {
      const hostileAssetId = await createProcessedAsset("hostile");
      const { jobId } = await uploadAndCommit(
        hostileAssetId,
        hostile,
        hostileChecksum,
        "jpeg-trailing-payload.jpg",
        "image/jpeg",
      );

      const terminal = await waitForTerminalJob(hostileAssetId, jobId);
      expect(terminal.status).toBe("failed");
      expect(terminal.data).toMatchObject({
        status: "FAILED",
        outputs: null,
        error: { code: expect.any(String), message: expect.any(String) },
      });
      expect(JSON.stringify(terminal.data.error)).not.toContain("/tmp/");

      const version = await assetDb.query<{ id: string; status: string; failure_code: string | null }>(
        "SELECT id, status, failure_code FROM asset_media_versions WHERE asset_id = $1",
        [hostileAssetId],
      );
      expect(version.rows[0].status).toBe("failed");
      expect(version.rows[0].failure_code).toBeTruthy();

      const outputs = await assetDb.query("SELECT 1 FROM media_outputs WHERE version_id = $1", [version.rows[0].id]);
      expect(outputs.rows).toHaveLength(0);

      const pointers = await assetDb.query<{
        active_media_version_id: string | null;
        pending_media_version_id: string | null;
      }>("SELECT active_media_version_id, pending_media_version_id FROM metadata_items WHERE id = $1", [
        hostileAssetId,
      ]);
      expect(pointers.rows[0].active_media_version_id).toBeNull();
      expect(pointers.rows[0].pending_media_version_id).toBeNull();

      const attempts = await assetDb.query<{ attempt_count: number }>(
        "SELECT attempt_count FROM media_processing_jobs WHERE id = $1",
        [jobId],
      );
      expect(attempts.rows[0].attempt_count).toBe(1);
    },
    claimToTerminalBudgetMs + 30_000,
  );

  test(
    "a failed replacement leaves the previously active rendition serving",
    async () => {
      const replacedAssetId = await createProcessedAsset("replacement");
      const first = await uploadAndCommit(
        replacedAssetId,
        landscape,
        landscapeChecksum,
        "landscape-2048x1152.jpg",
        "image/jpeg",
      );
      expect((await waitForTerminalJob(replacedAssetId, first.jobId)).status).toBe("completed");

      const activeBefore = await assetDb.query<{ active_media_version_id: string }>(
        "SELECT active_media_version_id FROM metadata_items WHERE id = $1",
        [replacedAssetId],
      );
      const originalVersionId = activeBefore.rows[0].active_media_version_id;
      expect(originalVersionId).toBeTruthy();

      const replacement = await uploadAndCommit(
        replacedAssetId,
        hostile,
        hostileChecksum,
        "jpeg-trailing-payload.jpg",
        "image/jpeg",
      );
      expect((await waitForTerminalJob(replacedAssetId, replacement.jobId)).status).toBe("failed");

      const activeAfter = await assetDb.query<{
        active_media_version_id: string;
        pending_media_version_id: string | null;
      }>("SELECT active_media_version_id, pending_media_version_id FROM metadata_items WHERE id = $1", [
        replacedAssetId,
      ]);
      expect(activeAfter.rows[0].active_media_version_id).toBe(originalVersionId);
      expect(activeAfter.rows[0].pending_media_version_id).toBeNull();

      const survivingOutputs = await assetDb.query("SELECT 1 FROM media_outputs WHERE version_id = $1", [
        originalVersionId,
      ]);
      expect(survivingOutputs.rows).toHaveLength(2);
    },
    2 * claimToTerminalBudgetMs + 30_000,
  );

  test(
    "the hostile corpus is rejected end to end without producing a derivative",
    async () => {
      const corpus = [
        { file: "hostile/jpeg-concatenated.jpg", contentType: "image/jpeg" },
        { file: "hostile/jpeg-truncated.jpg", contentType: "image/jpeg" },
        { file: "hostile/png-concatenated.png", contentType: "image/png" },
        { file: "hostile/png-trailing-payload.png", contentType: "image/png" },
        { file: "hostile/png-bad-crc.png", contentType: "image/png" },
        { file: "hostile/png-animated.apng.png", contentType: "image/png" },
        { file: "hostile/png-dimension-bomb.png", contentType: "image/png" },
        { file: "hostile/not-an-image.png", contentType: "image/png" },
      ];

      for (const sample of corpus) {
        const payload = await readFile(path.resolve(__dirname, `../../../asset-core/testdata/media/${sample.file}`));
        const checksum = createHash("sha256").update(payload).digest("base64");
        const corpusAssetId = await createProcessedAsset(`hostile ${sample.file}`);
        const extension = sample.contentType === "image/jpeg" ? "jpg" : "png";

        const { jobId } = await uploadAndCommit(
          corpusAssetId,
          payload,
          checksum,
          `sample.${extension}`,
          sample.contentType,
        );
        const terminal = await waitForTerminalJob(corpusAssetId, jobId);

        expect(terminal.status, `${sample.file} must fail`).toBe("failed");

        const version = await assetDb.query<{ id: string; failure_code: string | null }>(
          "SELECT id, failure_code FROM asset_media_versions WHERE asset_id = $1",
          [corpusAssetId],
        );
        expect(version.rows[0].failure_code, `${sample.file} must record a code`).toBeTruthy();

        const outputs = await assetDb.query("SELECT 1 FROM media_outputs WHERE version_id = $1", [version.rows[0].id]);
        expect(outputs.rows, `${sample.file} must produce no derivative`).toHaveLength(0);

        const pointers = await assetDb.query<{ active_media_version_id: string | null }>(
          "SELECT active_media_version_id FROM metadata_items WHERE id = $1",
          [corpusAssetId],
        );
        expect(pointers.rows[0].active_media_version_id, `${sample.file} must not activate`).toBeNull();
      }
    },
    8 * claimToTerminalBudgetMs,
  );

  test("processed derivatives are never reachable without authorization and never leak across organizations", async () => {
    const isolatedAssetId = await createProcessedAsset("isolation");
    const { jobId } = await uploadAndCommit(
      isolatedAssetId,
      landscape,
      landscapeChecksum,
      "landscape-2048x1152.jpg",
      "image/jpeg",
    );
    expect((await waitForTerminalJob(isolatedAssetId, jobId)).status).toBe("completed");

    const outputs = await assetDb.query<{ object_key: string }>(
      `SELECT object_key FROM media_outputs WHERE version_id = (
         SELECT id FROM asset_media_versions WHERE asset_id = $1)`,
      [isolatedAssetId],
    );
    expect(outputs.rows).toHaveLength(2);

    for (const output of outputs.rows) {
      expect(output.object_key.startsWith(`processed/${orgId}/`)).toBe(true);
      expect((await fetch(`http://127.0.0.1:9000/seta-media/${output.object_key}`)).status).toBe(403);
    }
    expect((await fetch("http://127.0.0.1:9000/seta-media?list-type=2")).status).toBe(403);

    const crossTenantRead = await app.inject({
      method: "GET",
      url: `/api/v1/assets/${isolatedAssetId}/media/status`,
      headers: { "x-user-id": userId, "x-org-id": otherOrgId },
    });
    expect(crossTenantRead.statusCode).toBeGreaterThanOrEqual(400);

    mockCanDo.mockResolvedValue({ allowed: false, reason: "revoked" });
    const revokedRead = await app.inject({
      method: "GET",
      url: `/api/v1/assets/${isolatedAssetId}/media/status`,
      headers: { "x-user-id": userId, "x-org-id": orgId },
    });
    expect(revokedRead.statusCode).toBe(403);
    mockCanDo.mockResolvedValue({ allowed: true });

    const rawKeys = await assetDb.query<{ raw_object_key: string }>(
      "SELECT raw_object_key FROM asset_media_versions WHERE asset_id = $1",
      [isolatedAssetId],
    );
    expect((await fetch(`http://127.0.0.1:9000/seta-media/${rawKeys.rows[0].raw_object_key}`)).status).toBe(403);
  });
});

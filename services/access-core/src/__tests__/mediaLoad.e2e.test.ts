import { createHash, randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import path from "node:path";
import type { FastifyInstance } from "fastify";
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, test, vi } from "vitest";
import { createCanDoMock } from "./helpers/canDoMock";

const { mockCanDo, mockFilterAllowedResourceIds } = vi.hoisted(() => ({
  mockCanDo: vi.fn(),
  mockFilterAllowedResourceIds: vi.fn(),
}));

vi.mock("../authz/decision", () => createCanDoMock(mockCanDo, mockFilterAllowedResourceIds));

import { closeRedisClient, getRedisClient } from "../cache/redisClient";
import { prisma } from "../db/prisma";
import { buildServer } from "../server";

const enabled = process.env.MEDIA_LOAD_E2E === "1";
const userId = "00000000-0000-0000-0000-000000000001";
const orgId = "00000000-0000-0000-0000-000000000010";
const folderId = "10000000-0000-0000-0000-000000000002";
const sampleCount = 20;
const batchSize = 5;
const terminalBudgetMs = 120_000;

function percentile95(values: number[]): number {
  if (values.length === 0) throw new Error("cannot calculate p95 without samples");
  const sorted = [...values].sort((left, right) => left - right);
  return sorted[Math.ceil(sorted.length * 0.95) - 1];
}

describe.skipIf(!enabled)("KAN-87 normal-load media SLO probe", () => {
  let app: FastifyInstance;
  let assetDb: Client;
  let image: Buffer;
  let checksum: string;

  beforeAll(async () => {
    image = await readFile(path.resolve(__dirname, "../../../asset-core/testdata/media/valid/small-64x64.png"));
    checksum = createHash("sha256").update(image).digest("base64");
    mockCanDo.mockResolvedValue({ allowed: true, reason: null });
    assetDb = new Client({
      connectionString: process.env.ASSET_DB_URL ?? "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db",
    });
    await assetDb.connect();
    const redis = getRedisClient();
    const keys = await redis.keys("media:session-create:*");
    if (keys.length > 0) await redis.del(...keys);
    app = await buildServer();
    await app.ready();
  });

  afterAll(async () => {
    if (assetDb) {
      const owned = "SELECT id FROM metadata_items WHERE title LIKE 'E2E-MEDIA-LOAD:%'";
      await assetDb.query(
        "UPDATE metadata_items SET active_media_version_id = NULL, pending_media_version_id = NULL WHERE title LIKE 'E2E-MEDIA-LOAD:%'",
      );
      await assetDb.query(
        `DELETE FROM media_job_outbox WHERE job_id IN (SELECT id FROM media_processing_jobs WHERE asset_id IN (${owned}))`,
      );
      await assetDb.query(`DELETE FROM media_processing_jobs WHERE asset_id IN (${owned})`);
      await assetDb.query(
        `DELETE FROM media_outputs WHERE version_id IN (SELECT id FROM asset_media_versions WHERE asset_id IN (${owned}))`,
      );
      await assetDb.query(`DELETE FROM asset_media_versions WHERE asset_id IN (${owned})`);
      await assetDb.query(`DELETE FROM media_upload_sessions WHERE asset_id IN (${owned})`);
      await assetDb.query("DELETE FROM metadata_items WHERE title LIKE 'E2E-MEDIA-LOAD:%'");
    }
    await app?.close();
    await closeRedisClient();
    await prisma.$disconnect();
    await assetDb?.end();
  });

  async function submit(index: number, assetId: string): Promise<{ jobId: string; commitDurationMs: number }> {
    const created = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: { "x-user-id": userId, "x-org-id": orgId, "idempotency-key": randomUUID() },
      payload: {
        filename: `load-${index}.png`,
        contentType: "image/png",
        sizeBytes: image.length,
        checksumSha256: checksum,
      },
    });
    expect(created.statusCode).toBe(201);
    const session = created.json().data as {
      uploadId: string;
      upload: { method: string; url: string; headers: Record<string, string> };
    };
    const transfer = await fetch(session.upload.url, {
      method: session.upload.method,
      headers: session.upload.headers,
      body: Uint8Array.from(image).buffer,
    });
    expect(transfer.ok).toBe(true);

    const commitStartedAt = performance.now();
    const committed = await app.inject({
      method: "PUT",
      url: `/api/v1/assets/${assetId}/media`,
      headers: { "x-user-id": userId, "x-org-id": orgId },
      payload: { uploadId: session.uploadId },
    });
    const commitDurationMs = performance.now() - commitStartedAt;
    expect(committed.statusCode).toBe(202);
    return { jobId: committed.json().data.jobId as string, commitDurationMs };
  }

  test(
    "p95 durable acceptance-to-terminal stays within 120 seconds under the bounded local profile",
    async () => {
      const assetIds: string[] = [];
      for (let index = 0; index < sampleCount; index += 1) {
        const assetId = randomUUID();
        assetIds.push(assetId);
        await assetDb.query("INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES ($1, $2, $3, $4)", [
          assetId,
          folderId,
          `E2E-MEDIA-LOAD: ${index} ${assetId}`,
          userId,
        ]);
      }
      const submissions: Array<{ jobId: string; commitDurationMs: number }> = [];
      for (let start = 0; start < sampleCount; start += batchSize) {
        submissions.push(
          ...(await Promise.all(
            Array.from({ length: batchSize }, (_, offset) => submit(start + offset, assetIds[start + offset])),
          )),
        );
      }
      const jobIds = submissions.map((submission) => submission.jobId);
      let maxQueued = 0;
      let maxProcessing = 0;
      let rows: Array<{
        status: string;
        queued_at: Date;
        started_at: Date | null;
        terminal_at: Date | null;
      }> = [];
      const pollingStartedAt = Date.now();
      while (Date.now() - pollingStartedAt < terminalBudgetMs) {
        const result = await assetDb.query<{
          status: string;
          queued_at: Date;
          started_at: Date | null;
          terminal_at: Date | null;
        }>(
          `SELECT status, queued_at, started_at, COALESCE(completed_at, failed_at) AS terminal_at
           FROM media_processing_jobs WHERE id = ANY($1::uuid[])`,
          [jobIds],
        );
        rows = result.rows;
        maxQueued = Math.max(maxQueued, rows.filter((row) => row.status === "queued").length);
        maxProcessing = Math.max(maxProcessing, rows.filter((row) => row.status === "processing").length);
        if (rows.length === sampleCount && rows.every((row) => row.terminal_at !== null)) break;
        await new Promise((resolve) => setTimeout(resolve, 100));
      }

      expect(rows).toHaveLength(sampleCount);
      expect(rows.every((row) => row.status === "completed" && row.started_at && row.terminal_at)).toBe(true);
      const acceptanceToTerminal = rows.map((row) => (row.terminal_at!.getTime() - row.queued_at.getTime()) / 1_000);
      const claimToTerminal = rows.map((row) => (row.terminal_at!.getTime() - row.started_at!.getTime()) / 1_000);
      const commitP95Seconds = percentile95(submissions.map((submission) => submission.commitDurationMs)) / 1_000;
      const acceptanceP95Seconds = percentile95(acceptanceToTerminal);
      const claimP95Seconds = percentile95(claimToTerminal);

      console.info(
        JSON.stringify({
          event: "media_normal_load_probe",
          sampleCount,
          offeredConcurrency: batchSize,
          commitP95Seconds,
          acceptanceToTerminalP95Seconds: acceptanceP95Seconds,
          claimToTerminalP95Seconds: claimP95Seconds,
          maxQueued,
          maxProcessing,
        }),
      );
      expect(commitP95Seconds).toBeLessThanOrEqual(2);
      expect(acceptanceP95Seconds).toBeLessThanOrEqual(120);
      expect(claimP95Seconds).toBeLessThanOrEqual(90);
    },
    terminalBudgetMs + 60_000,
  );
});

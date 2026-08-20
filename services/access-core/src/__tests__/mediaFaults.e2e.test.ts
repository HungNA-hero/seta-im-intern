import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import path from "node:path";
import type { FastifyInstance } from "fastify";
import { Client } from "pg";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { createCanDoMock } from "./helpers/canDoMock";

const { mockCanDo, mockFilterAllowedResourceIds } = vi.hoisted(() => ({
  mockCanDo: vi.fn(),
  mockFilterAllowedResourceIds: vi.fn(),
}));

vi.mock("../authz/decision", () => createCanDoMock(mockCanDo, mockFilterAllowedResourceIds));

import { closeRedisClient, getRedisClient } from "../cache/redisClient";
import { prisma } from "../db/prisma";
import { buildServer } from "../server";

const enabled = process.env.MEDIA_FAULT_E2E === "1";
const userId = "00000000-0000-0000-0000-000000000001";
const orgId = "00000000-0000-0000-0000-000000000010";
const folderId = "10000000-0000-0000-0000-000000000002";
const project = process.env.MEDIA_E2E_COMPOSE_PROJECT ?? "seta-soft-delete-e2e";
const containerPrefix = process.env.SETA_COMPOSE_PREFIX ?? project;
const composeFile = path.resolve(__dirname, "../../../../infra/docker-compose.yml");
const terminalBudgetMs = 120_000;
const assetDatabaseUrl = process.env.ASSET_DB_URL ?? "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db";
const accessDatabaseUrl =
  process.env.DATABASE_URL ?? "postgresql://access_user:access_password@127.0.0.1:5434/access_db";
const commandEnvironment = {
  ...process.env,
  ASSET_DB_PORT: new URL(assetDatabaseUrl).port || "5433",
  ACCESS_DB_PORT: new URL(accessDatabaseUrl).port || "5434",
  SETA_COMPOSE_PREFIX: containerPrefix,
};

interface CommandResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

function command(file: string, args: string[], input?: string): Promise<CommandResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(file, args, { env: commandEnvironment });
    const stdout: Buffer[] = [];
    const stderr: Buffer[] = [];
    child.stdout.on("data", (chunk: Buffer) => stdout.push(chunk));
    child.stderr.on("data", (chunk: Buffer) => stderr.push(chunk));
    child.on("error", reject);
    child.on("close", (exitCode) =>
      resolve({
        stdout: Buffer.concat(stdout).toString("utf8"),
        stderr: Buffer.concat(stderr).toString("utf8"),
        exitCode: exitCode ?? -1,
      }),
    );
    if (input !== undefined) child.stdin.end(input);
    else child.stdin.end();
  });
}

async function dockerCompose(...args: string[]): Promise<CommandResult> {
  const result = await command("docker", ["compose", "-p", project, "-f", composeFile, ...args]);
  if (result.exitCode !== 0) {
    throw new Error(`docker compose ${args.join(" ")} failed: ${result.stderr || result.stdout}`);
  }
  return result;
}

async function waitForKafka(): Promise<void> {
  const startedAt = Date.now();
  while (Date.now() - startedAt < 60_000) {
    const result = await command("docker", [
      "exec",
      `${containerPrefix}-kafka`,
      "/opt/kafka/bin/kafka-topics.sh",
      "--bootstrap-server",
      "kafka:9092",
      "--list",
    ]);
    if (result.exitCode === 0 && result.stdout.includes("media-processing.v1")) return;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error("Kafka did not become ready with the media topic");
}

async function publishMainRecord(key: string, payload: string): Promise<void> {
  const result = await command(
    "docker",
    [
      "exec",
      "-i",
      `${containerPrefix}-kafka`,
      "/opt/kafka/bin/kafka-console-producer.sh",
      "--bootstrap-server",
      "kafka:9092",
      "--topic",
      "media-processing.v1",
      "--property",
      "parse.key=true",
      "--property",
      "key.separator=|",
    ],
    `${key}|${payload}\n`,
  );
  if (result.exitCode !== 0) throw new Error(`Kafka publish failed: ${result.stderr || result.stdout}`);
}

describe.skipIf(!enabled)("KAN-87 media delivery fault recovery", () => {
  let app: FastifyInstance;
  let assetDb: Client;
  let image: Buffer;
  let checksum: string;

  beforeAll(async () => {
    image = await readFile(path.resolve(__dirname, "../../../asset-core/testdata/media/valid/small-64x64.png"));
    checksum = createHash("sha256").update(image).digest("base64");
    assetDb = new Client({
      connectionString: assetDatabaseUrl,
    });
    await assetDb.connect();
    app = await buildServer();
    await app.ready();
  });

  beforeEach(async () => {
    mockCanDo.mockReset();
    mockCanDo.mockResolvedValue({ allowed: true, reason: null });
    mockFilterAllowedResourceIds.mockReset();
    const redis = getRedisClient();
    const keys = await redis.keys("media:session-create:*");
    if (keys.length > 0) await redis.del(...keys);
  });

  afterEach(async () => {
    await dockerCompose("up", "-d", "kafka", "minio", "minio-init");
    await waitForKafka();
    await dockerCompose("up", "-d", "media-worker");
  });

  afterAll(async () => {
    if (assetDb) {
      const owned = "SELECT id FROM metadata_items WHERE title LIKE 'E2E-KAN87:%'";
      await assetDb.query(
        "UPDATE metadata_items SET active_media_version_id = NULL, pending_media_version_id = NULL WHERE title LIKE 'E2E-KAN87:%'",
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
      await assetDb.query("DELETE FROM metadata_items WHERE title LIKE 'E2E-KAN87:%'");
    }
    await app?.close();
    await closeRedisClient();
    await prisma.$disconnect();
    await assetDb?.end();
  });

  async function createTransferredUpload(label: string): Promise<{ assetId: string; uploadId: string }> {
    const assetId = randomUUID();
    await assetDb.query("INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES ($1, $2, $3, $4)", [
      assetId,
      folderId,
      `E2E-KAN87: ${label} ${assetId}`,
      userId,
    ]);
    const created = await app.inject({
      method: "POST",
      url: `/api/v1/assets/${assetId}/media/uploads`,
      headers: { "x-user-id": userId, "x-org-id": orgId, "idempotency-key": randomUUID() },
      payload: {
        filename: "small-64x64.png",
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
    const transferred = await fetch(session.upload.url, {
      method: session.upload.method,
      headers: session.upload.headers,
      body: Uint8Array.from(image).buffer,
    });
    expect(transferred.ok).toBe(true);
    return { assetId, uploadId: session.uploadId };
  }

  async function commit(assetId: string, uploadId: string): Promise<string> {
    const response = await app.inject({
      method: "PUT",
      url: `/api/v1/assets/${assetId}/media`,
      headers: { "x-user-id": userId, "x-org-id": orgId },
      payload: { uploadId },
    });
    expect(response.statusCode).toBe(202);
    return response.json().data.jobId as string;
  }

  async function waitForTerminal(assetId: string, jobId: string): Promise<Record<string, unknown>> {
    const startedAt = Date.now();
    while (Date.now() - startedAt < terminalBudgetMs) {
      const response = await app.inject({
        method: "GET",
        url: `/api/v1/assets/${assetId}/media/status`,
        headers: { "x-user-id": userId, "x-org-id": orgId },
      });
      if (response.statusCode === 200) {
        const status = response.json().data as Record<string, unknown>;
        if (status.jobId === jobId && (status.status === "COMPLETED" || status.status === "FAILED")) return status;
      }
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    throw new Error(`job ${jobId} did not reach a terminal state`);
  }

  async function assertSingleActivation(assetId: string, jobId: string): Promise<void> {
    const result = await assetDb.query<{
      job_count: string;
      version_count: string;
      output_count: string;
      active_matches: boolean;
    }>(
      `SELECT
         (SELECT count(*) FROM media_processing_jobs WHERE asset_id = $1)::text AS job_count,
         (SELECT count(*) FROM asset_media_versions WHERE asset_id = $1)::text AS version_count,
         (SELECT count(*) FROM media_outputs WHERE version_id = jobs.version_id)::text AS output_count,
         EXISTS (
           SELECT 1 FROM metadata_items
           WHERE id = $1 AND active_media_version_id = jobs.version_id AND pending_media_version_id IS NULL
         ) AS active_matches
       FROM media_processing_jobs AS jobs WHERE jobs.id = $2`,
      [assetId, jobId],
    );
    expect(result.rows[0]).toMatchObject({
      job_count: "1",
      version_count: "1",
      output_count: "2",
      active_matches: true,
    });
  }

  test(
    "durable acceptance survives Kafka being unavailable and completes without recommit",
    async () => {
      const upload = await createTransferredUpload("kafka-outage");
      await dockerCompose("stop", "media-worker", "kafka");
      const jobId = await commit(upload.assetId, upload.uploadId);

      const durable = await assetDb.query<{ status: string; pending: string }>(
        `SELECT jobs.status,
           (SELECT count(*) FROM media_job_outbox WHERE job_id = jobs.id AND status IN ('pending', 'publishing'))::text AS pending
         FROM media_processing_jobs AS jobs WHERE jobs.id = $1`,
        [jobId],
      );
      expect(durable.rows[0]).toEqual({ status: "queued", pending: "1" });

      await dockerCompose("up", "-d", "kafka");
      await waitForKafka();
      await dockerCompose("up", "-d", "media-worker");
      expect((await waitForTerminal(upload.assetId, jobId)).status).toBe("COMPLETED");
      await assertSingleActivation(upload.assetId, jobId);
    },
    terminalBudgetMs + 60_000,
  );

  test(
    "reconciliation repairs a missing notification and duplicate delivery has one effect",
    async () => {
      const missing = await createTransferredUpload("missing-notification");
      await dockerCompose("stop", "media-worker");
      const missingJobId = await commit(missing.assetId, missing.uploadId);
      await assetDb.query(
        `UPDATE media_job_outbox
         SET status = 'published', published_at = statement_timestamp() - interval '61 seconds', updated_at = statement_timestamp()
         WHERE job_id = $1`,
        [missingJobId],
      );
      await dockerCompose("up", "-d", "media-worker");
      expect((await waitForTerminal(missing.assetId, missingJobId)).status).toBe("COMPLETED");
      const repaired = await assetDb.query<{ count: string }>(
        "SELECT count(*)::text AS count FROM media_job_outbox WHERE job_id = $1",
        [missingJobId],
      );
      expect(Number(repaired.rows[0].count)).toBeGreaterThanOrEqual(2);
      await assertSingleActivation(missing.assetId, missingJobId);

      const duplicate = await createTransferredUpload("duplicate-delivery");
      await dockerCompose("stop", "media-worker");
      const duplicateJobId = await commit(duplicate.assetId, duplicate.uploadId);
      const payload = await assetDb.query<{ payload: Record<string, unknown> }>(
        "UPDATE media_job_outbox SET status = 'published', published_at = statement_timestamp() WHERE job_id = $1 RETURNING payload",
        [duplicateJobId],
      );
      await dockerCompose("up", "-d", "media-worker");
      const serialized = JSON.stringify(payload.rows[0].payload);
      await publishMainRecord(duplicateJobId, serialized);
      await publishMainRecord(duplicateJobId, serialized);
      expect((await waitForTerminal(duplicate.assetId, duplicateJobId)).status).toBe("COMPLETED");
      const attempts = await assetDb.query<{ attempt_count: number }>(
        "SELECT attempt_count FROM media_processing_jobs WHERE id = $1",
        [duplicateJobId],
      );
      expect(attempts.rows[0].attempt_count).toBe(1);
      await assertSingleActivation(duplicate.assetId, duplicateJobId);
    },
    2 * terminalBudgetMs + 60_000,
  );

  test(
    "an expired claimed job is recovered and a transient storage outage stops after three attempts",
    async () => {
      const recovered = await createTransferredUpload("expired-lease");
      await dockerCompose("stop", "media-worker");
      const recoveredJobId = await commit(recovered.assetId, recovered.uploadId);
      await assetDb.query(
        `UPDATE media_processing_jobs
         SET status = 'processing', stage = 'transforming', attempt_count = 1,
             lease_owner = 'crashed-worker', lease_expires_at = statement_timestamp() - interval '1 second'
         WHERE id = $1`,
        [recoveredJobId],
      );
      await assetDb.query(
        `UPDATE media_job_outbox
         SET status = 'published', published_at = statement_timestamp() - interval '61 seconds'
         WHERE job_id = $1`,
        [recoveredJobId],
      );
      await dockerCompose("up", "-d", "media-worker");
      expect((await waitForTerminal(recovered.assetId, recoveredJobId)).status).toBe("COMPLETED");
      const recoveredAttempts = await assetDb.query<{ attempt_count: number }>(
        "SELECT attempt_count FROM media_processing_jobs WHERE id = $1",
        [recoveredJobId],
      );
      expect(recoveredAttempts.rows[0].attempt_count).toBe(2);
      await assertSingleActivation(recovered.assetId, recoveredJobId);

      const exhausted = await createTransferredUpload("transient-storage");
      await dockerCompose("stop", "media-worker");
      const exhaustedJobId = await commit(exhausted.assetId, exhausted.uploadId);
      await assetDb.query(
        "UPDATE media_processing_jobs SET next_attempt_at = statement_timestamp() + interval '8 seconds' WHERE id = $1",
        [exhaustedJobId],
      );
      await assetDb.query(
        "UPDATE media_job_outbox SET next_attempt_at = statement_timestamp() + interval '8 seconds' WHERE job_id = $1",
        [exhaustedJobId],
      );
      await dockerCompose("up", "-d", "media-worker");
      await dockerCompose("stop", "minio");
      expect((await waitForTerminal(exhausted.assetId, exhaustedJobId)).status).toBe("FAILED");
      const attempts = await assetDb.query<{ attempt_count: number; status: string; retry_delays: number[] }>(
        `SELECT jobs.attempt_count, jobs.status,
           ARRAY(
             SELECT round(EXTRACT(EPOCH FROM (outbox.next_attempt_at - outbox.created_at)))::int
             FROM media_job_outbox AS outbox WHERE outbox.job_id = jobs.id
             ORDER BY outbox.created_at
           ) AS retry_delays
         FROM media_processing_jobs AS jobs WHERE jobs.id = $1`,
        [exhaustedJobId],
      );
      expect(attempts.rows[0].attempt_count).toBe(3);
      expect(attempts.rows[0].status).toBe("failed");
      expect(attempts.rows[0].retry_delays.slice(-2)).toEqual([2, 10]);
    },
    2 * terminalBudgetMs + 60_000,
  );

  test(
    "poison is isolated once, never auto-replays, and privileged replay rebuilds database truth",
    async () => {
      const upload = await createTransferredUpload("poison-replay");
      await dockerCompose("stop", "media-worker");
      const jobId = await commit(upload.assetId, upload.uploadId);
      await assetDb.query(
        "UPDATE media_job_outbox SET status = 'published', published_at = statement_timestamp() WHERE job_id = $1",
        [jobId],
      );
      await dockerCompose("up", "-d", "media-worker");

      const poison = JSON.stringify({
        eventId: randomUUID(),
        eventType: "media.processing.requested",
        schemaVersion: 99,
        source: "asset-core",
        occurredAt: new Date().toISOString(),
        orgId,
        assetId: upload.assetId,
        uploadId: upload.uploadId,
        versionId: randomUUID(),
        jobId,
      });
      await publishMainRecord(jobId, poison);

      const isolationStartedAt = Date.now();
      let isolatedAt: string | null = null;
      while (Date.now() - isolationStartedAt < 30_000) {
        const isolated = await assetDb.query<{ notification_isolated_at: string | null }>(
          "SELECT notification_isolated_at FROM media_processing_jobs WHERE id = $1",
          [jobId],
        );
        isolatedAt = isolated.rows[0].notification_isolated_at;
        if (isolatedAt) break;
        await new Promise((resolve) => setTimeout(resolve, 250));
      }
      expect(isolatedAt).toBeTruthy();
      await new Promise((resolve) => setTimeout(resolve, 1_000));
      const untouched = await assetDb.query<{ status: string; attempt_count: number; outbox_count: string }>(
        `SELECT jobs.status, jobs.attempt_count,
           (SELECT count(*) FROM media_job_outbox WHERE job_id = jobs.id)::text AS outbox_count
         FROM media_processing_jobs AS jobs WHERE jobs.id = $1`,
        [jobId],
      );
      expect(untouched.rows[0]).toEqual({ status: "failed", attempt_count: 0, outbox_count: "1" });

      const dlq = await command("docker", [
        "exec",
        `${containerPrefix}-kafka`,
        "/opt/kafka/bin/kafka-console-consumer.sh",
        "--bootstrap-server",
        "kafka:9092",
        "--topic",
        "media-processing-dlq.v1",
        "--from-beginning",
        "--timeout-ms",
        "5000",
      ]);
      const records = dlq.stdout
        .split("\n")
        .filter(Boolean)
        .map((line) => JSON.parse(line) as Record<string, unknown>);
      const record = records.find((candidate) => candidate.jobId === jobId);
      expect(record).toMatchObject({ jobId, reasonCode: "UNSUPPORTED_SCHEMA_VERSION" });
      expect(JSON.stringify(record)).not.toContain(poison);

      const replay = await dockerCompose(
        "run",
        "--rm",
        "-e",
        "ASSET_MEDIA_REPLAY_AUTHORIZATION=seta-media-local-replay-token",
        "media-worker",
        "replay-dead-letter",
        "--quarantine-id",
        String(record?.quarantineId),
        "--job-id",
        jobId,
        "--operator-id",
        userId,
      );
      expect(replay.stdout + replay.stderr).toContain("media dead-letter replayed");
      expect((await waitForTerminal(upload.assetId, jobId)).status).toBe("COMPLETED");
      await assertSingleActivation(upload.assetId, jobId);
    },
    terminalBudgetMs + 90_000,
  );
});

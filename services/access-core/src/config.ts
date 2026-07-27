import { config as loadEnv } from "dotenv";
import path from "node:path";

loadEnv({ path: path.resolve(__dirname, "../../../.env") });

const dbHost = process.env.ACCESS_DB_HOST ?? "localhost";
const dbPort = process.env.ACCESS_DB_PORT ?? "5434";
const dbName = process.env.ACCESS_DB_NAME ?? "access_db";
const dbUser = process.env.ACCESS_DB_USER ?? "access_user";
const dbPassword = process.env.ACCESS_DB_PASSWORD ?? "access_password";
const redisHost = process.env.ACCESS_REDIS_HOST ?? "localhost";
const redisPort = process.env.ACCESS_REDIS_PORT ?? "6379";

function intInRange(
  name: string,
  fallback: number,
  min: number,
  max: number,
): number {
  const raw = process.env[name];
  if (raw === undefined) return fallback;
  if (!/^\d+$/.test(raw)) {
    throw new Error(
      `${name} must be a whole integer between ${min} and ${max}`,
    );
  }
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < min || value > max) {
    throw new Error(
      `${name} must be a whole integer between ${min} and ${max}`,
    );
  }
  return value;
}

function boolEnv(name: string, fallback: boolean): boolean {
  const raw = process.env[name];
  if (raw === undefined) return fallback;
  const normalized = raw.toLowerCase();
  if (normalized === "true") return true;
  if (normalized === "false") return false;
  throw new Error(`${name} must be either true or false`);
}

export const ASSET_FETCH_TIMEOUT_MS = 3000;
const RESET_TIMEOUT_SAFETY_MARGIN_MS = 500;

function validateResetTimeoutMs(resetTimeoutMs: number): number {
  const minRequired = ASSET_FETCH_TIMEOUT_MS + RESET_TIMEOUT_SAFETY_MARGIN_MS;
  if (resetTimeoutMs < minRequired) {
    throw new Error(
      `ACCESS_ASSET_BREAKER_RESET_MS (${resetTimeoutMs}) must be at least ${minRequired}ms — ` +
        `greater than the asset fetch deadline (${ASSET_FETCH_TIMEOUT_MS}ms) plus a ` +
        `${RESET_TIMEOUT_SAFETY_MARGIN_MS}ms safety margin — so every call admitted before the ` +
        `breaker opened is guaranteed to have settled before the breaker can transition to ` +
        `half-open; otherwise a stale pre-open success could incorrectly close recovery`,
    );
  }
  return resetTimeoutMs;
}

if (!process.env.DATABASE_URL) {
  process.env.DATABASE_URL = `postgresql://${dbUser}:${dbPassword}@${dbHost}:${dbPort}/${dbName}`;
}

export const config = {
  goAssetUrl: process.env.GO_ASSET_URL || "http://localhost:8080",
  assetInternalApiToken: process.env.ASSET_INTERNAL_API_TOKEN ?? "",
  port: parseInt(process.env.PORT ?? "4000", 10),
  host: process.env.HOST ?? "0.0.0.0",
  assetBreaker: {
    enabled: boolEnv("ACCESS_ASSET_BREAKER_ENABLED", true),
    errorThresholdPercentage: intInRange(
      "ACCESS_ASSET_BREAKER_ERROR_THRESHOLD_PCT",
      50,
      1,
      99,
    ),
    volumeThreshold: intInRange(
      "ACCESS_ASSET_BREAKER_VOLUME_THRESHOLD",
      10,
      1,
      1000,
    ),
    resetTimeoutMs: validateResetTimeoutMs(
      intInRange("ACCESS_ASSET_BREAKER_RESET_MS", 5000, 500, 120000),
    ),
    capacity: intInRange("ACCESS_ASSET_BREAKER_CAPACITY", 50, 1, 10000),
  },
  db: {
    host: dbHost,
    port: parseInt(dbPort, 10),
    database: dbName,
    user: dbUser,
    password: dbPassword,
  },
  redis: {
    host: redisHost,
    port: parseInt(redisPort, 10),
    password: process.env.ACCESS_REDIS_PASSWORD || undefined,
    db: parseInt(process.env.ACCESS_REDIS_DB ?? "0", 10),
    connectTimeoutMs: parseInt(
      process.env.ACCESS_REDIS_CONNECT_TIMEOUT_MS ?? "250",
      10,
    ),
    commandTimeoutMs: parseInt(
      process.env.ACCESS_REDIS_COMMAND_TIMEOUT_MS ?? "75",
      10,
    ),
  },
} as const;

export function assertRuntimeConfig(): void {
  if (!config.assetInternalApiToken.trim()) {
    throw new Error(
      "ASSET_INTERNAL_API_TOKEN must be configured before Access Core starts",
    );
  }
}

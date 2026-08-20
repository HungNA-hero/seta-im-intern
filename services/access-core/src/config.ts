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

function intInRange(name: string, fallback: number, min: number, max: number): number {
  const raw = process.env[name];
  if (raw === undefined) return fallback;
  if (!/^\d+$/.test(raw)) {
    throw new Error(`${name} must be a whole integer between ${min} and ${max}`);
  }
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < min || value > max) {
    throw new Error(`${name} must be a whole integer between ${min} and ${max}`);
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

function listEnv(name: string, fallback: readonly string[]): string[] {
  const raw = process.env[name];
  if (raw === undefined) return [...fallback];
  return raw
    .split(",")
    .map((value) => value.trim())
    .filter(Boolean);
}

export function validateMediaTransportConfig(nodeEnvironment: string, requireHTTPS: boolean): void {
  if (nodeEnvironment === "production" && !requireHTTPS) {
    throw new Error("ACCESS_MEDIA_REQUIRE_HTTPS cannot be disabled in production");
  }
}

export const ASSET_FETCH_TIMEOUT_MS = 3000;
export const MEDIA_ASSET_FETCH_TIMEOUT_MS = intInRange(
  "ACCESS_MEDIA_FETCH_TIMEOUT_MS",
  ASSET_FETCH_TIMEOUT_MS,
  1000,
  119500,
);
const RESET_TIMEOUT_SAFETY_MARGIN_MS = 500;

function validateResetTimeoutMs(name: string, resetTimeoutMs: number, requestTimeoutMs: number): number {
  const minRequired = requestTimeoutMs + RESET_TIMEOUT_SAFETY_MARGIN_MS;
  if (resetTimeoutMs < minRequired) {
    throw new Error(
      `${name} (${resetTimeoutMs}) must be at least ${minRequired}ms — ` +
        `greater than the request deadline (${requestTimeoutMs}ms) plus a ` +
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
  metricsEnabled: boolEnv("METRICS_ENABLED", false),
  mediaUploadEnabled: boolEnv("ACCESS_MEDIA_UPLOAD_ENABLED", false),
  mediaRequireHTTPS: boolEnv("ACCESS_MEDIA_REQUIRE_HTTPS", true),
  trustedProxies: listEnv("ACCESS_TRUSTED_PROXIES", ["127.0.0.1", "::1"]),
  mediaAllowedOrigins: listEnv("ACCESS_MEDIA_CORS_ALLOW_ORIGINS", ["http://localhost:3000", "http://localhost:4000"]),
  mediaSessionRateLimit: {
    userPerMinute: intInRange("ACCESS_MEDIA_SESSION_LIMIT_PER_USER_PER_MINUTE", 10, 1, 10000),
    organizationPerMinute: intInRange("ACCESS_MEDIA_SESSION_LIMIT_PER_ORG_PER_MINUTE", 60, 1, 100000),
  },
  assetBreaker: {
    enabled: boolEnv("ACCESS_ASSET_BREAKER_ENABLED", true),
    requestTimeoutMs: ASSET_FETCH_TIMEOUT_MS,
    errorThresholdPercentage: intInRange("ACCESS_ASSET_BREAKER_ERROR_THRESHOLD_PCT", 50, 1, 99),
    volumeThreshold: intInRange("ACCESS_ASSET_BREAKER_VOLUME_THRESHOLD", 10, 1, 1000),
    resetTimeoutMs: validateResetTimeoutMs(
      "ACCESS_ASSET_BREAKER_RESET_MS",
      intInRange("ACCESS_ASSET_BREAKER_RESET_MS", 5000, 500, 120000),
      ASSET_FETCH_TIMEOUT_MS,
    ),
    capacity: intInRange("ACCESS_ASSET_BREAKER_CAPACITY", 50, 1, 10000),
  },
  mediaBreaker: {
    enabled: boolEnv("ACCESS_MEDIA_BREAKER_ENABLED", true),
    requestTimeoutMs: MEDIA_ASSET_FETCH_TIMEOUT_MS,
    errorThresholdPercentage: intInRange("ACCESS_MEDIA_BREAKER_ERROR_THRESHOLD_PCT", 50, 1, 99),
    volumeThreshold: intInRange("ACCESS_MEDIA_BREAKER_VOLUME_THRESHOLD", 10, 1, 1000),
    resetTimeoutMs: validateResetTimeoutMs(
      "ACCESS_MEDIA_BREAKER_RESET_MS",
      intInRange("ACCESS_MEDIA_BREAKER_RESET_MS", 5000, 500, 120000),
      MEDIA_ASSET_FETCH_TIMEOUT_MS,
    ),
    capacity: intInRange("ACCESS_MEDIA_BREAKER_CAPACITY", 50, 1, 10000),
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
    connectTimeoutMs: parseInt(process.env.ACCESS_REDIS_CONNECT_TIMEOUT_MS ?? "250", 10),
    commandTimeoutMs: parseInt(process.env.ACCESS_REDIS_COMMAND_TIMEOUT_MS ?? "75", 10),
  },
} as const;

export function assertRuntimeConfig(): void {
  if (!config.assetInternalApiToken.trim()) {
    throw new Error("ASSET_INTERNAL_API_TOKEN must be configured before Access Core starts");
  }
  validateMediaTransportConfig(process.env.NODE_ENV ?? "development", config.mediaRequireHTTPS);
}

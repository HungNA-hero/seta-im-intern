import { config as loadEnv } from "dotenv";
import path from "node:path";

loadEnv({ path: path.resolve(__dirname, "../../../.env") });

const dbHost = process.env.ACCESS_DB_HOST ?? "localhost";
const dbPort = process.env.ACCESS_DB_PORT ?? "5434";
const dbName = process.env.ACCESS_DB_NAME ?? "access_db";
const dbUser = process.env.ACCESS_DB_USER ?? "access_user";
const dbPassword = process.env.ACCESS_DB_PASSWORD ?? "access_password";

if (!process.env.DATABASE_URL) {
  process.env.DATABASE_URL = `postgresql://${dbUser}:${dbPassword}@${dbHost}:${dbPort}/${dbName}`;
}

export const config = {
  goAssetUrl: process.env.GO_ASSET_URL || "http://localhost:8080",
  port: parseInt(process.env.PORT ?? "4000", 10),
  host: process.env.HOST ?? "0.0.0.0",
  db: {
    host: dbHost,
    port: parseInt(dbPort, 10),
    database: dbName,
    user: dbUser,
    password: dbPassword,
  },
} as const;

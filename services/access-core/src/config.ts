import { config as loadEnv } from 'dotenv';
loadEnv();

const dbHost     = process.env.ACCESS_DB_HOST     ?? 'localhost';
const dbPort     = process.env.ACCESS_DB_PORT     ?? '5434';
const dbName     = process.env.ACCESS_DB_NAME     ?? 'access_db';
const dbUser     = process.env.ACCESS_DB_USER     ?? 'access_user';
const dbPassword = process.env.ACCESS_DB_PASSWORD ?? 'access_password';

// Build DATABASE_URL from individual vars if not already set (used by Prisma).
if (!process.env.DATABASE_URL) {
  process.env.DATABASE_URL = `postgresql://${dbUser}:${dbPassword}@${dbHost}:${dbPort}/${dbName}`;
}

export const config = {
  port: parseInt(process.env.PORT ?? '4000', 10),
  host: process.env.HOST ?? '0.0.0.0',
} as const;

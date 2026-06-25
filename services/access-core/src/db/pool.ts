import { Pool } from 'pg';
import { config } from '../config';

export const pool = new Pool({
  host:     config.db.host,
  port:     config.db.port,
  database: config.db.database,
  user:     config.db.user,
  password: config.db.password,
});

pool.on('error', (err) => {
  console.error('Unexpected pg pool error', err);
  process.exit(1);
});

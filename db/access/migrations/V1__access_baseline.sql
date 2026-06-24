CREATE EXTENSION IF NOT EXISTS pgcrypto;

COMMENT ON SCHEMA public IS
    'Access DB owned by Node Access Policy Service. RBAC and object permission tables are implemented in KAN-19.';

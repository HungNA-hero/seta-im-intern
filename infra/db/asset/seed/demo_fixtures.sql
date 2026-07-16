-- Demo fixture data for local development and the trainer/sprint demo scripts.
-- Not a Flyway migration: apply manually after `flyway migrate`, and after
-- infra/db/access/seed/demo_fixtures.sql (this file's org/user rows mirror IDs
-- seeded there).
--
-- Apply with:
--   docker exec -i seta-asset-db psql -U asset_user -d asset_db < infra/db/asset/seed/demo_fixtures.sql

INSERT INTO organization_ref (org_id)
VALUES ('00000000-0000-0000-0000-000000000010')
ON CONFLICT (org_id) DO NOTHING;

INSERT INTO user_ref (user_id)
VALUES ('00000000-0000-0000-0000-000000000001')
ON CONFLICT (user_id) DO NOTHING;

INSERT INTO folders (id, org_id, path, name, description, created_by, updated_by)
VALUES (
    '10000000-0000-0000-0000-000000000000',
    '00000000-0000-0000-0000-000000000010',
    'root',
    'Root',
    'Demo root folder for local development.',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO folders (id, org_id, path, name, description, created_by, updated_by)
VALUES (
    '10000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000010',
    'root.animals',
    'Animals',
    'Demo folder matching the architecture example.',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO folders (id, org_id, path, name, description, created_by, updated_by)
VALUES (
    '10000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000010',
    'root.animals.dogs',
    'Dogs',
    'Demo child folder for tree and metadata checks.',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO metadata_items (
    id,
    folder_id,
    title,
    description,
    labels,
    category,
    external_source,
    external_id,
    source_url,
    thumbnail_url,
    license,
    author,
    metadata_json,
    notes,
    created_by,
    updated_by
)
VALUES (
    '20000000-0000-0000-0000-000000000001',
    '10000000-0000-0000-0000-000000000002',
    'Demo dog metadata',
    'Seed metadata item used to verify folder placement and import identity.',
    ARRAY['dog', 'animal'],
    'animal',
    'open_images_v7',
    'demo-dog-001',
    'https://storage.googleapis.com/openimages/web/index.html',
    NULL,
    'demo',
    'Open Images',
    '{"source": "demo"}'::jsonb,
    'Local seed data only.',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001'
)
ON CONFLICT (id) DO NOTHING;

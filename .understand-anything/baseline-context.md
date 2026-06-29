# Seta IM Intern - Baseline Context
This document serves as the baseline context for Phase 1.

## Project Statistics
- **Total Files Scanned:** 53

## Architecture Grouping
- `services`: 30 files
- `infra`: 18 files
- `documentation`: 1 files
- `config`: 1 files
- `root`: 1 files
- `.understand-anything`: 2 files

## Data Pipeline
### Schemas
- `services/access-core/prisma/schema.prisma`
- `infra/db/asset/migrations/V1__asset_initial_schema.sql`
- `infra/db/access/migrations/V1__create_access_schema.sql`
- `infra/db/access/migrations/V2__seed_access_schema.sql`

### Models
- `services/asset-core/internal/domain/asset.go`

## Deployment Topology
- hasDockerfile: False
- hasCompose: True
- hasK8s: False
- hasTerraform: False
- hasCI: False

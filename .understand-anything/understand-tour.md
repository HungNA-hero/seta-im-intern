# Seta IM Intern - Understand Anything Tour

Welcome to the Codebase Tour! This document provides an automated walkthrough of the architecture, key modules, and patterns in this repository.

## High Level Architecture
Based on the structural analysis, the codebase is grouped into the following primary directories:

- **`services`** (Pattern: service, Files: 30)
- **`infra`** (Pattern: infrastructure, Files: 18)
- **`documentation`** (Pattern: documentation, Files: 1)
- **`config`** (Pattern: config, Files: 1)

## Key Modules and Entry Points

### `services/asset-core/cmd/server/main.go`
**Summary:** Khởi động Asset Core: kết nối PostgreSQL qua GORM, ghép các tầng repository/use case/HTTP handler và phục vụ API nội bộ.
- **Tags:** entry-point, http-server, dependency-injection, database

### `services/access-core/src/index.ts`
**Summary:** Entry point của Access Core: kiểm tra kết nối Prisma, dựng Fastify server và lắng nghe theo cấu hình runtime.
- **Tags:** entry-point, bootstrap, database-healthcheck, http-server

## Data Pipeline & Topology
### Schema & Migrations
- `infra/db/access/migrations/V1__create_access_schema.sql`
- `infra/db/asset/migrations/V1__asset_initial_schema.sql`
- `infra/db/access/migrations/V2__seed_access_schema.sql`
- `infra/db/asset/migrations/V2__asset_seed_demo_tree.sql`
- `services/access-core/prisma/schema.prisma`

### Deployment Topology
- Has Dockerfile: False
- Has Docker Compose: True

## Complex Files (May need refactoring)
- **`services/asset-core/internal/delivery/http/middleware_test.go`**: Bao phủ các trường hợp header actor hợp lệ, thiếu và malformed bằng table-driven test.
- **`infra/db/access/migrations/V1__create_access_schema.sql`**: Khởi tạo schema `access` cho danh tính, tổ chức và mô hình phân quyền nhiều tầng, gồm RBAC theo vai trò và quyền trên từng đối tượng. Migration cũng định nghĩa enum, trigger cập nhật thời gian, ràng buộc toàn vẹn và các index phục vụ truy vấn quyền.
- **`infra/db/access/migrations/V1__create_access_schema.sql`**: Lưu quyền trực tiếp trên từng đối tượng cho người dùng hoặc vai trò trong một tổ chức. Check constraint buộc chỉ có đúng một loại bên nhận quyền, còn partial unique index ngăn cấp trùng.
- **`infra/db/asset/migrations/V1__asset_initial_schema.sql`**: Khởi tạo cơ sở dữ liệu Asset Core cho cây thư mục và bản ghi metadata, đồng thời tạo bảng tham chiếu mỏng tới danh tính thuộc Access DB. Migration dùng `ltree`, soft delete, audit fields, trigger và index chuyên biệt để bảo vệ cấu trúc cây và tối ưu truy vấn.
- **`infra/db/asset/migrations/V1__asset_initial_schema.sql`**: Lưu cây thư mục theo tenant bằng đường dẫn `ltree`, kèm thông tin mô tả, audit và soft delete. Các partial unique index giữ duy nhất đường dẫn và tên anh em còn hiệu lực.
- **`infra/db/asset/migrations/V1__asset_initial_schema.sql`**: Lưu metadata văn bản của asset trong từng thư mục, gồm nhãn, nguồn ngoài, giấy phép, tác giả, JSON mở rộng và thông tin audit. Ràng buộc bảo đảm cặp định danh nguồn ngoài đầy đủ, còn trigger cấm gắn item vào thư mục đã soft delete.

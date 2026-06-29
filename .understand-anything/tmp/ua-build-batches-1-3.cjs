const fs = require('fs');
const path = require('path');

const root = process.cwd();
const intermediate = path.join(root, '.understand-anything', 'intermediate');
const temp = path.join(root, '.understand-anything', 'tmp');
const batches = JSON.parse(fs.readFileSync(path.join(intermediate, 'batches.json'), 'utf8')).batches;

const fileMeta = {
  'services/asset-core/cmd/server/main.go': ['Khởi động Asset Core: kết nối PostgreSQL qua GORM, ghép các tầng repository/use case/HTTP handler và phục vụ API nội bộ.', ['entry-point', 'http-server', 'dependency-injection', 'database'], 'Go entry point ghép các tầng theo Clean Architecture bằng constructor injection.'],
  'services/asset-core/internal/delivery/http/asset_handler.go': ['Cung cấp HTTP handler cho health check, cây thư mục và metadata item; đồng thời kiểm tra organization context trước khi gọi use case.', ['api-handler', 'http', 'validation', 'asset-service'], 'Các method trên pointer receiver chia sẻ use case và kết nối GORM trong một handler có trạng thái.'],
  'services/asset-core/internal/delivery/http/asset_handler_test.go': ['Kiểm thử endpoint cây thư mục, bảo đảm request khác organization bị từ chối và organization hợp lệ được truyền xuống use case.', ['test', 'http-test', 'authorization', 'organization-scope']],
  'services/asset-core/internal/delivery/http/middleware.go': ['Định nghĩa middleware xác thực hai header actor dưới dạng UUID rồi đưa user/organization vào request context.', ['middleware', 'authentication-context', 'validation', 'http']],
  'services/asset-core/internal/delivery/http/middleware_test.go': ['Kiểm thử theo bảng cho middleware actor với header hợp lệ, thiếu hoặc sai định dạng và xác nhận context truyền xuống handler.', ['test', 'table-driven-test', 'middleware', 'validation'], 'Dùng table-driven tests và subtest, một idiom phổ biến của Go để bao phủ nhiều trường hợp đầu vào.'],
  'services/asset-core/internal/domain/asset.go': ['Định nghĩa entity GORM cho tham chiếu user/organization, cây Folder và MetadataItem, cùng contract repository và use case của Asset Core.', ['data-model', 'domain-model', 'gorm', 'interface'], 'Các struct kết hợp GORM tags với PostgreSQL ltree, JSONB, array và soft delete để ánh xạ mô hình asset.'],
  'services/asset-core/internal/repository/asset_repository.go': ['Hiện thực truy vấn cây thư mục theo organization bằng toán tử PostgreSQL ltree và upsert idempotent các tham chiếu user/organization.', ['repository', 'database', 'gorm', 'ltree']],
  'services/asset-core/internal/repository/asset_repository_test.go': ['Kiểm thử SQL cho thao tác EnsureRefs, gồm gọi lặp idempotent và việc truyền nguyên lỗi khi insert organization hoặc user thất bại.', ['test', 'repository-test', 'sql-mock', 'error-handling']],
  'services/asset-core/internal/requestcontext/context.go': ['Cung cấp kiểu Actor và helper gắn/lấy danh tính user cùng organization từ context của request.', ['request-context', 'authentication-context', 'utility', 'error-handling']],
  'services/asset-core/internal/usecase/asset_usecase.go': ['Hiện thực tầng use case mỏng cho Asset Core, ủy quyền truy vấn cây thư mục và đồng bộ tham chiếu cho repository contract.', ['usecase', 'service-layer', 'dependency-injection', 'delegation']],
  'services/access-core/src/db/queries/roles.ts': ['Cung cấp truy vấn Prisma để liệt kê role theo organization hoặc lấy role theo ID, đồng thời ánh xạ tên cột database sang domain shape.', ['database-query', 'prisma', 'role-management', 'serialization']],
  'services/access-core/src/db/queries/users.ts': ['Cung cấp truy vấn Prisma để liệt kê user hoặc lấy user theo ID và chuẩn hóa các trường snake_case thành domain shape TypeScript.', ['database-query', 'prisma', 'user-management', 'serialization']],
  'services/access-core/src/graphql/resolvers/index.ts': ['Tổng hợp resolver Query cho user, role và permission thành một resolver map dùng chung cho GraphQL schema.', ['barrel', 'graphql-resolver', 'composition', 'query-api']],
  'services/access-core/src/graphql/resolvers/roleResolvers.ts': ['Hiện thực GraphQL resolver cho danh sách và chi tiết role, gọi tầng query rồi chuyển Date thành chuỗi ISO.', ['graphql-resolver', 'role-management', 'serialization', 'api-handler']],
  'services/access-core/src/graphql/resolvers/userResolvers.ts': ['Hiện thực GraphQL resolver cho danh sách và chi tiết user, gọi tầng query rồi chuyển trường thời gian thành chuỗi ISO.', ['graphql-resolver', 'user-management', 'serialization', 'api-handler']],
  'services/access-core/src/graphql/schema.ts': ['Ghép GraphQL type definitions và resolver map thành executable schema bằng GraphQL Yoga.', ['graphql', 'schema-composition', 'api-schema', 'configuration']],
  'services/access-core/src/graphql/typeDefs.ts': ['Khai báo GraphQL schema cho user, role, role permission, object permission và các truy vấn Access Core.', ['graphql', 'schema-definition', 'permission-model', 'api-schema'], 'Schema được giữ trong tagged template string TypeScript để GraphQL Yoga biên dịch lúc khởi động.'],
  'services/access-core/src/server.ts': ['Xây dựng Fastify server, nối GraphQL Yoga tại /graphql và cung cấp health endpoint cho Access Core.', ['http-server', 'fastify', 'graphql-yoga', 'factory'], 'Adapter thủ công chuyển Fastify request/response sang Fetch API mà GraphQL Yoga sử dụng.'],
  'services/access-core/src/config.ts': ['Nạp biến môi trường, dựng DATABASE_URL mặc định cho Prisma và xuất cấu hình host/port của Access Core.', ['configuration', 'environment', 'database', 'runtime-config']],
  'services/access-core/src/db/prisma.ts': ['Khởi tạo PrismaClient trên PostgreSQL adapter và connection pool dùng DATABASE_URL đã được cấu hình.', ['database-client', 'prisma', 'postgresql', 'singleton']],
  'services/access-core/src/db/queries/objectPermissions.ts': ['Truy vấn object permission theo organization, loại resource và resource ID rồi ánh xạ record Prisma sang domain shape.', ['database-query', 'prisma', 'permission-model', 'organization-scope']],
  'services/access-core/src/db/queries/rolePermissions.ts': ['Truy vấn các permission gắn với một role và chuyển record Prisma thành RolePermission shape.', ['database-query', 'prisma', 'role-permission', 'serialization']],
  'services/access-core/src/graphql/resolvers/permissionResolvers.ts': ['Hiện thực GraphQL resolver cho role permission và object permission, bao gồm chuẩn hóa nullable grantee và timestamp ISO.', ['graphql-resolver', 'permission-model', 'api-handler', 'serialization']],
  'services/access-core/src/index.ts': ['Entry point của Access Core: kiểm tra kết nối Prisma, dựng Fastify server và lắng nghe theo cấu hình runtime.', ['entry-point', 'bootstrap', 'database-healthcheck', 'http-server']]
};

function fileComplexity(result) {
  if (result.nonEmptyLines < 50) return 'simple';
  if (result.nonEmptyLines <= 200) return 'moderate';
  return 'complex';
}

function symbolComplexity(start, end) {
  const lines = end - start + 1;
  if (lines <= 20) return 'simple';
  if (lines <= 50) return 'moderate';
  return 'complex';
}

function symbolTags(filePath, name, type) {
  if (/Test/.test(name) || /_test\.go$/.test(filePath)) return ['test', 'unit-test', 'behavior-verification'];
  if (type === 'class' && filePath.includes('/domain/')) return ['domain-model', 'data-contract', 'gorm'];
  if (type === 'class') return ['class', 'dependency-injection', 'service-component'];
  if (/Handler|RequireActor/.test(name)) return ['api-handler', 'http', 'validation'];
  if (/^New/.test(name)) return ['factory', 'dependency-injection', 'constructor'];
  if (/FolderTree|EnsureRefs/.test(name)) return ['business-operation', 'repository', 'asset-service'];
  if (/^list|^get/.test(name)) return ['database-query', 'prisma', 'data-access'];
  if (/^to(Role|User)/.test(name)) return ['serialization', 'graphql-resolver', 'data-mapping'];
  if (name === 'buildServer') return ['factory', 'http-server', 'graphql-yoga'];
  if (name === 'main') return ['entry-point', 'bootstrap', 'orchestration'];
  if (/Actor/.test(name)) return ['request-context', 'authentication-context', 'utility'];
  return ['function', 'business-logic', 'service-component'];
}

function symbolSummary(filePath, name, type) {
  const key = `${filePath}|${name}`;
  const exact = {
    'services/asset-core/cmd/server/main.go|main': 'Điều phối khởi động Asset Core, nối database, repository, use case, HTTP routes và bắt đầu lắng nghe.',
    'services/asset-core/cmd/server/main.go|openAssetDB': 'Mở kết nối GORM PostgreSQL và ping connection pool để xác nhận database sẵn sàng.',
    'services/asset-core/cmd/server/main.go|assetDSNFromEnv': 'Dựng PostgreSQL DSN từ các biến môi trường Asset Core với giá trị mặc định cho local development.',
    'services/asset-core/internal/delivery/http/asset_handler.go|AssetHandler': 'Giữ dependency use case và database cho nhóm HTTP endpoint của Asset Core.',
    'services/asset-core/internal/delivery/http/asset_handler.go|NewAssetHandler': 'Khởi tạo AssetHandler và đăng ký health, folder, metadata routes lên ServeMux.',
    'services/asset-core/internal/delivery/http/asset_handler.go|HandleHealth': 'Trả health status và cho biết dependency database có được khởi tạo hay không.',
    'services/asset-core/internal/delivery/http/asset_handler.go|HandleFolders': 'Xác thực method, query và organization context trước khi trả cây thư mục từ use case.',
    'services/asset-core/internal/delivery/http/asset_handler.go|HandleMetadataItems': 'Giữ chỗ endpoint metadata item và trả phản hồi not implemented có cấu trúc.',
    'services/asset-core/internal/delivery/http/asset_handler_test.go|fakeAssetUsecase': 'Test double ghi nhận organization được handler truyền xuống và trả danh sách folder rỗng.',
    'services/asset-core/internal/delivery/http/asset_handler_test.go|GetFolderTree': 'Ghi nhận lời gọi lấy cây thư mục trong fake use case phục vụ kiểm thử handler.',
    'services/asset-core/internal/delivery/http/asset_handler_test.go|EnsureRefs': 'Cài đặt contract EnsureRefs tối giản cho fake use case trong kiểm thử HTTP.',
    'services/asset-core/internal/delivery/http/asset_handler_test.go|TestHandleFoldersRejectsOrganizationContextMismatch': 'Xác minh handler trả 403 và không gọi use case khi organization trong query khác actor context.',
    'services/asset-core/internal/delivery/http/asset_handler_test.go|TestHandleFoldersUsesMatchingOrganizationContext': 'Xác minh request hợp lệ trả 200 và organization được chuyển đúng xuống use case.',
    'services/asset-core/internal/delivery/http/middleware.go|RequireActor': 'Bao bọc handler bằng kiểm tra X-User-Id/X-Org-Id, validate UUID và gắn Actor vào request context.',
    'services/asset-core/internal/delivery/http/middleware_test.go|TestRequireActor': 'Bao phủ các trường hợp header actor hợp lệ, thiếu và malformed bằng table-driven test.',
    'services/asset-core/internal/domain/asset.go|OrganizationRef': 'Mô hình shadow reference tối giản đến organization thuộc Access DB.',
    'services/asset-core/internal/domain/asset.go|UserRef': 'Mô hình shadow reference tối giản đến user thuộc Access DB.',
    'services/asset-core/internal/domain/asset.go|Folder': 'Mô hình folder phân cấp theo PostgreSQL ltree, có organization scope và soft delete.',
    'services/asset-core/internal/domain/asset.go|MetadataItem': 'Mô hình metadata của asset với nhãn, nguồn ngoài, JSONB và thông tin audit.',
    'services/asset-core/internal/domain/asset.go|AssetRepository': 'Contract cho truy vấn folder tree và bảo đảm shadow reference tồn tại trong database.',
    'services/asset-core/internal/domain/asset.go|AssetUsecase': 'Contract nghiệp vụ mà delivery layer dùng để truy cập folder và đồng bộ reference.',
    'services/asset-core/internal/domain/asset.go|TableName': 'Ánh xạ domain entity sang tên bảng PostgreSQL rõ ràng cho GORM.',
    'services/asset-core/internal/repository/asset_repository.go|assetRepository': 'Repository implementation giữ GORM database handle cho các thao tác Asset Core.',
    'services/asset-core/internal/repository/asset_repository.go|NewAssetRepository': 'Tạo repository implementation từ GORM database và trả về domain contract.',
    'services/asset-core/internal/repository/asset_repository.go|GetFolderTree': 'Truy vấn các descendant folder theo ltree path, organization và trạng thái chưa xóa.',
    'services/asset-core/internal/repository/asset_repository.go|EnsureRefs': 'Upsert idempotent user và organization shadow references, dừng ngay khi database lỗi.',
    'services/asset-core/internal/requestcontext/context.go|Actor': 'Mang danh tính user và organization đã xác thực xuyên suốt request context.',
    'services/asset-core/internal/requestcontext/context.go|WithActor': 'Tạo context con chứa Actor dưới private typed key.',
    'services/asset-core/internal/requestcontext/context.go|GetActor': 'Lấy Actor từ context và trả lỗi rõ ràng khi giá trị chưa được middleware gắn.',
    'services/asset-core/internal/usecase/asset_usecase.go|assetUsecase': 'Use case implementation giữ repository contract của Asset Core.',
    'services/asset-core/internal/usecase/asset_usecase.go|NewAssetUsecase': 'Tạo use case từ repository dependency theo constructor injection.',
    'services/asset-core/internal/usecase/asset_usecase.go|GetFolderTree': 'Ủy quyền truy vấn cây thư mục theo organization và root path cho repository.',
    'services/asset-core/internal/usecase/asset_usecase.go|EnsureRefs': 'Ủy quyền việc bảo đảm user/organization references cho repository.',
    'services/access-core/src/db/queries/roles.ts|listRolesByOrg': 'Lấy toàn bộ role của một organization và ánh xạ record Prisma sang Role shape.',
    'services/access-core/src/db/queries/roles.ts|getRoleById': 'Lấy một role theo ID, trả null khi không tồn tại và chuẩn hóa tên trường.',
    'services/access-core/src/db/queries/users.ts|listUsers': 'Liệt kê user và ánh xạ record Prisma sang User shape dùng trong API.',
    'services/access-core/src/db/queries/users.ts|getUserById': 'Lấy user theo ID, trả null khi không tồn tại và chuẩn hóa trường database.',
    'services/access-core/src/graphql/resolvers/roleResolvers.ts|toRole': 'Chuyển Role domain object thành GraphQL payload với timestamp ISO và nullable description.',
    'services/access-core/src/graphql/resolvers/userResolvers.ts|toUser': 'Chuyển User domain object thành GraphQL payload với tên trường API và timestamp ISO.',
    'services/access-core/src/server.ts|buildServer': 'Tạo Fastify instance, bridge GraphQL Yoga qua Fetch API và đăng ký health endpoint.',
    'services/access-core/src/db/queries/objectPermissions.ts|listObjectPermissions': 'Lọc object permissions theo organization/resource rồi ánh xạ kết quả Prisma sang API shape.',
    'services/access-core/src/db/queries/rolePermissions.ts|listRolePermissions': 'Lấy permissions của role và ánh xạ chúng sang RolePermission shape.',
    'services/access-core/src/index.ts|main': 'Kiểm tra database, dựng server và bắt đầu lắng nghe theo host/port runtime.'
  };
  if (exact[key]) return exact[key];
  if (/^Test/.test(name)) return `Kiểm thử hành vi ${name.replace(/^Test/, '')} và xác nhận kết quả mong đợi.`;
  if (type === 'class') return `Đóng gói trạng thái và hành vi của ${name} trong module.`;
  return `Hiện thực thao tác ${name} của module và cung cấp kết quả cho tầng gọi.`;
}

function addEdge(edges, source, target, type, weight) {
  edges.push({ source, target, type, direction: 'forward', weight });
}

function build(batchIndex) {
  const batch = batches.find(item => item.batchIndex === batchIndex);
  if (!batch) throw new Error(`Missing batch ${batchIndex}`);
  const extraction = JSON.parse(fs.readFileSync(path.join(temp, `ua-file-extract-results-${batchIndex}.json`), 'utf8'));
  if (!extraction.scriptCompleted) throw new Error(`Extraction incomplete for batch ${batchIndex}`);

  const nodes = [];
  const edges = [];
  const selectedIds = new Set();

  for (const result of extraction.results) {
    const meta = fileMeta[result.path];
    if (!meta) throw new Error(`Missing semantic metadata for ${result.path}`);
    const fileId = `file:${result.path}`;
    const fileNode = {
      id: fileId,
      type: 'file',
      name: path.basename(result.path),
      filePath: result.path,
      summary: meta[0],
      tags: meta[1],
      complexity: fileComplexity(result)
    };
    if (meta[2]) fileNode.languageNotes = meta[2];
    nodes.push(fileNode);
    selectedIds.add(fileId);

    const exported = new Set((result.exports || []).map(item => item.name));
    for (const fn of result.functions || []) {
      const significant = exported.has(fn.name) || (fn.endLine - fn.startLine + 1 >= 10);
      if (!significant) continue;
      const id = `function:${result.path}:${fn.name}`;
      if (selectedIds.has(id)) continue;
      nodes.push({
        id,
        type: 'function',
        name: fn.name,
        filePath: result.path,
        lineRange: [fn.startLine, fn.endLine],
        summary: symbolSummary(result.path, fn.name, 'function'),
        tags: symbolTags(result.path, fn.name, 'function'),
        complexity: symbolComplexity(fn.startLine, fn.endLine)
      });
      selectedIds.add(id);
      addEdge(edges, fileId, id, 'contains', 1.0);
      if (exported.has(fn.name)) addEdge(edges, fileId, id, 'exports', 0.8);
    }

    for (const cls of result.classes || []) {
      const significant = exported.has(cls.name) || (cls.methods || []).length >= 2 || (cls.endLine - cls.startLine + 1 >= 20);
      if (!significant) continue;
      const id = `class:${result.path}:${cls.name}`;
      if (selectedIds.has(id)) continue;
      nodes.push({
        id,
        type: 'class',
        name: cls.name,
        filePath: result.path,
        lineRange: [cls.startLine, cls.endLine],
        summary: symbolSummary(result.path, cls.name, 'class'),
        tags: symbolTags(result.path, cls.name, 'class'),
        complexity: symbolComplexity(cls.startLine, cls.endLine)
      });
      selectedIds.add(id);
      addEdge(edges, fileId, id, 'contains', 1.0);
      if (exported.has(cls.name)) addEdge(edges, fileId, id, 'exports', 0.8);
    }
  }

  for (const file of batch.files) {
    for (const target of batch.batchImportData[file.path] || []) {
      addEdge(edges, `file:${file.path}`, `file:${target}`, 'imports', 0.7);
    }
  }

  if (batchIndex === 1) {
    const testedPairs = [
      ['services/asset-core/internal/delivery/http/asset_handler.go', 'services/asset-core/internal/delivery/http/asset_handler_test.go'],
      ['services/asset-core/internal/delivery/http/middleware.go', 'services/asset-core/internal/delivery/http/middleware_test.go'],
      ['services/asset-core/internal/repository/asset_repository.go', 'services/asset-core/internal/repository/asset_repository_test.go']
    ];
    for (const [production, test] of testedPairs) addEdge(edges, `file:${production}`, `file:${test}`, 'tested_by', 0.5);
    const calls = [
      ['function:services/asset-core/cmd/server/main.go:main', 'function:services/asset-core/internal/repository/asset_repository.go:NewAssetRepository'],
      ['function:services/asset-core/cmd/server/main.go:main', 'function:services/asset-core/internal/usecase/asset_usecase.go:NewAssetUsecase'],
      ['function:services/asset-core/cmd/server/main.go:main', 'function:services/asset-core/internal/delivery/http/asset_handler.go:NewAssetHandler'],
      ['function:services/asset-core/internal/delivery/http/asset_handler.go:NewAssetHandler', 'function:services/asset-core/internal/delivery/http/middleware.go:RequireActor'],
      ['function:services/asset-core/internal/delivery/http/asset_handler.go:HandleFolders', 'function:services/asset-core/internal/requestcontext/context.go:GetActor'],
      ['function:services/asset-core/internal/delivery/http/middleware.go:RequireActor', 'function:services/asset-core/internal/requestcontext/context.go:WithActor'],
      ['function:services/asset-core/internal/usecase/asset_usecase.go:GetFolderTree', 'function:services/asset-core/internal/repository/asset_repository.go:GetFolderTree'],
      ['function:services/asset-core/internal/usecase/asset_usecase.go:EnsureRefs', 'function:services/asset-core/internal/repository/asset_repository.go:EnsureRefs']
    ];
    for (const [source, target] of calls) addEdge(edges, source, target, 'calls', 0.8);
  }

  if (batchIndex === 3) {
    addEdge(edges, 'function:services/access-core/src/index.ts:main', 'function:services/access-core/src/server.ts:buildServer', 'calls', 0.8);
  }

  const output = { nodes, edges };
  if (nodes.length > 60 || edges.length > 120) throw new Error(`Batch ${batchIndex} requires split output: ${nodes.length} nodes/${edges.length} edges`);
  fs.writeFileSync(path.join(intermediate, `batch-${batchIndex}.json`), `${JSON.stringify(output, null, 2)}\n`);
  process.stdout.write(`batch-${batchIndex}.json nodes=${nodes.length} edges=${edges.length} skipped=${extraction.filesSkipped.length}\n`);
}

const requested = process.argv.slice(2).map(Number);
for (const batchIndex of requested.length ? requested : [1, 2, 3]) build(batchIndex);

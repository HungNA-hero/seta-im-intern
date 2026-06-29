import test from 'node:test';
import assert from 'node:assert';
import { folderResolvers } from './folderResolvers';
import { GraphQLError } from 'graphql';

test('folderResolvers - missing context throws UNAUTHENTICATED', async () => {
  const context = { request: { headers: new Map() } }; // no x-user-id or x-org-id
  
  try {
    await folderResolvers.Query.folderTree(null, { orgId: 'org-1' }, context);
    assert.fail('Expected error');
  } catch (err: any) {
    assert.strictEqual(err instanceof GraphQLError, true);
    assert.strictEqual(err.extensions.code, 'UNAUTHENTICATED');
  }
});

test('folderResolvers - org mismatch throws FORBIDDEN', async () => {
  const headers = new Map([
    ['x-user-id', 'user-1'],
    ['x-org-id', 'org-1']
  ]);
  const context = { request: { headers } };
  
  try {
    // Requesting data for org-2 while context is org-1
    await folderResolvers.Query.folderTree(null, { orgId: 'org-2' }, context);
    assert.fail('Expected error');
  } catch (err: any) {
    assert.strictEqual(err instanceof GraphQLError, true);
    assert.strictEqual(err.extensions.code, 'FORBIDDEN');
    assert.match(err.message, /Organization mismatch/);
  }
});

test('folderResolvers - policy deny throws FORBIDDEN', async () => {
  const headers = new Map([
    ['x-user-id', 'user-1'],
    ['x-org-id', 'org-1']
  ]);
  const context = {
    request: { headers },
    policyGuard: { async checkFolderAccess() { return false; } }
  };
  
  try {
    await folderResolvers.Query.folderTree(null, { orgId: 'org-1' }, context);
    assert.fail('Expected error');
  } catch (err: any) {
    assert.strictEqual(err instanceof GraphQLError, true);
    assert.strictEqual(err.extensions.code, 'FORBIDDEN');
    assert.match(err.message, /Policy deny/);
  }
});

test('folderResolvers - mapping snake_case to camelCase and exact forwarded headers', async () => {
  const headers = new Map([
    ['x-user-id', 'user-1'],
    ['x-org-id', 'org-1']
  ]);
  const context = {
    request: { headers },
    policyGuard: { async checkFolderAccess() { return true; } }
  };
  
  // Mock fetch
  const originalFetch = global.fetch;
  let fetchUrl = '';
  let fetchHeaders: any = {};
  
  global.fetch = async (url: any, options: any) => {
    fetchUrl = url;
    fetchHeaders = options.headers;
    return {
      ok: true,
      json: async () => ({
        folder: {
          id: 'f-1',
          org_id: 'org-1',
          path: 'root',
          name: 'Root',
          description: null,
          created_by: 'user-1',
          updated_by: null,
          created_at: '2026',
          updated_at: '2026',
          deleted_at: null
        }
      })
    } as any;
  };
  
  try {
    const res = await folderResolvers.Query.folder(null, { orgId: 'org-1', id: 'f-1' }, context);
    assert.strictEqual(res?.id, 'f-1');
    assert.strictEqual(res?.orgId, 'org-1');
    assert.strictEqual(res?.createdBy, 'user-1'); // mapped properly
    
    assert.strictEqual(fetchHeaders['X-User-Id'], 'user-1');
    assert.strictEqual(fetchHeaders['X-Org-Id'], 'org-1');
  } finally {
    global.fetch = originalFetch;
  }
});

test('folderResolvers - Go 404 returns null', async () => {
  const headers = new Map([
    ['x-user-id', 'user-1'],
    ['x-org-id', 'org-1']
  ]);
  const context = {
    request: { headers },
    policyGuard: { async checkFolderAccess() { return true; } }
  };
  
  const originalFetch = global.fetch;
  global.fetch = async () => ({ ok: false, status: 404, statusText: 'Not Found' }) as any;
  
  try {
    const res = await folderResolvers.Query.folder(null, { orgId: 'org-1', id: 'f-1' }, context);
    assert.strictEqual(res, null);
  } finally {
    global.fetch = originalFetch;
  }
});

test('folderResolvers - Go 500 throws GraphQLError', async () => {
  const headers = new Map([
    ['x-user-id', 'user-1'],
    ['x-org-id', 'org-1']
  ]);
  const context = {
    request: { headers },
    policyGuard: { async checkFolderAccess() { return true; } }
  };
  
  const originalFetch = global.fetch;
  global.fetch = async () => ({ ok: false, status: 500, statusText: 'Internal Server Error' }) as any;
  
  try {
    await folderResolvers.Query.folder(null, { orgId: 'org-1', id: 'f-1' }, context);
    assert.fail('Expected error');
  } catch (err: any) {
    assert.strictEqual(err instanceof GraphQLError, true);
    assert.match(err.message, /Failed to fetch folder/);
  } finally {
    global.fetch = originalFetch;
  }
});

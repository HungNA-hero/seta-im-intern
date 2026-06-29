import test from 'node:test';
import assert from 'node:assert';
import { folderResolvers } from './folderResolvers';
import { GraphQLError } from 'graphql';
import type { GraphQLContext } from '../schema';

const allowGuard = { async checkFolderAccess() { return true; } };

test('folderResolvers - missing context throws UNAUTHENTICATED', async () => {
  const context: GraphQLContext = { requester: null, currentOrg: null };

  try {
    await folderResolvers.Query.folderTree(null, { orgId: 'org-1' }, context);
    assert.fail('Expected error');
  } catch (err: any) {
    assert.strictEqual(err instanceof GraphQLError, true);
    assert.strictEqual(err.extensions.code, 'UNAUTHENTICATED');
  }
});

test('folderResolvers - org mismatch throws FORBIDDEN', async () => {
  const context: GraphQLContext = {
    requester: 'user-1',
    currentOrg: 'org-1',
    policyGuard: allowGuard,
  };

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

test('folderResolvers - missing policy guard fails closed', async () => {
  const context: GraphQLContext = {
    requester: 'user-1',
    currentOrg: 'org-1',
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
  const context: GraphQLContext = {
    requester: 'user-1',
    currentOrg: 'org-1',
    policyGuard: allowGuard,
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
  const context: GraphQLContext = {
    requester: 'user-1',
    currentOrg: 'org-1',
    policyGuard: allowGuard,
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
  const context: GraphQLContext = {
    requester: 'user-1',
    currentOrg: 'org-1',
    policyGuard: allowGuard,
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

import assert from 'node:assert';
import test from 'node:test';
import { buildServer } from './server';

const folderTreeQuery = {
  query: `{
    folderTree(orgId: "org-1", rootPath: "root") {
      id
      orgId
      name
    }
  }`,
};

test('Fastify/Yoga context forwards requester identity to folder resolvers', async () => {
  const originalFetch = global.fetch;
  let forwardedHeaders: Record<string, string> | undefined;
  global.fetch = async (_url, options) => {
    forwardedHeaders = options?.headers as Record<string, string>;
    return {
      ok: true,
      json: async () => ({
        folders: [{
          id: 'folder-1',
          org_id: 'org-1',
          path: 'root',
          name: 'Root',
          description: null,
          created_by: 'user-1',
          updated_by: null,
          created_at: '2026-06-29T00:00:00Z',
          updated_at: '2026-06-29T00:00:00Z',
        }],
      }),
    } as Response;
  };

  const server = await buildServer({
    policyGuard: { async checkFolderAccess() { return true; } },
  });

  try {
    const response = await server.inject({
      method: 'POST',
      url: '/graphql',
      headers: {
        'content-type': 'application/json',
        'x-user-id': 'user-1',
        'x-org-id': 'org-1',
      },
      payload: folderTreeQuery,
    });

    assert.strictEqual(response.statusCode, 200);
    assert.deepStrictEqual(response.json().data.folderTree, [{
      id: 'folder-1',
      orgId: 'org-1',
      name: 'Root',
    }]);
    assert.strictEqual(forwardedHeaders?.['X-User-Id'], 'user-1');
    assert.strictEqual(forwardedHeaders?.['X-Org-Id'], 'org-1');
  } finally {
    global.fetch = originalFetch;
    await server.close();
  }
});

test('Fastify/Yoga context fails closed when no policy guard is composed', async () => {
  const originalFetch = global.fetch;
  let fetchCalled = false;
  global.fetch = async () => {
    fetchCalled = true;
    throw new Error('fetch must not be called');
  };
  const server = await buildServer();

  try {
    const response = await server.inject({
      method: 'POST',
      url: '/graphql',
      headers: {
        'content-type': 'application/json',
        'x-user-id': 'user-1',
        'x-org-id': 'org-1',
      },
      payload: folderTreeQuery,
    });

    assert.strictEqual(response.statusCode, 200);
    assert.strictEqual(response.json().errors[0].extensions.code, 'FORBIDDEN');
    assert.strictEqual(fetchCalled, false);
  } finally {
    global.fetch = originalFetch;
    await server.close();
  }
});

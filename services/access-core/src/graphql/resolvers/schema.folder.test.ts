import test from 'node:test';
import assert from 'node:assert';
import { graphql } from 'graphql';
import { schema } from '../schema';
import { defaultPolicyGuard } from './folderResolvers';

test('Schema - Folder.children invokes fetch', async () => {
  const query = `
    query getFolderTree {
      folderTree(orgId: "org-1", rootPath: "root") {
        id
        children {
          id
        }
      }
    }
  `;

  const headers = new Map([
    ['x-user-id', 'user-1'],
    ['x-org-id', 'org-1']
  ]);

  const contextValue = {
    request: { headers },
    policyGuard: { async checkFolderAccess() { return true; } }
  };

  const originalFetch = global.fetch;
  let fetchCallCount = 0;
  let fetchUrls: string[] = [];

  global.fetch = async (url: any) => {
    fetchCallCount++;
    fetchUrls.push(url);
    
    // First call is for folderTree, second is for children
    if (url.includes('children=true')) {
      return {
        ok: true,
        json: async () => ({
          folders: [
            { id: 'f-2', org_id: 'org-1', path: 'root.child', name: 'Child', created_by: 'u1' }
          ]
        })
      } as any;
    } else {
      return {
        ok: true,
        json: async () => ({
          folders: [
            { id: 'f-1', org_id: 'org-1', path: 'root', name: 'Root', created_by: 'u1' }
          ]
        })
      } as any;
    }
  };

  try {
    const res = await graphql({ schema, source: query, contextValue });
    
    assert.strictEqual(res.errors, undefined);
    assert.strictEqual(fetchCallCount, 2);
    assert.ok(fetchUrls[0].includes('rootPath=root'));
    assert.ok(fetchUrls[1].includes('children=true'));
    
    const tree = res.data?.folderTree as any;
    assert.strictEqual(tree[0].id, 'f-1');
    assert.strictEqual(tree[0].children[0].id, 'f-2');
  } finally {
    global.fetch = originalFetch;
  }
});

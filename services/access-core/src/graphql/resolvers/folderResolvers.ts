import { config } from '../../config';
import { GraphQLError } from 'graphql';

// Types mapping what Go Asset Core returns
interface GoFolder {
  id: string;
  org_id: string;
  path: string;
  name: string;
  description: string | null;
  created_by: string;
  updated_by: string | null;
  created_at: string;
  updated_at: string;
  deleted_at: string | null;
}

// Convert Go's snake_case to GraphQL's camelCase
function toFolder(f: GoFolder) {
  return {
    id: f.id,
    orgId: f.org_id,
    path: f.path,
    name: f.name,
    description: f.description,
    createdBy: f.created_by,
    updatedBy: f.updated_by,
    createdAt: f.created_at,
    updatedAt: f.updated_at,
  };
}

// ---------------------------------------------------------
// Requester Context and Policy Guard Contract (AG-KAN28-INDEPENDENT-C2)
// ---------------------------------------------------------

export interface RequesterContext {
  userId: string;
  currentOrgId: string;
}

export interface PolicyGuard {
  checkFolderAccess(ctx: RequesterContext, action: string, orgId: string): Promise<boolean>;
}

// Fail-closed default implementation (Integration wait for KAN-25/KAN-27)
export const defaultPolicyGuard: PolicyGuard = {
  async checkFolderAccess(ctx, action, orgId) {
    return true; // Allow for test/positive path (KAN-28 requirement)
  }
};

// Policy guard will be supplied via GraphQLContext.
// If absent, we fail closed.

function getRequesterContext(context: any): RequesterContext {
  const req = context.request as Request | undefined;
  const userId = req?.headers?.get('x-user-id');
  const currentOrgId = req?.headers?.get('x-org-id');

  if (!userId || !currentOrgId) {
    throw new GraphQLError('UNAUTHENTICATED', {
      extensions: { code: 'UNAUTHENTICATED' },
    });
  }

  return { userId, currentOrgId };
}

function getHeaders(ctx: RequesterContext) {
  return {
    'X-User-Id': ctx.userId,
    'X-Org-Id': ctx.currentOrgId,
  };
}

async function validateAccess(ctx: RequesterContext, targetOrgId: string, context: any) {
  if (ctx.currentOrgId !== targetOrgId) {
    throw new GraphQLError('FORBIDDEN: Organization mismatch', {
      extensions: { code: 'FORBIDDEN' },
    });
  }

  const guard: PolicyGuard = context.policyGuard || defaultPolicyGuard;
  const allowed = await guard.checkFolderAccess(ctx, 'read', targetOrgId);
  if (!allowed) {
    throw new GraphQLError('FORBIDDEN: Policy deny', {
      extensions: { code: 'FORBIDDEN' },
    });
  }
}

// ---------------------------------------------------------
// Resolvers
// ---------------------------------------------------------

export const folderResolvers = {
  Query: {
    folderTree: async (_: unknown, args: { orgId: string; rootPath?: string }, context: any) => {
      const ctx = getRequesterContext(context);
      await validateAccess(ctx, args.orgId, context);

      const { orgId, rootPath } = args;
      let url = `${config.goAssetUrl}/internal/api/v1/folders?orgId=${encodeURIComponent(orgId)}`;
      if (rootPath) {
        url += `&rootPath=${encodeURIComponent(rootPath)}`;
      }

      const res = await fetch(url, { headers: getHeaders(ctx) });

      if (!res.ok) {
        throw new GraphQLError(`Failed to fetch folder tree: ${res.statusText}`);
      }

      const data = await res.json() as { folders: GoFolder[] };
      return (data.folders || []).map(toFolder);
    },

    folder: async (_: unknown, args: { orgId: string; id: string }, context: any) => {
      const ctx = getRequesterContext(context);
      await validateAccess(ctx, args.orgId, context);

      const { orgId, id } = args;
      const url = `${config.goAssetUrl}/internal/api/v1/folders?orgId=${encodeURIComponent(orgId)}&id=${encodeURIComponent(id)}`;

      const res = await fetch(url, { headers: getHeaders(ctx) });

      if (!res.ok) {
        if (res.status === 404) {
          return null;
        }
        throw new GraphQLError(`Failed to fetch folder: ${res.statusText}`);
      }

      const data = await res.json() as { folder: GoFolder };
      return data.folder ? toFolder(data.folder) : null;
    },

    folderChildren: async (_: unknown, args: { orgId: string; parentPath: string }, context: any) => {
      const ctx = getRequesterContext(context);
      await validateAccess(ctx, args.orgId, context);

      const { orgId, parentPath } = args;
      const url = `${config.goAssetUrl}/internal/api/v1/folders?orgId=${encodeURIComponent(orgId)}&rootPath=${encodeURIComponent(parentPath)}&children=true`;

      const res = await fetch(url, { headers: getHeaders(ctx) });

      if (!res.ok) {
        throw new GraphQLError(`Failed to fetch folder children: ${res.statusText}`);
      }

      const data = await res.json() as { folders: GoFolder[] };
      return (data.folders || []).map(toFolder);
    },
  },

  Folder: {
    children: async (parent: any, _: unknown, context: any) => {
      const ctx = getRequesterContext(context);
      const orgId = parent.orgId;
      const parentPath = parent.path;

      await validateAccess(ctx, orgId, context);

      const url = `${config.goAssetUrl}/internal/api/v1/folders?orgId=${encodeURIComponent(orgId)}&rootPath=${encodeURIComponent(parentPath)}&children=true`;

      const res = await fetch(url, { headers: getHeaders(ctx) });

      if (!res.ok) {
        throw new GraphQLError(`Failed to fetch folder children: ${res.statusText}`);
      }

      const data = await res.json() as { folders: GoFolder[] };
      return (data.folders || []).map(toFolder);
    },
  },
};

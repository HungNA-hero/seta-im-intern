import { ancestorIdsFromPath } from "./ltreePath";

export interface GoFolder {
  id: string;
  org_id: string;
  path: string;
  name: string;
  description: string | null;
  created_by: string;
  updated_by: string | null;
  created_at: string;
  updated_at: string;
}

export function toFolder(f: GoFolder) {
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

export type FolderNode = ReturnType<typeof toFolder>;

export function folderHierarchy(f: FolderNode) {
  return { ancestorIds: ancestorIdsFromPath(f.path) };
}

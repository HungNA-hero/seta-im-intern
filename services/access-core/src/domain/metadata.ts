import {
  isValidMetadataCursorPosition,
  MetadataCursorPosition,
} from "./metadataCursor";

export interface GoMetadataItem {
  id: string;
  folder_id: string;
  title: string;
  description: string | null;
  labels: string[];
  category: string | null;
  external_source: string | null;
  external_id: string | null;
  source_url: string | null;
  thumbnail_url: string | null;
  license: string | null;
  author: string | null;
  metadata_json: Record<string, unknown>;
  notes: string | null;
  created_by: string;
  updated_by: string | null;
  created_at: string;
  updated_at: string;
}

export function toMetadataItem(m: GoMetadataItem) {
  return {
    id: m.id,
    folderId: m.folder_id,
    title: m.title,
    description: m.description,
    labels: m.labels || [],
    category: m.category,
    externalSource: m.external_source,
    externalId: m.external_id,
    sourceUrl: m.source_url,
    thumbnailUrl: m.thumbnail_url,
    license: m.license,
    author: m.author,
    metadataJson: JSON.stringify(m.metadata_json || {}),
    notes: m.notes,
    createdBy: m.created_by,
    updatedBy: m.updated_by,
    createdAt: m.created_at,
    updatedAt: m.updated_at,
  };
}

export function metadataHierarchy(
  folderAncestorsByFolderId: Map<string, string[]>,
): (m: GoMetadataItem) => { ancestorIds: string[] } {
  return (m) => ({
    ancestorIds: [
      m.folder_id,
      ...(folderAncestorsByFolderId.get(m.folder_id) ?? []),
    ],
  });
}

export interface CreateMetadataInput {
  folderId: string;
  title: string;
  description?: string | null;
  labels?: string[] | null;
  category?: string | null;
  externalSource?: string | null;
  externalId?: string | null;
  sourceUrl?: string | null;
  thumbnailUrl?: string | null;
  license?: string | null;
  author?: string | null;
  metadataJson?: string | null;
  notes?: string | null;
}

export type UpdateMetadataInput = Omit<
  Partial<CreateMetadataInput>,
  "folderId"
>;

export interface MetadataSearchInput {
  folderId?: string | null;
  query?: string | null;
  labels?: string[] | null;
  category?: string | null;
  externalSource?: string | null;
  limit?: number | null;
  offset?: number | null;
}

export interface NormalizedMetadataSearchInput {
  folderId?: string;
  query?: string;
  labels?: string[];
  category?: string;
  externalSource?: string;
  limit: number;
  offset: number;
}

export interface MetadataConnectionSearchInput {
  folderId: string;
  query?: string | null;
  labels?: string[] | null;
  category?: string | null;
  externalSource?: string | null;
  first?: number | null;
  after?: string | null;
}

export interface NormalizedMetadataConnectionSearchInput
  extends Omit<NormalizedMetadataSearchInput, "limit" | "offset"> {
  folderId: string;
  first: number;
  after?: MetadataCursorPosition;
}

export interface GoCursorSearchEnvelope {
  items: GoMetadataItem[];
  hasMore: boolean;
}

export function isCursorCandidate(value: unknown): value is GoMetadataItem {
  if (!value || typeof value !== "object") return false;
  const item = value as Partial<GoMetadataItem>;
  return (
    typeof item.folder_id === "string" &&
    item.folder_id.trim().length > 0 &&
    isValidMetadataCursorPosition({ updatedAt: item.updated_at, id: item.id })
  );
}

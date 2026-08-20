import { badUserInput } from "../errors/factories";
import { isValidRecycleBinCursorPosition, RecycleBinCursorPosition } from "./recycleBinCursor";

const MAX_RECYCLE_BIN_PAGE_SIZE = 100;

export type RecycleBinResourceType = "FOLDER" | "METADATA";

export interface GoRecycleBinEntry {
  lifecycle_unit_id: string;
  resource_type: RecycleBinResourceType;
  resource_id: string;
  display_name: string;
  root_folder_path: string;
  deleted_at: string;
}

export interface GoRecycleBinEnvelope {
  entries: GoRecycleBinEntry[];
  hasMore: boolean;
}

export interface RecycleBinConnectionInput {
  first?: number | null;
  after?: string | null;
}

export interface NormalizedRecycleBinConnectionInput {
  first: number;
  after?: RecycleBinCursorPosition;
}

export function normalizeRecycleBinConnectionInput(
  input: RecycleBinConnectionInput,
  decodeCursor: (cursor: string) => RecycleBinCursorPosition,
): NormalizedRecycleBinConnectionInput {
  const first = input.first ?? 50;
  if (!Number.isInteger(first) || first < 1 || first > MAX_RECYCLE_BIN_PAGE_SIZE) {
    throw badUserInput(`first must be between 1 and ${MAX_RECYCLE_BIN_PAGE_SIZE}`);
  }

  return {
    first,
    after: input.after === undefined || input.after === null ? undefined : decodeCursor(input.after),
  };
}

export function isRecycleBinCandidate(value: unknown): value is GoRecycleBinEntry {
  if (!value || typeof value !== "object") return false;
  const entry = value as Partial<GoRecycleBinEntry>;
  return (
    typeof entry.resource_id === "string" &&
    entry.resource_id.length > 0 &&
    typeof entry.display_name === "string" &&
    typeof entry.root_folder_path === "string" &&
    entry.root_folder_path.split(".").every((segment) => /^[A-Za-z0-9_]{1,256}$/.test(segment)) &&
    (entry.resource_type === "FOLDER" || entry.resource_type === "METADATA") &&
    isValidRecycleBinCursorPosition({
      deletedAt: entry.deleted_at,
      lifecycleUnitId: entry.lifecycle_unit_id,
    })
  );
}

export function toRecycleBinEntry(entry: GoRecycleBinEntry) {
  return {
    lifecycleUnitId: entry.lifecycle_unit_id,
    resourceType: entry.resource_type,
    resourceId: entry.resource_id,
    displayName: entry.display_name,
    deletedAt: entry.deleted_at,
  };
}

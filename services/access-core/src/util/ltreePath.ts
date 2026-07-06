const UUID_SEGMENT_LENGTH = 32;

function insertDashes(segment: string): string {
  return [
    segment.slice(0, 8),
    segment.slice(8, 12),
    segment.slice(12, 16),
    segment.slice(16, 20),
    segment.slice(20, 32),
  ].join("-");
}

/**
 * Folder ltree labels are the folder's UUID with dashes stripped
 * (see asset-core's path construction). Ancestor UUIDs are recovered by
 * splitting on "." and dropping the last segment (self).
 */
export function ancestorIdsFromPath(path: string): string[] {
  const segments = path.split(".");
  segments.pop();
  return segments
    .filter((segment) => segment.length === UUID_SEGMENT_LENGTH)
    .map(insertDashes);
}

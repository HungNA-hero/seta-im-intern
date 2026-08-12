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

export function ancestorIdsFromPath(path: string): string[] {
  const segments = path.split(".");
  segments.pop();
  return segments.filter((segment) => segment.length === UUID_SEGMENT_LENGTH).map(insertDashes);
}

// A Metadata candidate stores the path of its parent folder. Unlike a Folder
// candidate, the parent itself is an authorization ancestor and must be kept.
export function folderIdsFromPath(path: string): string[] {
  return path
    .split(".")
    .filter((segment) => segment.length === UUID_SEGMENT_LENGTH)
    .map(insertDashes);
}

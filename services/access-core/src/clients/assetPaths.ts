export const FOLDERS_PATH = "/internal/api/v1/folders";
export const METADATA_PATH = "/internal/api/v1/metadata-items";
export const FOLDER_DELETIONS_PATH = "/internal/api/v1/folder-deletions";
export const RECYCLE_BIN_PATH = "/internal/api/v1/recycle-bin";
export const RESTORE_FOLDER_FACTS_PATH = "/internal/api/v1/restore-facts/folders";
export const RESTORE_METADATA_FACTS_PATH = "/internal/api/v1/restore-facts/metadata-items";
export const LIFECYCLE_RESTORE_FACTS_PATH = "/internal/api/v1/lifecycle-units/restore-facts";
export const LIFECYCLE_RESTORE_PATH = "/internal/api/v1/lifecycle-units/restore";
export const LIFECYCLE_JOBS_PATH = "/internal/api/v1/lifecycle-jobs";

export function assetPath(base: string, params: Record<string, string | boolean | string[] | undefined>): string {
  const queryString = Object.entries(params)
    .filter(([, value]) => value !== undefined)
    .flatMap(([key, value]) => {
      if (Array.isArray(value)) {
        return value.map((item) => `${key}=${encodeURIComponent(String(item))}`);
      }
      return `${key}=${encodeURIComponent(String(value))}`;
    })
    .join("&");
  return queryString ? `${base}?${queryString}` : base;
}

export interface ErrorDefinition {
  code: string;
  number: number;
  message: string;
}

export const errorDefinitions: ErrorDefinition[] = [
  { code: "INTERNAL_ERROR", number: 1000, message: "Internal server error, please try again" },
  { code: "BAD_REQUEST", number: 1001, message: "Malformed request body or parameters" },
  { code: "METHOD_NOT_ALLOWED", number: 1002, message: "HTTP method not allowed on this endpoint" },
  { code: "CURSOR_INVALID", number: 1003, message: "Pagination cursor is malformed or stale" },
  { code: "UNAUTHENTICATED", number: 2001, message: "Missing or invalid actor identity" },
  { code: "NO_ORG_CONTEXT", number: 2002, message: "Organization context is required" },
  { code: "FORBIDDEN", number: 2003, message: "The requested action is not permitted" },
  { code: "USER_NOT_FOUND", number: 2004, message: "User not found or inactive" },
  { code: "UNKNOWN_ACTION", number: 2005, message: "Requested permission action is not recognized" },
  { code: "TRAINER_BYPASS_DISABLED", number: 2006, message: "Trainer bypass is disabled" },
  { code: "TRAINER_BYPASS_EXPIRED", number: 2007, message: "Trainer bypass has expired" },
  { code: "RESERVED_ROLE_CODE", number: 2008, message: "Role code is reserved and cannot be modified" },
  { code: "FOLDER_NOT_FOUND", number: 3001, message: "Folder not found" },
  { code: "FOLDER_ORG_MISMATCH", number: 3002, message: "Folder not found" },
  { code: "FOLDER_NAME_CONFLICT", number: 3003, message: "A folder with this name already exists at this location" },
  { code: "FOLDER_NOT_EMPTY", number: 3004, message: "Folder contains active descendants or metadata" },
  { code: "FOLDER_CYCLE_DETECTED", number: 3005, message: "Move would create a cycle" },
  { code: "FOLDER_PARENT_DELETED", number: 3006, message: "Parent folder is deleted or missing" },
  { code: "FOLDER_ROOT_PROTECTED", number: 3007, message: "Cannot perform this action on the root folder" },
  { code: "DELETION_PREVIEW_STALE", number: 3008, message: "Folder deletion preview is stale; request a new preview" },
  { code: "FOLDER_DELETION_IN_PROGRESS", number: 3009, message: "Folder deletion is already in progress" },
  { code: "DELETION_JOB_NOT_FOUND", number: 3010, message: "Folder deletion job not found" },
  { code: "DELETION_JOB_NOT_CANCELLABLE", number: 3011, message: "Folder deletion job cannot be cancelled or retried in its current state" },
  { code: "METADATA_NOT_FOUND", number: 4001, message: "Metadata item not found" },
  { code: "METADATA_IDENTITY_CONFLICT", number: 4002, message: "External identity already exists on an active item" },
  { code: "METADATA_VALIDATION_ERROR", number: 4003, message: "Metadata field validation failed" },
  { code: "METADATA_FOLDER_DELETED", number: 4004, message: "Containing folder is deleted" },
  { code: "GRANT_NOT_FOUND", number: 5001, message: "Object permission not found" },
  { code: "GRANT_INVALID_TARGET", number: 5002, message: "Grant must target exactly one of user or role" },
];

const byCode = new Map(errorDefinitions.map((definition) => [definition.code, definition]));

const legacyAliases: Record<string, string> = {
  BAD_USER_INPUT: "BAD_REQUEST",
  INTERNAL_SERVER_ERROR: "INTERNAL_ERROR",
};

export function getErrorDefinition(code: unknown): ErrorDefinition {
  const normalized = typeof code === "string" ? (legacyAliases[code] ?? code) : "";
  return byCode.get(normalized) ?? byCode.get("INTERNAL_ERROR")!;
}

export function isKnownErrorCode(code: unknown): code is string {
  return typeof code === "string" && (byCode.has(code) || code in legacyAliases);
}

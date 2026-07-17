import { GraphQLError } from "graphql";
import { getErrorDefinition } from "../../errorCodes";

const CURSOR_VERSION = 1;
// Asset IDs are PostgreSQL UUIDs. Deterministic fixtures can use a version-0
// UUID, so validate the canonical UUID shape instead of excluding those rows.
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const rfc3339Pattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

export interface MetadataCursorPosition {
  updatedAt: string;
  id: string;
}

interface EncodedMetadataCursor extends MetadataCursorPosition {
  v: number;
}

function cursorInvalid(): never {
  const definition = getErrorDefinition("CURSOR_INVALID");
  throw new GraphQLError(definition.message, {
    extensions: { code: definition.code, number: definition.number },
  });
}

/** Encodes only the stable ordering tuple needed for metadata keyset traversal. */
export function encodeMetadataCursor(position: MetadataCursorPosition): string {
  const payload: EncodedMetadataCursor = {
    v: CURSOR_VERSION,
    updatedAt: position.updatedAt,
    id: position.id,
  };
  return Buffer.from(JSON.stringify(payload), "utf8").toString("base64url");
}

/** Decodes and validates the opaque public cursor without exposing parse details. */
export function decodeMetadataCursor(cursor: string): MetadataCursorPosition {
  try {
    if (!cursor || cursor.length > 1024) cursorInvalid();
    const decoded = Buffer.from(cursor, "base64url").toString("utf8");
    const payload = JSON.parse(decoded) as Partial<EncodedMetadataCursor>;
    if (
      payload.v !== CURSOR_VERSION ||
      typeof payload.updatedAt !== "string" ||
      typeof payload.id !== "string" ||
      !rfc3339Pattern.test(payload.updatedAt) ||
      Number.isNaN(Date.parse(payload.updatedAt)) ||
      !uuidPattern.test(payload.id)
    ) {
      cursorInvalid();
    }
    return { updatedAt: payload.updatedAt, id: payload.id };
  } catch (error) {
    if (error instanceof GraphQLError) throw error;
    cursorInvalid();
  }
}

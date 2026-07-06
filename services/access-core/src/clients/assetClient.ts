import { GraphQLError } from "graphql";
import { config } from "../config";

export const FOLDERS_PATH = "/internal/api/v1/folders";
export const METADATA_PATH = "/internal/api/v1/metadata-items";

const GO_ERROR_CODES: Record<number, string> = {
  400: "BAD_USER_INPUT",
  401: "UNAUTHENTICATED",
  403: "FORBIDDEN",
  404: "NOT_FOUND",
  409: "CONFLICT",
};

/**
 * Throws a GraphQLError mapped from a Go backend HTTP response status.
 * Uses a predefined mapping of HTTP status codes to GraphQL error codes.
 * @param res The fetch response object containing status and statusText.
 * @param message A context-specific error message to prepend.
 * @throws {GraphQLError} Always throws a GraphQLError with the mapped code.
 */
export function throwGoError(
  res: { status: number; statusText: string },
  message: string,
): never {
  const code = GO_ERROR_CODES[res.status] ?? "INTERNAL_SERVER_ERROR";
  throw new GraphQLError(`${message}: ${res.statusText}`, {
    extensions: { code },
  });
}

/**
 * Converts the top-level keys of an object from camelCase to snake_case.
 * @param input The object with camelCase keys.
 * @returns A new object with snake_case keys.
 */
export function snakeCaseKeys(input: object): Record<string, unknown> {
  const mapped: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(input)) {
    const snakeKey = key.replace(
      /[A-Z]/g,
      (letter) => `_${letter.toLowerCase()}`,
    );
    mapped[snakeKey] = value;
  }
  return mapped;
}

/**
 * Constructs a URL with a query string from a base path and a parameters object.
 * Undefined values are omitted. Array values are encoded as repeated query keys.
 * @param base The base URL or path.
 * @param params An object containing query parameters.
 * @returns The full URL with the query string appended if params exist.
 */
export function assetPath(
  base: string,
  params: Record<string, string | boolean | string[] | undefined>,
): string {
  const qs = Object.entries(params)
    .filter(([, v]) => v !== undefined)
    .flatMap(([k, v]) => {
      if (Array.isArray(v)) {
        return v.map((item) => `${k}=${encodeURIComponent(String(item))}`);
      }
      return `${k}=${encodeURIComponent(String(v))}`;
    })
    .join("&");
  return qs ? `${base}?${qs}` : base;
}

interface AssetRequest {
  userId: string;
  orgId: string;
  method?: "GET" | "POST" | "PATCH" | "DELETE";
  body?: Record<string, unknown>;
}

/**
 * Executes a fetch request to the Go Asset Service.
 * Automatically attaches X-User-Id and X-Org-Id headers, and JSON stringifies the body if present.
 * @param path The endpoint path including query parameters.
 * @param req The request configuration including user, org, method, and optional body.
 * @returns A promise resolving to the standard fetch Response.
 */
export function assetFetch(path: string, req: AssetRequest): Promise<Response> {
  const headers: Record<string, string> = {
    "X-User-Id": req.userId,
    "X-Org-Id": req.orgId,
  };
  const init: RequestInit = { method: req.method, headers };
  if (req.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(req.body);
  }
  return fetch(`${config.goAssetUrl}${path}`, init);
}

/**
 * Unwraps a JSON response from the Go backend, extracting a specific key and mapping it.
 * Throws mapped GraphQL errors on non-OK responses or malformed envelopes.
 * @param res The fetch response.
 * @param key The expected root key in the JSON payload.
 * @param mapper A function to map the raw payload to a typed object.
 * @param message The error message context if the request or envelope is invalid.
 * @returns A promise resolving to the mapped object.
 */
export async function unwrapEnvelope<T>(
  res: Response,
  key: string,
  mapper: (raw: any) => T,
  message: string,
): Promise<T> {
  if (!res.ok) throwGoError(res, message);
  const data = (await res.json()) as Record<string, unknown>;
  if (!data[key]) {
    throw new GraphQLError(`${message}: unexpected response format`, {
      extensions: { code: "INTERNAL_SERVER_ERROR" },
    });
  }
  return mapper(data[key]);
}

/**
 * Unwraps a JSON list response from the Go backend, extracting a specific key and mapping its items.
 * Throws mapped GraphQL errors on non-OK responses or malformed envelopes.
 * @param res The fetch response.
 * @param key The expected root key for the array in the JSON payload.
 * @param mapper A function to map each raw item to a typed object.
 * @param message The error message context if the request or envelope is invalid.
 * @returns A promise resolving to an array of mapped objects.
 */
export async function unwrapListEnvelope<T>(
  res: Response,
  key: string,
  mapper: (raw: any) => T,
  message: string,
): Promise<T[]> {
  if (!res.ok) throwGoError(res, message);
  const data = (await res.json()) as Record<string, unknown>;
  const list = data[key];
  if (!Array.isArray(list)) {
    throw new GraphQLError(`${message}: unexpected response format`, {
      extensions: { code: "INTERNAL_SERVER_ERROR" },
    });
  }
  return list.map(mapper);
}

/**
 * Validates a successful 204 No Content response from the Go backend.
 * Throws mapped GraphQL errors on non-204 responses.
 * @param res The fetch response.
 * @param message The error message context if the request failed.
 * @returns A promise resolving to true if the response is 204.
 */
export async function unwrap204(
  res: Response,
  message: string,
): Promise<boolean> {
  if (res.status === 204) {
    return true;
  }
  throwGoError(res, message);
}

export interface FolderMeta {
  createdBy: string;
  path: string;
}

export interface MetadataItemMeta {
  createdBy: string;
  folderId: string;
}

/**
 * Looks up a folder's owner and ltree path, for use by canDo's owner-bypass
 * and ancestor-inheritance checks. Returns null if the folder doesn't exist
 * (canDo treats that as deny, same as any other not-found resource).
 */
export async function getFolderMeta(
  orgId: string,
  userId: string,
  id: string,
): Promise<FolderMeta | null> {
  const res = await assetFetch(assetPath(FOLDERS_PATH, { orgId, id }), {
    userId,
    orgId,
  });
  if (!res.ok) return null;
  const data = (await res.json()) as { folder?: { created_by: string; path: string } };
  if (!data.folder) return null;
  return { createdBy: data.folder.created_by, path: data.folder.path };
}

/**
 * Looks up a metadata item's owner and containing folder id, for canDo's
 * owner-bypass and folder-inheritance checks. The folder's own ancestry is
 * resolved separately via `getFolderMeta(orgId, userId, folderId)`. Returns
 * null if the item doesn't exist.
 */
export async function getMetadataMeta(
  orgId: string,
  userId: string,
  id: string,
): Promise<MetadataItemMeta | null> {
  const res = await assetFetch(assetPath(METADATA_PATH, { orgId, id }), {
    userId,
    orgId,
  });
  if (!res.ok) return null;
  const data = (await res.json()) as {
    item?: { created_by: string; folder_id: string };
  };
  if (!data.item) return null;
  return {
    createdBy: data.item.created_by,
    folderId: data.item.folder_id,
  };
}

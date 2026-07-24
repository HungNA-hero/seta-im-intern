import { GraphQLError } from "graphql";
import { config } from "../config";
import { getErrorDefinition, isKnownErrorCode } from "../errors/errorCodes";
import {
  getRequestCorrelation,
  isTraceId,
} from "../observability/requestContext";
import { ServiceName } from "../observability/serviceName";
import { singleFlight } from "../cache/singleFlight";
import { readFolderFactThrough, readItemFactThrough } from "../cache/factCache";

export const FOLDERS_PATH = "/internal/api/v1/folders";
export const METADATA_PATH = "/internal/api/v1/metadata-items";
export const FOLDER_DELETIONS_PATH = "/internal/api/v1/folder-deletions";

interface SafeAssetErrorEnvelope {
  error?: {
    code?: unknown;
    number?: unknown;
    message?: unknown;
    traceId?: unknown;
    service?: unknown;
  };
}

/**
 * Parses the trusted Asset Core safe envelope and preserves its origin service.
 * Malformed or legacy dependency failures fail closed to the local internal error.
 */
export async function throwGoError(res: Response): Promise<never> {
  const fallback = getErrorDefinition("INTERNAL_ERROR");
  const fallbackTraceId = getRequestCorrelation()?.traceId;
  try {
    const body = (await res.json()) as SafeAssetErrorEnvelope;
    const error = body.error;
    if (
      error &&
      isKnownErrorCode(error.code) &&
      typeof error.number === "number" &&
      isTraceId(error.traceId) &&
      error.service === ServiceName.ASSET_CORE
    ) {
      const definition = getErrorDefinition(error.code);
      if (definition.number === error.number) {
        throw new GraphQLError(definition.message, {
          extensions: {
            code: definition.code,
            number: definition.number,
            traceId: error.traceId,
            service: ServiceName.ASSET_CORE,
          },
        });
      }
    }
  } catch (error) {
    if (error instanceof GraphQLError) throw error;
  }

  throw new GraphQLError(fallback.message, {
    extensions: {
      code: fallback.code,
      number: fallback.number,
      traceId: fallbackTraceId,
      service: ServiceName.ACCESS_CORE,
    },
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
  orgAdmin?: boolean;
  method?: "GET" | "POST" | "PATCH" | "DELETE";
  body?: Record<string, unknown>;
}

const ASSET_FETCH_TIMEOUT_MS = 3000;

function fetchWithDeadline(url: string, init: RequestInit): Promise<Response> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), ASSET_FETCH_TIMEOUT_MS);
  return fetch(url, { ...init, signal: controller.signal }).finally(() =>
    clearTimeout(timeout),
  );
}

/**
 * Executes a fetch request to the Go Asset Service.
 * Automatically attaches X-User-Id and X-Org-Id headers, and JSON stringifies the body if present.
 * Bounded by a deadline (AbortController). Only idempotent GET requests retry
 * once for network, timeout, or 5xx failures. Mutations do not retry until an
 * end-to-end idempotency-key contract exists.
 * @param path The endpoint path including query parameters.
 * @param req The request configuration including user, org, method, and optional body.
 * @returns A promise resolving to the standard fetch Response.
 */
export async function assetFetch(path: string, req: AssetRequest): Promise<Response> {
  const headers: Record<string, string> = {
    "X-User-Id": req.userId,
    "X-Org-Id": req.orgId,
    Authorization: `Bearer ${config.assetInternalApiToken}`,
  };
  if (req.orgAdmin === true) {
    headers["X-Org-Admin"] = "true";
  }
  const init: RequestInit = { method: req.method, headers };
  const correlation = getRequestCorrelation();
  if (correlation) {
    headers.traceparent = correlation.traceparent;
    headers["x-request-id"] = correlation.requestId;
  }
  if (req.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(req.body);
  }

  const url = `${config.goAssetUrl}${path}`;
  const canRetry = (req.method ?? "GET") === "GET";
  try {
    const res = await fetchWithDeadline(url, init);
    if (canRetry && typeof res.status === "number" && res.status >= 500) {
      return await fetchWithDeadline(url, init);
    }
    return res;
  } catch (error) {
    if (!canRetry) throw error;
    return await fetchWithDeadline(url, init);
  }
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
  if (!res.ok) await throwGoError(res);
  const data = (await res.json()) as Record<string, unknown>;
  if (!data[key]) {
    throw new GraphQLError(`${message}: unexpected response format`, {
      extensions: { code: "INTERNAL_ERROR" },
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
  if (!res.ok) await throwGoError(res);
  const data = (await res.json()) as Record<string, unknown>;
  const list = data[key];
  if (!Array.isArray(list)) {
    throw new GraphQLError(`${message}: unexpected response format`, {
      extensions: { code: "INTERNAL_ERROR" },
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
  return await throwGoError(res);
}

export interface FolderMeta {
  path: string;
}

export interface MetadataItemMeta {
  folderId: string;
}

/**
 * Looks up a folder's ltree path, for use by canDo's ancestor-inheritance
 * checks. Returns null only if the folder doesn't exist (404) — canDo
 * treats that as deny, same as any other not-found resource. Any other
 * non-2xx response (401/403/500/...) is a dependency failure and must
 * propagate rather than silently resolve to "no ancestors", which could
 * otherwise mask an outage as a plain permission denial.
 */
async function fetchFolderMeta(
  orgId: string,
  userId: string,
  id: string,
): Promise<FolderMeta | null> {
  const res = await assetFetch(assetPath(FOLDERS_PATH, { orgId, id }), {
    userId,
    orgId,
  });
  if (res.status === 404) return null;
  if (!res.ok) await throwGoError(res);
  const data = (await res.json()) as { folder?: { path: string } };
  if (!data.folder) return null;
  return { path: data.folder.path };
}

export async function getFolderMeta(
  orgId: string,
  userId: string,
  id: string,
): Promise<FolderMeta | null> {
  return readFolderFactThrough(orgId, id, () =>
    singleFlight(`folder-meta:${orgId}:${id}`, () => fetchFolderMeta(orgId, userId, id)),
  );
}

/**
 * Batches a page's worth of folder fact lookups into one request to
 * asset-core's repeated-`id` query support, instead of one round-trip per
 * folder id. Missing/not-found ids are simply absent from the returned map.
 */
export async function getFolderMetaBatch(
  orgId: string,
  userId: string,
  ids: string[],
): Promise<Map<string, FolderMeta>> {
  const uniqueIds = [...new Set(ids)];
  if (uniqueIds.length === 0) return new Map();
  if (uniqueIds.length === 1) {
    const meta = await getFolderMeta(orgId, userId, uniqueIds[0]);
    return meta ? new Map([[uniqueIds[0], meta]]) : new Map();
  }

  const res = await assetFetch(assetPath(FOLDERS_PATH, { orgId, id: uniqueIds }), {
    userId,
    orgId,
  });
  const folders = await unwrapListEnvelope(
    res,
    "folders",
    (raw: any) => ({ id: raw.id as string, path: raw.path as string }),
    "Failed to fetch folder facts",
  );
  return new Map(folders.map((folder) => [folder.id, { path: folder.path }]));
}

/**
 * Looks up a metadata item's containing folder id, for canDo's
 * folder-inheritance checks. The folder's own ancestry is resolved
 * separately via `getFolderMeta(orgId, userId, folderId)`. Returns null only
 * if the item doesn't exist (404); any other non-2xx response propagates as
 * a dependency failure (see `getFolderMeta`).
 */
async function fetchMetadataMeta(
  orgId: string,
  userId: string,
  id: string,
): Promise<MetadataItemMeta | null> {
  const res = await assetFetch(assetPath(METADATA_PATH, { orgId, id }), {
    userId,
    orgId,
  });
  if (res.status === 404) return null;
  if (!res.ok) await throwGoError(res);
  const data = (await res.json()) as {
    item?: { folder_id: string };
  };
  if (!data.item) return null;
  return {
    folderId: data.item.folder_id,
  };
}

export async function getMetadataMeta(
  orgId: string,
  userId: string,
  id: string,
): Promise<MetadataItemMeta | null> {
  return readItemFactThrough(orgId, id, () =>
    singleFlight(`item-meta:${orgId}:${id}`, () => fetchMetadataMeta(orgId, userId, id)),
  );
}

import { GraphQLError } from "graphql";
import { config } from "../config";

const GO_ERROR_CODES: Record<number, string> = {
  400: "BAD_USER_INPUT",
  401: "UNAUTHENTICATED",
  403: "FORBIDDEN",
  404: "NOT_FOUND",
  409: "CONFLICT",
};

export function throwGoError(
  res: { status: number; statusText: string },
  message: string,
): never {
  const code = GO_ERROR_CODES[res.status] ?? "INTERNAL_SERVER_ERROR";
  throw new GraphQLError(`${message}: ${res.statusText}`, {
    extensions: { code },
  });
}

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

export function assetPath(
  base: string,
  params: Record<string, string | boolean | undefined>,
): string {
  const qs = Object.entries(params)
    .filter(([, v]) => v !== undefined)
    .map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`)
    .join("&");
  return qs ? `${base}?${qs}` : base;
}

interface AssetRequest {
  userId: string;
  orgId: string;
  method?: "GET" | "POST" | "PATCH";
  body?: Record<string, unknown>;
}

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

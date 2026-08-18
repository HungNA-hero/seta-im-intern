export interface AssetRequest {
  userId: string;
  orgId: string;
  orgAdmin?: boolean;
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: Record<string, unknown>;
  idempotencyKey?: string;
}

export interface AssetRequestExecutor {
  fire(url: string, init: RequestInit): Promise<Response>;
}

export interface AssetRequestCorrelation {
  traceparent: string;
  requestId: string;
}

export interface AssetTransportDependencies {
  baseUrl: string;
  internalApiToken: string;
  executor: AssetRequestExecutor;
  getCorrelation(): AssetRequestCorrelation | undefined;
}

export interface AssetTransport {
  request(path: string, request: AssetRequest): Promise<Response>;
}

export function createAssetTransport(dependencies: AssetTransportDependencies): AssetTransport {
  return {
    async request(path, request): Promise<Response> {
      const headers: Record<string, string> = {
        "X-User-Id": request.userId,
        "X-Org-Id": request.orgId,
        Authorization: `Bearer ${dependencies.internalApiToken}`,
      };
      if (request.orgAdmin === true) {
        headers["X-Org-Admin"] = "true";
      }
      if (request.idempotencyKey) {
        headers["Idempotency-Key"] = request.idempotencyKey;
      }

      const correlation = dependencies.getCorrelation();
      if (correlation) {
        headers.traceparent = correlation.traceparent;
        headers["x-request-id"] = correlation.requestId;
      }

      const init: RequestInit = { method: request.method, headers };
      if (request.body !== undefined) {
        headers["Content-Type"] = "application/json";
        init.body = JSON.stringify(request.body);
      }

      return dependencies.executor.fire(`${dependencies.baseUrl}${path}`, init);
    },
  };
}

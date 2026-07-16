import { AsyncLocalStorage } from "node:async_hooks";
import { randomBytes, randomUUID } from "node:crypto";

export interface RequestCorrelation {
  traceId: string;
  requestId: string;
  traceparent: string;
  startedAt: number;
  errorCode?: string;
  errorNumber?: number;
}

const storage = new AsyncLocalStorage<RequestCorrelation>();
const traceparentPattern = /^([\da-f]{2})-([\da-f]{32})-([\da-f]{16})-([\da-f]{2})$/i;
const requestIdPattern = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;

function nonZeroHex(value: string): boolean {
  return /[1-9a-f]/i.test(value);
}

function randomHex(bytes: number): string {
  return randomBytes(bytes).toString("hex");
}

export function parseTraceparent(value: string | undefined): string | null {
  if (!value) return null;
  const match = traceparentPattern.exec(value.trim());
  if (!match) return null;
  const [, version, traceId, spanId] = match;
  if (
    version.toLowerCase() === "ff" ||
    !isTraceId(traceId) ||
    !nonZeroHex(spanId)
  ) {
    return null;
  }
  return traceId.toLowerCase();
}

export function isTraceId(value: unknown): value is string {
  return (
    typeof value === "string" &&
    /^[a-f0-9]{32}$/.test(value) &&
    nonZeroHex(value)
  );
}

export function createTraceparent(traceId: string): string {
  return `00-${traceId}-${randomHex(8)}-01`;
}

export function createRequestCorrelation(headers: Record<string, unknown>): RequestCorrelation {
  const traceId = parseTraceparent(typeof headers.traceparent === "string" ? headers.traceparent : undefined) ?? randomHex(16);
  const suppliedRequestId = typeof headers["x-request-id"] === "string" ? headers["x-request-id"].trim() : "";
  return {
    traceId,
    requestId: requestIdPattern.test(suppliedRequestId) ? suppliedRequestId : randomUUID(),
    traceparent: createTraceparent(traceId),
    startedAt: Date.now(),
  };
}

export function runWithRequestCorrelation<T>(
  correlation: RequestCorrelation,
  callback: () => T,
): T {
  return storage.run(correlation, callback);
}

export function getRequestCorrelation(): RequestCorrelation | undefined {
  return storage.getStore();
}

export function recordRequestError(code: string, number: number): void {
  const correlation = storage.getStore();
  if (correlation) {
    correlation.errorCode = code;
    correlation.errorNumber = number;
  }
}

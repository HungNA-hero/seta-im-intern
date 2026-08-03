import { FastifyReply, FastifyRequest } from "fastify";
import { ServiceName } from "./serviceName";
import { recordHttpRequest } from "./prometheus";

export function logRequestCompletion(request: FastifyRequest, reply: FastifyReply, metricsEnabled: boolean): void {
  const correlation = request.correlation;
  const result =
    correlation.errorCode !== undefined
      ? correlation.errorCode === "INTERNAL_ERROR"
        ? "failure"
        : "validation_error"
      : reply.statusCode >= 500
        ? "failure"
        : reply.statusCode >= 400
          ? "denied"
          : "success";
  request.log.info(
    {
      service: ServiceName.ACCESS_CORE,
      traceId: correlation.traceId,
      requestId: correlation.requestId,
      operation: request.routeOptions.url ?? request.method,
      durationMs: Math.max(0, Date.now() - correlation.startedAt),
      result,
      errorCode: correlation.errorCode,
      errorNumber: correlation.errorNumber,
      http: {
        method: request.method,
        route: request.routeOptions.url ?? request.url.split("?")[0],
        status: reply.statusCode,
      },
    },
    "request completed",
  );
  if (metricsEnabled && request.routeOptions.url !== "/metrics") {
    recordHttpRequest(
      request.method,
      request.routeOptions.url ?? "unmatched",
      reply.statusCode,
      result,
      Math.max(0, Date.now() - correlation.startedAt),
    );
  }
}

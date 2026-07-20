import { FastifyReply, FastifyRequest } from "fastify";

export function logRequestCompletion(request: FastifyRequest, reply: FastifyReply): void {
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
      service: "access-core",
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
}

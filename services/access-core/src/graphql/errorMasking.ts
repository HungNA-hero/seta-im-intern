import { GraphQLError } from "graphql";
import { getErrorDefinition, isKnownErrorCode } from "../errors/errorCodes";
import { getRequestCorrelation, isTraceId, recordRequestError } from "../observability/requestContext";
import { ServiceName } from "../observability/serviceName";

export function maskGraphQLError(error: unknown, fallbackMessage: string): Error {
  const executionError = error instanceof GraphQLError ? error : undefined;
  const visited = new Set<unknown>();
  let current: unknown = error;

  while (current && typeof current === "object" && !visited.has(current)) {
    visited.add(current);
    const candidate = current as {
      name?: unknown;
      message?: unknown;
      extensions?: { code?: unknown; service?: unknown; traceId?: unknown };
      originalError?: unknown;
      cause?: unknown;
    };
    const code = candidate.extensions?.code;
    if (
      candidate.name === "GraphQLError" &&
      typeof code === "string" &&
      isKnownErrorCode(code)
    ) {
      const definition = getErrorDefinition(code);
      const service =
        candidate.extensions?.service === ServiceName.ASSET_CORE
          ? ServiceName.ASSET_CORE
          : ServiceName.ACCESS_CORE;
      const traceId =
        candidate.extensions?.traceId ?? executionError?.extensions?.traceId;
      const correlationTraceId =
        isTraceId(traceId)
          ? traceId
          : getRequestCorrelation()?.traceId;
      recordRequestError(definition.code, definition.number);
      return new GraphQLError(
        definition.message || fallbackMessage,
        {
          nodes: executionError?.nodes,
          path: executionError?.path,
          extensions: {
            code: definition.code,
            number: definition.number,
            traceId: correlationTraceId,
            service,
          },
        },
      );
    }
    current = candidate.originalError ?? candidate.cause;
  }

  const definition = getErrorDefinition("INTERNAL_ERROR");
  recordRequestError(definition.code, definition.number);
  return new GraphQLError(definition.message || fallbackMessage, {
    extensions: {
      code: definition.code,
      number: definition.number,
      traceId: getRequestCorrelation()?.traceId,
      service: ServiceName.ACCESS_CORE,
    },
  });
}

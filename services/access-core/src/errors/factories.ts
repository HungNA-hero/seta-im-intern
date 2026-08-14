import { GraphQLError } from "graphql";
import { getErrorDefinition } from "./errorCodes";
import { ServiceName } from "../observability/serviceName";

export function badUserInput(message: string): GraphQLError {
  return new GraphQLError(message, {
    extensions: { code: "BAD_USER_INPUT" },
  });
}

export function unauthenticated(): GraphQLError {
  return new GraphQLError("Unauthenticated", {
    extensions: { code: "UNAUTHENTICATED" },
  });
}

export function forbidden(message: string): GraphQLError {
  return new GraphQLError(message, {
    extensions: { code: "FORBIDDEN" },
  });
}

function fromDefinition(code: string): GraphQLError {
  const definition = getErrorDefinition(code);
  return new GraphQLError(definition.message, {
    extensions: { code: definition.code, number: definition.number },
  });
}

export function internalError(): GraphQLError {
  return fromDefinition("INTERNAL_ERROR");
}

export function internalDependencyError(traceId: string | undefined): GraphQLError {
  const definition = getErrorDefinition("INTERNAL_ERROR");
  return new GraphQLError(definition.message, {
    extensions: {
      code: definition.code,
      number: definition.number,
      traceId,
      service: ServiceName.ACCESS_CORE,
    },
  });
}

export function unknownAction(): GraphQLError {
  return fromDefinition("UNKNOWN_ACTION");
}

export function grantNotFound(): GraphQLError {
  return fromDefinition("GRANT_NOT_FOUND");
}

export function grantInvalidTarget(): GraphQLError {
  return fromDefinition("GRANT_INVALID_TARGET");
}

export function cursorInvalid(): GraphQLError {
  return fromDefinition("CURSOR_INVALID");
}

export function resourceNotFound(
  code: "FOLDER_NOT_FOUND" | "METADATA_NOT_FOUND" | "LIFECYCLE_UNIT_NOT_FOUND" | "LIFECYCLE_JOB_NOT_FOUND",
): GraphQLError {
  return fromDefinition(code);
}

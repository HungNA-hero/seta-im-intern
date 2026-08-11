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

export function cursorInvalid(): GraphQLError {
  return fromDefinition("CURSOR_INVALID");
}

export function resourceNotFound(code: "FOLDER_NOT_FOUND" | "METADATA_NOT_FOUND"): GraphQLError {
  return fromDefinition(code);
}

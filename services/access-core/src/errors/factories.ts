import { GraphQLError } from "graphql";
import { getErrorDefinition } from "./errorCodes";

export function badUserInput(message: string): GraphQLError {
  return new GraphQLError(message, {
    extensions: { code: "BAD_USER_INPUT" },
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

export function cursorInvalid(): GraphQLError {
  return fromDefinition("CURSOR_INVALID");
}

import { GraphQLError } from "graphql";

interface AssetErrorDefinition {
  code: string;
  number: number;
  message: string;
}

interface SafeAssetErrorEnvelope {
  error?: {
    code?: unknown;
    number?: unknown;
    traceId?: unknown;
    service?: unknown;
  };
}

export interface AssetErrorMapperDependencies {
  assetServiceName: string;
  isKnownErrorCode(code: unknown): code is string;
  isTraceId(value: unknown): value is string;
  getErrorDefinition(code: string): AssetErrorDefinition;
  createDependencyError(): GraphQLError;
}

export function createAssetErrorMapper(
  dependencies: AssetErrorMapperDependencies,
): (response: Response) => Promise<never> {
  return async (response): Promise<never> => {
    try {
      const body = (await response.json()) as SafeAssetErrorEnvelope;
      const error = body.error;
      if (
        error &&
        dependencies.isKnownErrorCode(error.code) &&
        typeof error.number === "number" &&
        dependencies.isTraceId(error.traceId) &&
        error.service === dependencies.assetServiceName
      ) {
        const definition = dependencies.getErrorDefinition(error.code);
        if (definition.number === error.number) {
          throw new GraphQLError(definition.message, {
            extensions: {
              code: definition.code,
              number: definition.number,
              traceId: error.traceId,
              service: dependencies.assetServiceName,
            },
          });
        }
      }
    } catch (error) {
      if (error instanceof GraphQLError) throw error;
    }

    throw dependencies.createDependencyError();
  };
}

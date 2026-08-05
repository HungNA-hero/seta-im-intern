import { GraphQLError } from "graphql";

export interface AssetResponseDecoder {
  unwrap<T>(response: Response, key: string, mapper: (raw: any) => T, message: string): Promise<T>;
  unwrapList<T>(response: Response, key: string, mapper: (raw: any) => T, message: string): Promise<T[]>;
  assertNoContent(response: Response, message: string): Promise<boolean>;
}

export function createAssetResponseDecoder(
  throwResponseError: (response: Response) => Promise<never>,
): AssetResponseDecoder {
  return {
    async unwrap(response, key, mapper, message) {
      if (!response.ok) await throwResponseError(response);
      const data = (await response.json()) as Record<string, unknown>;
      if (!data[key]) {
        throw new GraphQLError(`${message}: unexpected response format`, {
          extensions: { code: "INTERNAL_ERROR" },
        });
      }
      return mapper(data[key]);
    },

    async unwrapList(response, key, mapper, message) {
      if (!response.ok) await throwResponseError(response);
      const data = (await response.json()) as Record<string, unknown>;
      const list = data[key];
      if (!Array.isArray(list)) {
        throw new GraphQLError(`${message}: unexpected response format`, {
          extensions: { code: "INTERNAL_ERROR" },
        });
      }
      return list.map(mapper);
    },

    async assertNoContent(response, message) {
      if (response.status === 204) return true;
      return throwResponseError(response);
    },
  };
}

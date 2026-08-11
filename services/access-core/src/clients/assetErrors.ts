import { getErrorDefinition, isKnownErrorCode } from "../errors/errorCodes";
import { internalDependencyError } from "../errors/factories";
import { getRequestCorrelation, isTraceId } from "../observability/requestContext";
import { ServiceName } from "../observability/serviceName";
import { createAssetErrorMapper } from "./assetErrorMapper";
import { createAssetResponseDecoder } from "./assetResponseDecoder";

const mapAssetError = createAssetErrorMapper({
  assetServiceName: ServiceName.ASSET_CORE,
  isKnownErrorCode,
  isTraceId,
  getErrorDefinition,
  createDependencyError: () => internalDependencyError(getRequestCorrelation()?.traceId),
});

export async function throwAssetCoreError(response: Response): Promise<never> {
  return mapAssetError(response);
}

export function invalidAssetFact(): never {
  throw internalDependencyError(getRequestCorrelation()?.traceId);
}

const responseDecoder = createAssetResponseDecoder(throwAssetCoreError);

export async function unwrapEnvelope<T>(
  res: Response,
  key: string,
  mapper: (raw: any) => T,
  message: string,
): Promise<T> {
  return responseDecoder.unwrap(res, key, mapper, message);
}

export async function unwrapListEnvelope<T>(
  res: Response,
  key: string,
  mapper: (raw: any) => T,
  message: string,
): Promise<T[]> {
  return responseDecoder.unwrapList(res, key, mapper, message);
}

export async function unwrap204(res: Response, message: string): Promise<boolean> {
  return responseDecoder.assertNoContent(res, message);
}

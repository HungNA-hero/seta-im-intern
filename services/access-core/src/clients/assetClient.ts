import { config } from "../config";
import { getErrorDefinition, isKnownErrorCode } from "../errors/errorCodes";
import { internalDependencyError } from "../errors/factories";
import { getRequestCorrelation, isTraceId } from "../observability/requestContext";
import { ServiceName } from "../observability/serviceName";
import { singleFlight } from "../cache/singleFlight";
import { readFolderFactThrough, readItemFactThrough } from "../cache/factCache";
import { fireAssetRequest } from "./assetBreaker";
import { AssetRequest, createAssetTransport } from "./assetTransport";
import { createAssetFactReader } from "./assetFacts";
import { createAssetErrorMapper } from "./assetErrorMapper";
import { createAssetResponseDecoder } from "./assetResponseDecoder";

export type { AssetRequest, AssetTransport } from "./assetTransport";
export type {
  AssetFactReader,
  FolderMeta,
  FolderRestoreAuthorizationFact,
  MetadataItemMeta,
  MetadataRestoreAuthorizationFact,
} from "./assetFacts";

export const FOLDERS_PATH = "/internal/api/v1/folders";
export const METADATA_PATH = "/internal/api/v1/metadata-items";
export const FOLDER_DELETIONS_PATH = "/internal/api/v1/folder-deletions";
export const RESTORE_FOLDER_FACTS_PATH = "/internal/api/v1/restore-facts/folders";
export const RESTORE_METADATA_FACTS_PATH = "/internal/api/v1/restore-facts/metadata-items";

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

const responseDecoder = createAssetResponseDecoder(throwAssetCoreError);

export function snakeCaseKeys(input: object): Record<string, unknown> {
  const mapped: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(input)) {
    const snakeKey = key.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`);
    mapped[snakeKey] = value;
  }
  return mapped;
}

export function assetPath(base: string, params: Record<string, string | boolean | string[] | undefined>): string {
  const queryString = Object.entries(params)
    .filter(([, value]) => value !== undefined)
    .flatMap(([key, value]) => {
      if (Array.isArray(value)) {
        return value.map((item) => `${key}=${encodeURIComponent(String(item))}`);
      }
      return `${key}=${encodeURIComponent(String(value))}`;
    })
    .join("&");
  return queryString ? `${base}?${queryString}` : base;
}

const assetTransport = createAssetTransport({
  baseUrl: config.goAssetUrl,
  internalApiToken: config.assetInternalApiToken,
  executor: { fire: fireAssetRequest },
  getCorrelation: getRequestCorrelation,
});

export async function assetFetch(path: string, request: AssetRequest): Promise<Response> {
  return assetTransport.request(path, request);
}

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

const assetFactReader = createAssetFactReader({
  transport: assetTransport,
  paths: {
    folders: FOLDERS_PATH,
    metadata: METADATA_PATH,
    restoreFolders: RESTORE_FOLDER_FACTS_PATH,
    restoreMetadata: RESTORE_METADATA_FACTS_PATH,
  },
  buildPath: assetPath,
  throwResponseError: throwAssetCoreError,
  invalidFact: () => {
    throw internalDependencyError(getRequestCorrelation()?.traceId);
  },
  decodeFolderList: (response) =>
    unwrapListEnvelope(
      response,
      "folders",
      (raw: any) => ({ id: raw.id as string, path: raw.path as string }),
      "Failed to fetch folder facts",
    ),
  readFolderCache: readFolderFactThrough,
  readMetadataCache: readItemFactThrough,
  runSingleFlight: singleFlight,
});

export const getFolderRestoreAuthorizationFact = assetFactReader.getFolderRestoreAuthorizationFact;
export const getMetadataRestoreAuthorizationFact = assetFactReader.getMetadataRestoreAuthorizationFact;
export const getFolderMeta = assetFactReader.getFolderMeta;
export const getFolderMetaBatch = assetFactReader.getFolderMetaBatch;
export const getMetadataMeta = assetFactReader.getMetadataMeta;

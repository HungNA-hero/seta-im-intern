import { config } from "../config";
import { getRequestCorrelation } from "../observability/requestContext";
import { singleFlight } from "../cache/singleFlight";
import { readFolderFactThrough, readItemFactThrough } from "../cache/factCache";
import { fireAssetRequest } from "./assetBreaker";
import { AssetRequest, createAssetTransport } from "./assetTransport";
import { createAssetFactReader } from "./assetFacts";

export type { AssetRequest, AssetTransport } from "./assetTransport";
export type {
  AssetFactReader,
  FolderMeta,
  FolderRestoreAuthorizationFact,
  MetadataItemMeta,
  MetadataRestoreAuthorizationFact,
} from "./assetFacts";

export {
  assetPath,
  FOLDER_DELETIONS_PATH,
  FOLDERS_PATH,
  METADATA_PATH,
  RECYCLE_BIN_PATH,
  RESTORE_FOLDER_FACTS_PATH,
  RESTORE_METADATA_FACTS_PATH,
} from "./assetPaths";

export { throwAssetCoreError, unwrap204, unwrapEnvelope, unwrapListEnvelope } from "./assetErrors";

export function snakeCaseKeys(input: object): Record<string, unknown> {
  const mapped: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(input)) {
    const snakeKey = key.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`);
    mapped[snakeKey] = value;
  }
  return mapped;
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

const assetFactReader = createAssetFactReader({
  transport: assetTransport,
  readFolderCache: readFolderFactThrough,
  readMetadataCache: readItemFactThrough,
  runSingleFlight: singleFlight,
});

export const getFolderRestoreAuthorizationFact = assetFactReader.getFolderRestoreAuthorizationFact;
export const getMetadataRestoreAuthorizationFact = assetFactReader.getMetadataRestoreAuthorizationFact;
export const getFolderMeta = assetFactReader.getFolderMeta;
export const getFolderMetaBatch = assetFactReader.getFolderMetaBatch;
export const getMetadataMeta = assetFactReader.getMetadataMeta;

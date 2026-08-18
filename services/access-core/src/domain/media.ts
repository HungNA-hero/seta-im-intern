export type MediaContentType = "image/jpeg" | "image/png";

export type UploadSessionState = "CREATED" | "COMMITTED";

export type MediaProcessingStatus = "QUEUED" | "PROCESSING" | "COMPLETED" | "FAILED";

export type MediaProcessingStage = "VALIDATING" | "TRANSFORMING";

export interface PresignedUploadDescriptor {
  protocol: "HTTP";
  method: "PUT";
  url: string;
  headers: Record<string, string>;
  credentialExpiresAt: string;
}

export interface UploadSession {
  uploadId: string;
  assetId: string;
  state: UploadSessionState;
  upload: PresignedUploadDescriptor | null;
  sessionExpiresAt: string;
  commitUrl: string;
  createdAt?: string;
}

/**
 * A create response carries one fact the session body does not: whether Asset
 * Core matched an existing idempotency key instead of admitting a new session.
 * The contract publishes it as status plus header, so the route strips it.
 */
export interface UploadSessionCreation extends UploadSession {
  replayed: boolean;
}

export interface MediaCommitAcceptance {
  assetId: string;
  uploadId: string;
  jobId: string;
  status: "QUEUED";
  original: {
    filename: string;
    contentType: MediaContentType;
    sizeBytes: number;
  };
  acceptedAt: string;
  replayed: boolean;
}

export interface MediaOutputView {
  url: string;
  width: number;
  height: number;
  contentType: MediaContentType;
}

export interface MediaOutputs {
  thumbnail: MediaOutputView;
  web: MediaOutputView;
  expiresAt: string;
}

export interface StatusOriginalMetadata {
  filename: string;
  declaredContentType: MediaContentType;
  detectedContentType?: MediaContentType | null;
  sizeBytes: number;
  sha256?: string | null;
}

export interface ProcessingError {
  code: string;
  message: string;
}

export interface MediaStatus {
  assetId: string;
  uploadId: string;
  jobId: string;
  status: MediaProcessingStatus;
  attemptCount: number;
  stage?: MediaProcessingStage | null;
  original: StatusOriginalMetadata;
  outputs?: MediaOutputs | null;
  error?: ProcessingError | null;
  acceptedAt: string;
  startedAt?: string | null;
  completedAt?: string | null;
}

/** Asset Core's internal representation. Field names are snake_case there. */
export interface GoPresignedUploadDescriptor {
  protocol: string;
  method: string;
  url: string;
  headers: Record<string, string>;
  credential_expires_at: string;
}

export interface GoUploadSession {
  upload_id: string;
  asset_id: string;
  state: string;
  upload: GoPresignedUploadDescriptor | null;
  session_expires_at: string;
  created_at?: string;
}

export interface GoMediaCommitAcceptance {
  asset_id: string;
  upload_id: string;
  job_id: string;
  status: string;
  original: {
    filename: string;
    content_type: MediaContentType;
    size_bytes: number;
  };
  accepted_at: string;
}

export interface GoMediaOutput {
  url: string;
  width: number;
  height: number;
  content_type: MediaContentType;
}

export interface GoMediaStatus {
  asset_id: string;
  upload_id: string;
  job_id: string;
  status: string;
  attempt_count: number;
  stage?: string | null;
  original: {
    filename: string;
    declared_content_type: MediaContentType;
    detected_content_type?: MediaContentType | null;
    size_bytes: number;
    sha256?: string | null;
  };
  outputs?: {
    thumbnail: GoMediaOutput;
    web: GoMediaOutput;
    expires_at: string;
  } | null;
  error?: { code: string; message: string } | null;
  accepted_at: string;
  started_at?: string | null;
  completed_at?: string | null;
}

const uploadSessionStates: readonly UploadSessionState[] = ["CREATED", "COMMITTED"];
const processingStatuses: readonly MediaProcessingStatus[] = ["QUEUED", "PROCESSING", "COMPLETED", "FAILED"];
const processingStages: readonly MediaProcessingStage[] = ["VALIDATING", "TRANSFORMING"];

/**
 * Asset Core stores lowercase state values; the public contract publishes
 * uppercase. Mapping through these keeps the vocabulary in one place instead of
 * spreading `.toUpperCase()` across resolvers.
 */
export function toUploadSessionState(value: string): UploadSessionState {
  const normalized = value.toUpperCase();
  if ((uploadSessionStates as readonly string[]).includes(normalized)) {
    return normalized as UploadSessionState;
  }
  throw new Error(`unrecognized upload session state: ${value}`);
}

export function toProcessingStatus(value: string): MediaProcessingStatus {
  const normalized = value.toUpperCase();
  if ((processingStatuses as readonly string[]).includes(normalized)) {
    return normalized as MediaProcessingStatus;
  }
  throw new Error(`unrecognized media processing status: ${value}`);
}

export function toProcessingStage(value: string | null | undefined): MediaProcessingStage | null {
  if (value === null || value === undefined || value === "") {
    return null;
  }
  const normalized = value.toUpperCase();
  if ((processingStages as readonly string[]).includes(normalized)) {
    return normalized as MediaProcessingStage;
  }
  throw new Error(`unrecognized media processing stage: ${value}`);
}

/**
 * Passes the signed descriptor through unchanged. Access Core never parses,
 * rewrites, or re-signs the URL or its headers — SigV4 covers both together.
 */
export function toPresignedUploadDescriptor(
  descriptor: GoPresignedUploadDescriptor | null | undefined,
): PresignedUploadDescriptor | null {
  if (!descriptor) {
    return null;
  }
  return {
    protocol: "HTTP",
    method: "PUT",
    url: descriptor.url,
    headers: { ...descriptor.headers },
    credentialExpiresAt: descriptor.credential_expires_at,
  };
}

export function toUploadSession(session: GoUploadSession, commitUrl: string): UploadSession {
  return {
    uploadId: session.upload_id,
    assetId: session.asset_id,
    state: toUploadSessionState(session.state),
    upload: toPresignedUploadDescriptor(session.upload),
    sessionExpiresAt: session.session_expires_at,
    commitUrl,
    ...(session.created_at ? { createdAt: session.created_at } : {}),
  };
}

function toMediaOutputView(output: GoMediaOutput): MediaOutputView {
  return {
    url: output.url,
    width: output.width,
    height: output.height,
    contentType: output.content_type,
  };
}

export function toMediaStatus(status: GoMediaStatus): MediaStatus {
  return {
    assetId: status.asset_id,
    uploadId: status.upload_id,
    jobId: status.job_id,
    status: toProcessingStatus(status.status),
    attemptCount: status.attempt_count,
    stage: toProcessingStage(status.stage),
    original: {
      filename: status.original.filename,
      declaredContentType: status.original.declared_content_type,
      detectedContentType: status.original.detected_content_type ?? null,
      sizeBytes: status.original.size_bytes,
      sha256: status.original.sha256 ?? null,
    },
    outputs: status.outputs
      ? {
          thumbnail: toMediaOutputView(status.outputs.thumbnail),
          web: toMediaOutputView(status.outputs.web),
          expiresAt: status.outputs.expires_at,
        }
      : null,
    error: status.error ? { code: status.error.code, message: status.error.message } : null,
    acceptedAt: status.accepted_at,
    startedAt: status.started_at ?? null,
    completedAt: status.completed_at ?? null,
  };
}

export const MEDIA_CONTENT_TYPES: readonly MediaContentType[] = ["image/jpeg", "image/png"];

export const MIN_UPLOAD_SIZE_BYTES = 1;
export const MAX_UPLOAD_SIZE_BYTES = 50_000_000;

export const SHA256_BASE64_PATTERN = "^[A-Za-z0-9+/]{43}=$";

const base64Sha256Pattern = new RegExp(SHA256_BASE64_PATTERN);

/**
 * The declared checksum is required at session creation and must be exactly a
 * base64-encoded SHA-256. Strict syntax here keeps a malformed declaration from
 * reaching the signing path.
 */
export function isValidBase64Sha256(value: unknown): value is string {
  return typeof value === "string" && base64Sha256Pattern.test(value);
}

export function isAdmittedContentType(value: unknown): value is MediaContentType {
  return typeof value === "string" && (MEDIA_CONTENT_TYPES as readonly string[]).includes(value);
}

export function isAdmittedSize(value: unknown): value is number {
  return (
    typeof value === "number" &&
    Number.isInteger(value) &&
    value >= MIN_UPLOAD_SIZE_BYTES &&
    value <= MAX_UPLOAD_SIZE_BYTES
  );
}

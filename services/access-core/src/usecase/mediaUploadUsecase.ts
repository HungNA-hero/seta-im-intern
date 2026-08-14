import { GraphQLError } from "graphql";
import { mediaAssetFetch, unwrap204, unwrapEnvelope } from "../clients/assetClient";
import type {
  GoMediaCommitAcceptance,
  GoUploadSession,
  MediaCommitAcceptance,
  MediaContentType,
  UploadSession,
  UploadSessionCreation,
} from "../domain/media";
import {
  isAdmittedContentType,
  isAdmittedSize,
  isValidBase64Sha256,
  toProcessingStatus,
  toUploadSession,
} from "../domain/media";
import type { GraphQLContext } from "../graphql/context";
import { assertCan, assertOrgMember } from "../graphql/context";
import {
  createAuthorizedAssetFetch,
  type AssetAuthorizationPrecondition,
  type AuthorizedAssetFetch,
} from "./authorizedAssetFetch";

export interface CreateMediaSessionInput {
  idempotencyKey: string;
  filename: string;
  contentType: MediaContentType;
  sizeBytes: number;
  checksumSha256: string;
}

export interface MediaUploadOperations {
  createSession(ctx: GraphQLContext, assetId: string, input: CreateMediaSessionInput): Promise<UploadSessionCreation>;
  getSession(ctx: GraphQLContext, assetId: string, uploadId: string): Promise<UploadSession>;
  refreshSession(ctx: GraphQLContext, assetId: string, uploadId: string): Promise<UploadSession>;
  cancelSession(ctx: GraphQLContext, assetId: string, uploadId: string): Promise<void>;
  commit(ctx: GraphQLContext, assetId: string, uploadId: string): Promise<MediaCommitAcceptance>;
}

export interface MediaSessionRateLimiter {
  consumeSessionCreation(input: { userId: string; orgId: string }): Promise<void>;
}

export interface MediaUploadUsecaseDependencies {
  authorizedAsset: AuthorizedAssetFetch;
  rateLimiter: MediaSessionRateLimiter;
}

const writeMedia = (assetId: string) => ({
  action: "write" as const,
  resourceType: "metadata_item" as const,
  resourceId: assetId,
});

const alreadyAuthorized: AssetAuthorizationPrecondition[] = [];

export function createMediaUploadUsecase({
  authorizedAsset,
  rateLimiter,
}: MediaUploadUsecaseDependencies): MediaUploadOperations {
  function requireMember(
    ctx: GraphQLContext,
  ): asserts ctx is GraphQLContext & { userId: string; currentOrgId: string } {
    assertOrgMember(ctx);
  }

  async function request(
    ctx: GraphQLContext,
    assetId: string,
    path: string,
    init: {
      method?: "GET" | "POST" | "PUT" | "DELETE";
      body?: Record<string, unknown>;
      idempotencyKey?: string;
    } = {},
    preconditions: AssetAuthorizationPrecondition[] = [writeMedia(assetId)],
  ): Promise<Response> {
    requireMember(ctx);
    return authorizedAsset.authorizedFetch({
      ctx,
      orgId: ctx.currentOrgId,
      path,
      preconditions,
      init,
    });
  }

  return {
    async createSession(ctx, assetId, input) {
      requireMember(ctx);
      if (
        !input.filename ||
        !isAdmittedContentType(input.contentType) ||
        !isAdmittedSize(input.sizeBytes) ||
        !isValidBase64Sha256(input.checksumSha256)
      ) {
        throw new GraphQLError("Invalid media upload declaration", { extensions: { code: "BAD_USER_INPUT" } });
      }
      await authorizedAsset.assertPreconditions({
        ctx,
        orgId: ctx.currentOrgId,
        preconditions: [writeMedia(assetId)],
      });
      await rateLimiter.consumeSessionCreation({ userId: ctx.userId, orgId: ctx.currentOrgId });
      const response = await request(
        ctx,
        assetId,
        `/internal/api/v1/metadata-items/${assetId}/media/uploads`,
        {
          method: "POST",
          idempotencyKey: input.idempotencyKey,
          body: {
            filename: input.filename,
            content_type: input.contentType,
            size_bytes: input.sizeBytes,
            checksum_sha256: input.checksumSha256,
          },
        },
        alreadyAuthorized,
      );
      const replayed = response.status === 200;
      const session = await unwrapEnvelope(
        response,
        "data",
        (raw) => toUploadSession(assertGoUploadSession(raw), `/api/v1/assets/${assetId}/media`),
        "Create media upload session",
      );
      return { ...session, replayed };
    },

    async getSession(ctx, assetId, uploadId) {
      const response = await request(
        ctx,
        assetId,
        `/internal/api/v1/metadata-items/${assetId}/media/uploads/${uploadId}`,
      );
      return unwrapEnvelope(
        response,
        "data",
        (raw) => toUploadSession(assertGoUploadSession(raw), `/api/v1/assets/${assetId}/media`),
        "Get media upload session",
      );
    },

    async refreshSession(ctx, assetId, uploadId) {
      const response = await request(
        ctx,
        assetId,
        `/internal/api/v1/metadata-items/${assetId}/media/uploads/${uploadId}/refresh`,
        { method: "POST" },
      );
      return unwrapEnvelope(
        response,
        "data",
        (raw) => toUploadSession(assertGoUploadSession(raw), `/api/v1/assets/${assetId}/media`),
        "Refresh media upload session",
      );
    },

    async cancelSession(ctx, assetId, uploadId) {
      const response = await request(
        ctx,
        assetId,
        `/internal/api/v1/metadata-items/${assetId}/media/uploads/${uploadId}`,
        { method: "DELETE" },
      );
      await unwrap204(response, "Cancel media upload session");
    },

    async commit(ctx, assetId, uploadId) {
      const response = await request(ctx, assetId, `/internal/api/v1/metadata-items/${assetId}/media`, {
        method: "PUT",
        body: { upload_id: uploadId },
      });
      const replayed = response.status === 200;
      const result = await unwrapEnvelope(response, "data", assertGoCommitAcceptance, "Commit media upload");
      const status = toProcessingStatus(result.status);
      if (status !== "QUEUED") throwInvalidAssetMediaResponse();
      return {
        assetId: result.asset_id,
        uploadId: result.upload_id,
        jobId: result.job_id,
        status,
        original: {
          filename: result.original.filename,
          contentType: result.original.content_type,
          sizeBytes: result.original.size_bytes,
        },
        acceptedAt: result.accepted_at,
        replayed,
      };
    },
  };
}

function assertRecord(value: unknown): asserts value is Record<string, any> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throwInvalidAssetMediaResponse();
}

function assertGoUploadSession(value: unknown): GoUploadSession {
  assertRecord(value);
  if (
    typeof value.upload_id !== "string" ||
    typeof value.asset_id !== "string" ||
    typeof value.state !== "string" ||
    typeof value.session_expires_at !== "string" ||
    (value.upload !== null && value.upload !== undefined && typeof value.upload !== "object")
  ) {
    throwInvalidAssetMediaResponse();
  }
  return value as GoUploadSession;
}

function assertGoCommitAcceptance(value: unknown): GoMediaCommitAcceptance {
  assertRecord(value);
  assertRecord(value.original);
  if (
    typeof value.asset_id !== "string" ||
    typeof value.upload_id !== "string" ||
    typeof value.job_id !== "string" ||
    typeof value.status !== "string" ||
    typeof value.accepted_at !== "string" ||
    typeof value.original.filename !== "string" ||
    !isAdmittedContentType(value.original.content_type) ||
    !isAdmittedSize(value.original.size_bytes)
  ) {
    throwInvalidAssetMediaResponse();
  }
  return value as GoMediaCommitAcceptance;
}

function throwInvalidAssetMediaResponse(): never {
  throw new GraphQLError("Asset Core returned an invalid media response", { extensions: { code: "INTERNAL_ERROR" } });
}

const authorizedMediaAsset = createAuthorizedAssetFetch({
  authorization: { assertAllowed: assertCan },
  transport: { request: mediaAssetFetch },
});

export function createAuthorizedMediaUploadUsecase(rateLimiter: MediaSessionRateLimiter): MediaUploadOperations {
  return createMediaUploadUsecase({ authorizedAsset: authorizedMediaAsset, rateLimiter });
}

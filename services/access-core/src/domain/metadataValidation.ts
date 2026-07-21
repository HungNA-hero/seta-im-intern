import { GraphQLError } from "graphql";
import {
  MetadataConnectionSearchInput,
  MetadataSearchInput,
  NormalizedMetadataConnectionSearchInput,
  NormalizedMetadataSearchInput,
} from "./metadata";
import { decodeMetadataCursor } from "./metadataCursor";
import { badUserInput } from "../errors/factories";

const MAX_METADATA_PAGE_SIZE = 100;

export function normalizeMetadataSearchInput(
  input: MetadataSearchInput,
): NormalizedMetadataSearchInput {
  const normalized: NormalizedMetadataSearchInput = {
    limit: input.limit ?? 50,
    offset: input.offset ?? 0,
  };

  if (normalized.limit < 1 || normalized.limit > MAX_METADATA_PAGE_SIZE) {
    throw badUserInput(`limit must be between 1 and ${MAX_METADATA_PAGE_SIZE}`);
  }
  if (normalized.offset < 0 || normalized.offset > 10000) {
    throw badUserInput("offset must be between 0 and 10000");
  }

  if (input.folderId !== undefined && input.folderId !== null) {
    const folderId = input.folderId.trim();
    if (folderId) normalized.folderId = folderId;
  }

  if (input.query !== undefined && input.query !== null) {
    const query = input.query.trim();
    const queryLength = [...query].length;
    if (queryLength < 2 || queryLength > 200) {
      throw badUserInput("query must contain between 2 and 200 characters");
    }
    normalized.query = query;
  }

  if (input.labels !== undefined && input.labels !== null) {
    const labels = input.labels.map((label) => label.trim());
    if (labels.some((label) => label.length === 0)) {
      throw badUserInput("labels must not contain blank values");
    }
    if (labels.length > 0) normalized.labels = [...new Set(labels)];
  }

  if (input.category !== undefined && input.category !== null) {
    const category = input.category.trim();
    if (category) normalized.category = category;
  }

  if (input.externalSource !== undefined && input.externalSource !== null) {
    const externalSource = input.externalSource.trim();
    if (externalSource) normalized.externalSource = externalSource;
  }

  if (
    !normalized.folderId &&
    !normalized.query &&
    !normalized.labels?.length &&
    !normalized.category &&
    !normalized.externalSource
  ) {
    throw badUserInput("at least one search filter must be provided");
  }

  return normalized;
}

export function normalizeMetadataConnectionSearchInput(
  input: MetadataConnectionSearchInput,
): NormalizedMetadataConnectionSearchInput {
  const normalized = normalizeMetadataSearchInput({
    folderId: input.folderId,
    query: input.query,
    labels: input.labels,
    category: input.category,
    externalSource: input.externalSource,
    limit: input.first ?? 50,
    offset: 0,
  });
  if (!normalized.folderId) {
    throw badUserInput("folderId is required for cursor pagination");
  }

  return {
    folderId: normalized.folderId,
    query: normalized.query,
    labels: normalized.labels,
    category: normalized.category,
    externalSource: normalized.externalSource,
    first: normalized.limit,
    after:
      input.after === undefined || input.after === null
        ? undefined
        : decodeMetadataCursor(input.after),
  };
}

export function validateAndParseJsonString(
  jsonString?: string | null,
): Record<string, unknown> | null | undefined {
  if (jsonString === undefined) return undefined;
  if (jsonString === null) return null;
  try {
    const parsed = JSON.parse(jsonString);
    if (
      typeof parsed !== "object" ||
      parsed === null ||
      Array.isArray(parsed)
    ) {
      throw badUserInput("metadataJson must be a JSON object string");
    }
    return parsed as Record<string, unknown>;
  } catch (error) {
    if (error instanceof GraphQLError) throw error;
    throw badUserInput("metadataJson must be a valid JSON string");
  }
}

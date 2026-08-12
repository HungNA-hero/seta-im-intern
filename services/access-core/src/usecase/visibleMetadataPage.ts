import {
  GoCursorSearchEnvelope,
  GoMetadataItem,
  metadataHierarchy,
  NormalizedMetadataConnectionSearchInput,
  toMetadataItem,
} from "../domain/metadata";
import { encodeMetadataCursor, MetadataCursorPosition } from "../domain/metadataCursor";
import { internalError } from "../errors/factories";
import { GraphQLContext } from "../graphql/context";
import { MetadataCandidatePageRequest } from "./metadataCandidatePage";

const CURSOR_CANDIDATE_LOOKAHEAD = 1;
const MAX_AUTHORIZATION_CANDIDATE_BATCHES = 10;

export type AuthenticatedContext = GraphQLContext & { userId: string };

export interface VisibleMetadataPageRequest {
  ctx: AuthenticatedContext;
  orgId: string;
  filters: NormalizedMetadataConnectionSearchInput;
}

export interface VisibleMetadataFilterRequest {
  userId: string;
  orgId: string;
  action: "read";
  resourceType: "metadata_item";
  items: GoMetadataItem[];
  getHierarchy: (item: GoMetadataItem) => { ancestorIds: string[] };
}

export interface VisibleMetadataPageDependencies {
  fetchCandidatePage(request: MetadataCandidatePageRequest): Promise<GoCursorSearchEnvelope>;
  getFolderAncestors(orgId: string, userId: string, folderId: string): Promise<string[]>;
  filterVisible(request: VisibleMetadataFilterRequest): Promise<GoMetadataItem[]>;
}

export interface VisibleMetadataPageReader {
  readVisibleMetadataPage(request: VisibleMetadataPageRequest): Promise<MetadataConnection>;
}

export interface MetadataConnection {
  nodes: ReturnType<typeof toMetadataItem>[];
  pageInfo: {
    endCursor: string | null;
    hasNextPage: boolean;
  };
}

export function createVisibleMetadataPageReader(
  dependencies: VisibleMetadataPageDependencies,
): VisibleMetadataPageReader {
  return {
    async readVisibleMetadataPage({ ctx, orgId, filters }: VisibleMetadataPageRequest): Promise<MetadataConnection> {
      const candidateBatchSize = filters.first + CURSOR_CANDIDATE_LOOKAHEAD;
      const ancestorIds = await dependencies.getFolderAncestors(orgId, ctx.userId, filters.folderId);
      const getHierarchy = metadataHierarchy(new Map([[filters.folderId, ancestorIds]]));

      const visible: GoMetadataItem[] = [];
      let scanAfter: MetadataCursorPosition | undefined = filters.after;

      for (let batch = 0; batch < MAX_AUTHORIZATION_CANDIDATE_BATCHES; batch += 1) {
        const candidatePage = await dependencies.fetchCandidatePage({
          ctx,
          orgId,
          filters,
          batchSize: candidateBatchSize,
          after: scanAfter,
        });
        assertCandidatePageAdvances(candidatePage);
        assertCandidatesWithinFolder(candidatePage.items, filters.folderId);

        const authorized = await dependencies.filterVisible({
          userId: ctx.userId,
          orgId,
          action: "read",
          resourceType: "metadata_item",
          items: candidatePage.items,
          getHierarchy,
        });
        visible.push(...authorized);

        if (visible.length >= candidateBatchSize) {
          return toMetadataConnection(visible, filters.first, true);
        }
        if (!candidatePage.hasMore) {
          return toMetadataConnection(visible, filters.first, false);
        }

        scanAfter = positionAfterLastCandidate(candidatePage);
      }

      throw internalError();
    },
  };
}

function assertCandidatePageAdvances(candidatePage: GoCursorSearchEnvelope): void {
  if (candidatePage.items.length === 0 && candidatePage.hasMore) {
    throw internalError();
  }
}

function assertCandidatesWithinFolder(candidates: GoMetadataItem[], folderId: string): void {
  if (candidates.some((candidate) => candidate.folder_id !== folderId)) {
    throw internalError();
  }
}

function positionAfterLastCandidate(candidatePage: GoCursorSearchEnvelope): MetadataCursorPosition {
  const lastCandidate = candidatePage.items[candidatePage.items.length - 1];
  return {
    updatedAt: lastCandidate.updated_at,
    id: lastCandidate.id,
  };
}

function toMetadataConnection(
  visible: GoMetadataItem[],
  requestedNodeCount: number,
  hasNextPage: boolean,
): MetadataConnection {
  const nodes = visible.slice(0, requestedNodeCount);
  const lastNode = nodes[nodes.length - 1];
  return {
    nodes: nodes.map(toMetadataItem),
    pageInfo: {
      endCursor:
        lastNode === undefined
          ? null
          : encodeMetadataCursor({
              updatedAt: lastNode.updated_at,
              id: lastNode.id,
            }),
      hasNextPage,
    },
  };
}

import { filterVisible } from "../authz/decision";
import { GoRecycleBinEntry, NormalizedRecycleBinConnectionInput, toRecycleBinEntry } from "../domain/recycleBin";
import { encodeRecycleBinCursor, RecycleBinCursorPosition } from "../domain/recycleBinCursor";
import { ancestorIdsFromPath, folderIdsFromPath } from "../domain/ltreePath";
import { internalError } from "../errors/factories";
import { GraphQLContext } from "../graphql/context";
import { RecycleBinCandidatePageRequest } from "./recycleBinCandidatePage";

const CURSOR_CANDIDATE_LOOKAHEAD = 1;
const MAX_AUTHORIZATION_CANDIDATE_BATCHES = 10;

export type AuthenticatedRecycleBinContext = GraphQLContext & { userId: string };

export interface VisibleRecycleBinPageRequest {
  ctx: AuthenticatedRecycleBinContext;
  orgId: string;
  input: NormalizedRecycleBinConnectionInput;
}

export interface VisibleRecycleBinPageDependencies {
  fetchCandidatePage(
    request: RecycleBinCandidatePageRequest,
  ): Promise<{ entries: GoRecycleBinEntry[]; hasMore: boolean }>;
  filterVisible: typeof filterVisible;
}

export interface RecycleBinConnection {
  nodes: ReturnType<typeof toRecycleBinEntry>[];
  pageInfo: {
    endCursor: string | null;
    hasNextPage: boolean;
  };
}

export interface VisibleRecycleBinPageReader {
  readVisibleRecycleBinPage(request: VisibleRecycleBinPageRequest): Promise<RecycleBinConnection>;
}

export function createVisibleRecycleBinPageReader(
  dependencies: VisibleRecycleBinPageDependencies,
): VisibleRecycleBinPageReader {
  return {
    async readVisibleRecycleBinPage({
      ctx,
      orgId,
      input,
    }: VisibleRecycleBinPageRequest): Promise<RecycleBinConnection> {
      const candidateBatchSize = input.first + CURSOR_CANDIDATE_LOOKAHEAD;
      const visible: GoRecycleBinEntry[] = [];
      let scanAfter: RecycleBinCursorPosition | undefined = input.after;

      for (let batch = 0; batch < MAX_AUTHORIZATION_CANDIDATE_BATCHES; batch += 1) {
        const candidatePage = await dependencies.fetchCandidatePage({
          ctx,
          orgId,
          batchSize: candidateBatchSize,
          after: scanAfter,
        });
        assertCandidatePageAdvances(candidatePage);
        visible.push(
          ...(await filterRecycleBinCandidates(dependencies.filterVisible, ctx.userId, orgId, candidatePage.entries)),
        );

        if (visible.length >= candidateBatchSize) {
          return toRecycleBinConnection(visible, input.first, true);
        }
        if (!candidatePage.hasMore) {
          return toRecycleBinConnection(visible, input.first, false);
        }

        scanAfter = positionAfterLastCandidate(candidatePage.entries);
      }

      throw internalError();
    },
  };
}

async function filterRecycleBinCandidates(
  filter: typeof filterVisible,
  userId: string,
  orgId: string,
  entries: GoRecycleBinEntry[],
): Promise<GoRecycleBinEntry[]> {
  const folders = entries.filter((entry) => entry.resource_type === "FOLDER");
  const metadata = entries.filter((entry) => entry.resource_type === "METADATA");
  const [visibleFolders, visibleMetadata] = await Promise.all([
    filter({
      userId,
      orgId,
      action: "read",
      resourceType: "folder",
      items: folders.map((entry) => ({ id: entry.resource_id, entry })),
      getHierarchy: ({ entry }) => ({ ancestorIds: ancestorIdsFromPath(entry.root_folder_path) }),
    }),
    filter({
      userId,
      orgId,
      action: "read",
      resourceType: "metadata_item",
      items: metadata.map((entry) => ({ id: entry.resource_id, entry })),
      getHierarchy: ({ entry }) => ({ ancestorIds: folderIdsFromPath(entry.root_folder_path) }),
    }),
  ]);
  const allowedKeys = new Set(
    [...visibleFolders, ...visibleMetadata].map(({ entry }) => `${entry.resource_type}:${entry.resource_id}`),
  );
  return entries.filter((entry) => allowedKeys.has(`${entry.resource_type}:${entry.resource_id}`));
}

function assertCandidatePageAdvances(candidatePage: { entries: GoRecycleBinEntry[]; hasMore: boolean }): void {
  if (candidatePage.entries.length === 0 && candidatePage.hasMore) {
    throw internalError();
  }
}

function positionAfterLastCandidate(entries: GoRecycleBinEntry[]): RecycleBinCursorPosition {
  const lastCandidate = entries[entries.length - 1];
  return { deletedAt: lastCandidate.deleted_at, lifecycleUnitId: lastCandidate.lifecycle_unit_id };
}

function toRecycleBinConnection(
  visible: GoRecycleBinEntry[],
  requestedNodeCount: number,
  hasNextPage: boolean,
): RecycleBinConnection {
  const nodes = visible.slice(0, requestedNodeCount);
  const lastNode = nodes[nodes.length - 1];
  return {
    nodes: nodes.map(toRecycleBinEntry),
    pageInfo: {
      endCursor:
        lastNode === undefined
          ? null
          : encodeRecycleBinCursor({
              deletedAt: lastNode.deleted_at,
              lifecycleUnitId: lastNode.lifecycle_unit_id,
            }),
      hasNextPage,
    },
  };
}

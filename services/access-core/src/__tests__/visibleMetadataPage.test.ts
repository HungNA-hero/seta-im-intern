import { describe, expect, test, vi } from "vitest";
import {
  AuthenticatedContext,
  createVisibleMetadataPageReader,
  VisibleMetadataFilterRequest,
  VisibleMetadataPageDependencies,
} from "../usecase/visibleMetadataPage";
import { GoCursorSearchEnvelope, GoMetadataItem, NormalizedMetadataConnectionSearchInput } from "../domain/metadata";
import { decodeMetadataCursor } from "../domain/metadataCursor";

const FOLDER_ID = "00000000-0000-4000-8000-0000000000f0";
const OTHER_FOLDER_ID = "00000000-0000-4000-8000-0000000000f1";
const ORG_ID = "00000000-0000-4000-8000-0000000000a0";
const ANCESTOR_IDS = ["00000000-0000-4000-8000-0000000000e0"];

const ctx = { userId: "00000000-0000-4000-8000-0000000000u0" } as unknown as AuthenticatedContext;

function candidate(index: number, folderId: string = FOLDER_ID): GoMetadataItem {
  return {
    id: `00000000-0000-4000-8000-00000000000${index}`,
    folder_id: folderId,
    updated_at: `2026-07-1${index}T10:11:12Z`,
  } as GoMetadataItem;
}

function page(items: GoMetadataItem[], hasMore: boolean): GoCursorSearchEnvelope {
  return { items, hasMore };
}

function filters(overrides: Partial<NormalizedMetadataConnectionSearchInput> = {}) {
  return { folderId: FOLDER_ID, first: 2, ...overrides } as NormalizedMetadataConnectionSearchInput;
}

function createReader(overrides: Partial<VisibleMetadataPageDependencies> = {}) {
  const fetchCandidatePage = vi.fn().mockResolvedValue(page([], false));
  const getFolderAncestors = vi.fn().mockResolvedValue(ANCESTOR_IDS);
  const filterVisible = vi.fn(async ({ items }: { items: GoMetadataItem[] }) => items);
  const dependencies = { fetchCandidatePage, getFolderAncestors, filterVisible, ...overrides };
  return { reader: createVisibleMetadataPageReader(dependencies), ...dependencies };
}

describe("visible metadata page reader", () => {
  test("rejects a candidate outside the requested folder instead of silently dropping its grants", async () => {
    const { reader } = createReader({
      fetchCandidatePage: vi.fn().mockResolvedValue(page([candidate(1), candidate(2, OTHER_FOLDER_ID)], false)),
    });

    await expect(reader.readVisibleMetadataPage({ ctx, orgId: ORG_ID, filters: filters() })).rejects.toMatchObject({
      extensions: { code: "INTERNAL_ERROR" },
    });
  });

  test("requests one candidate more than the caller asked for", async () => {
    const { reader, fetchCandidatePage } = createReader();

    await reader.readVisibleMetadataPage({ ctx, orgId: ORG_ID, filters: filters({ first: 5 }) });

    expect(fetchCandidatePage).toHaveBeenCalledWith(expect.objectContaining({ batchSize: 6 }));
  });

  test("reports a next page once the lookahead candidate is visible", async () => {
    const { reader } = createReader({
      fetchCandidatePage: vi.fn().mockResolvedValue(page([candidate(1), candidate(2), candidate(3)], true)),
    });

    const connection = await reader.readVisibleMetadataPage({ ctx, orgId: ORG_ID, filters: filters() });

    expect(connection.nodes).toHaveLength(2);
    expect(connection.pageInfo.hasNextPage).toBe(true);
  });

  test("reports no next page when the source is exhausted below the lookahead", async () => {
    const { reader } = createReader({
      fetchCandidatePage: vi.fn().mockResolvedValue(page([candidate(1), candidate(2)], false)),
    });

    const connection = await reader.readVisibleMetadataPage({ ctx, orgId: ORG_ID, filters: filters() });

    expect(connection.nodes).toHaveLength(2);
    expect(connection.pageInfo.hasNextPage).toBe(false);
  });

  test("advances the scan by the last candidate, not the last visible item", async () => {
    const firstBatch = page([candidate(1), candidate(2), candidate(3)], true);
    const fetchCandidatePage = vi
      .fn()
      .mockResolvedValueOnce(firstBatch)
      .mockResolvedValueOnce(page([candidate(4)], false));
    const { reader } = createReader({
      fetchCandidatePage,
      filterVisible: vi.fn(async ({ items }: { items: GoMetadataItem[] }) => items.slice(0, 1)),
    });

    await reader.readVisibleMetadataPage({ ctx, orgId: ORG_ID, filters: filters() });

    expect(fetchCandidatePage).toHaveBeenLastCalledWith(
      expect.objectContaining({
        after: { updatedAt: candidate(3).updated_at, id: candidate(3).id },
      }),
    );
  });

  test("fails closed rather than scanning without bound", async () => {
    const { reader, fetchCandidatePage } = createReader({
      fetchCandidatePage: vi.fn().mockResolvedValue(page([candidate(1)], true)),
      filterVisible: vi.fn(async () => []),
    });

    await expect(reader.readVisibleMetadataPage({ ctx, orgId: ORG_ID, filters: filters() })).rejects.toMatchObject({
      extensions: { code: "INTERNAL_ERROR" },
    });
    expect(fetchCandidatePage).toHaveBeenCalledTimes(10);
  });

  test("rejects a page that claims more results but cannot advance the cursor", async () => {
    const { reader } = createReader({
      fetchCandidatePage: vi.fn().mockResolvedValue(page([], true)),
    });

    await expect(reader.readVisibleMetadataPage({ ctx, orgId: ORG_ID, filters: filters() })).rejects.toMatchObject({
      extensions: { code: "INTERNAL_ERROR" },
    });
  });

  test("ends the cursor at the last returned node, not the last candidate examined", async () => {
    const { reader } = createReader({
      fetchCandidatePage: vi.fn().mockResolvedValue(page([candidate(1), candidate(2), candidate(3)], true)),
    });

    const connection = await reader.readVisibleMetadataPage({ ctx, orgId: ORG_ID, filters: filters() });

    expect(connection.pageInfo.endCursor).not.toBeNull();
    expect(decodeMetadataCursor(connection.pageInfo.endCursor as string)).toEqual({
      updatedAt: candidate(2).updated_at,
      id: candidate(2).id,
    });
  });

  test("resolves folder ancestry once for the whole scan", async () => {
    let observedHierarchy: VisibleMetadataFilterRequest["getHierarchy"] | undefined;
    const filterVisible = async (request: VisibleMetadataFilterRequest) => {
      observedHierarchy = request.getHierarchy;
      return request.items;
    };
    const { reader, getFolderAncestors } = createReader({
      fetchCandidatePage: vi
        .fn()
        .mockResolvedValueOnce(page([candidate(1)], true))
        .mockResolvedValueOnce(page([candidate(2)], false)),
      filterVisible,
    });

    await reader.readVisibleMetadataPage({ ctx, orgId: ORG_ID, filters: filters() });

    expect(getFolderAncestors).toHaveBeenCalledTimes(1);
    expect(getFolderAncestors).toHaveBeenCalledWith(ORG_ID, ctx.userId, FOLDER_ID);
    expect(observedHierarchy?.(candidate(1))).toEqual({ ancestorIds: [FOLDER_ID, ...ANCESTOR_IDS] });
  });
});

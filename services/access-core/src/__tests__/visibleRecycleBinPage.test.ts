import { describe, expect, test, vi } from "vitest";
import { GoRecycleBinEntry, NormalizedRecycleBinConnectionInput } from "../domain/recycleBin";
import { decodeRecycleBinCursor } from "../domain/recycleBinCursor";
import {
  AuthenticatedRecycleBinContext,
  createVisibleRecycleBinPageReader,
  VisibleRecycleBinPageDependencies,
} from "../usecase/visibleRecycleBinPage";

const ORG_ID = "00000000-0000-4000-8000-0000000000a0";
const USER_ID = "00000000-0000-4000-8000-0000000000b0";
const PARENT_ID = "00000000-0000-4000-8000-0000000000c0";
const ROOT_ID = "00000000-0000-4000-8000-0000000000d0";
const parentPath = PARENT_ID.replaceAll("-", "");
const rootPath = `${parentPath}.${ROOT_ID.replaceAll("-", "")}`;
const ctx = { userId: USER_ID } as AuthenticatedRecycleBinContext;

function uuid(index: number): string {
  return `00000000-0000-4000-8000-${index.toString().padStart(12, "0")}`;
}

function entry(index: number, resourceType: "FOLDER" | "METADATA" = "FOLDER"): GoRecycleBinEntry {
  return {
    lifecycle_unit_id: uuid(100 + index),
    resource_type: resourceType,
    resource_id: resourceType === "FOLDER" && index === 1 ? ROOT_ID : uuid(index),
    display_name: `deleted-${index}`,
    root_folder_path: resourceType === "FOLDER" ? rootPath : parentPath,
    deleted_at: `2026-08-12T10:00:0${index}Z`,
  };
}

function page(entries: GoRecycleBinEntry[], hasMore: boolean) {
  return { entries, hasMore };
}

function input(overrides: Partial<NormalizedRecycleBinConnectionInput> = {}) {
  return { first: 2, ...overrides } as NormalizedRecycleBinConnectionInput;
}

function createReader(overrides: Partial<VisibleRecycleBinPageDependencies> = {}) {
  const fetchCandidatePage = vi.fn().mockResolvedValue(page([], false));
  const filterVisible = vi.fn(async ({ items }: { items: Array<{ id: string; entry: GoRecycleBinEntry }> }) => items);
  const dependencies: VisibleRecycleBinPageDependencies = {
    fetchCandidatePage,
    filterVisible: filterVisible as unknown as VisibleRecycleBinPageDependencies["filterVisible"],
    ...overrides,
  };
  return {
    reader: createVisibleRecycleBinPageReader(dependencies),
    fetchCandidatePage: dependencies.fetchCandidatePage,
    filterVisible: dependencies.filterVisible,
  };
}

describe("visible recycle bin page reader", () => {
  test("requests one candidate more than the caller asked for and encodes the last returned node", async () => {
    const candidates = [entry(1), entry(2), entry(3)];
    const { reader, fetchCandidatePage } = createReader({
      fetchCandidatePage: vi.fn().mockResolvedValue(page(candidates, true)),
    });

    const result = await reader.readVisibleRecycleBinPage({ ctx, orgId: ORG_ID, input: input() });

    expect(fetchCandidatePage).toHaveBeenCalledWith(expect.objectContaining({ batchSize: 3 }));
    expect(result.nodes).toHaveLength(2);
    expect(result.pageInfo.hasNextPage).toBe(true);
    expect(decodeRecycleBinCursor(result.pageInfo.endCursor as string)).toEqual({
      deletedAt: candidates[1].deleted_at,
      lifecycleUnitId: candidates[1].lifecycle_unit_id,
    });
  });

  test("filters denied candidates before GraphQL output and scans to fill the visible page", async () => {
    const firstBatch = [entry(1), entry(2), entry(3)];
    const secondBatch = [entry(4), entry(5)];
    const fetchCandidatePage = vi
      .fn()
      .mockResolvedValueOnce(page(firstBatch, true))
      .mockResolvedValueOnce(page(secondBatch, false));
    const filterVisible = vi.fn(async ({ items }: { items: Array<{ id: string; entry: GoRecycleBinEntry }> }) =>
      items.filter(({ entry: candidate }) => candidate.resource_id !== firstBatch[0].resource_id),
    );
    const { reader } = createReader({
      fetchCandidatePage,
      filterVisible: filterVisible as unknown as VisibleRecycleBinPageDependencies["filterVisible"],
    });

    const result = await reader.readVisibleRecycleBinPage({ ctx, orgId: ORG_ID, input: input({ first: 3 }) });

    expect(result.nodes.map((node) => node.resourceId)).not.toContain(firstBatch[0].resource_id);
    expect(result.nodes).toHaveLength(3);
    expect(fetchCandidatePage).toHaveBeenLastCalledWith(
      expect.objectContaining({
        after: { deletedAt: firstBatch[2].deleted_at, lifecycleUnitId: firstBatch[2].lifecycle_unit_id },
      }),
    );
  });

  test("uses the stored path for inherited folder and metadata read grants", async () => {
    const folder = entry(1, "FOLDER");
    const metadata = entry(2, "METADATA");
    const observedHierarchies: Array<{ resourceType: string; ancestors: string[] }> = [];
    const filterVisible = vi.fn(async (request: any) => {
      if (request.items.length > 0) {
        observedHierarchies.push({
          resourceType: request.resourceType,
          ancestors: request.getHierarchy(request.items[0]).ancestorIds,
        });
      }
      return request.items;
    });
    const { reader } = createReader({
      fetchCandidatePage: vi.fn().mockResolvedValue(page([folder, metadata], false)),
      filterVisible: filterVisible as unknown as VisibleRecycleBinPageDependencies["filterVisible"],
    });

    await reader.readVisibleRecycleBinPage({ ctx, orgId: ORG_ID, input: input() });

    expect(observedHierarchies).toEqual([
      { resourceType: "folder", ancestors: [PARENT_ID] },
      { resourceType: "metadata_item", ancestors: [PARENT_ID] },
    ]);
  });

  test("fails closed after ten batches rather than scanning without a bound", async () => {
    const { reader, fetchCandidatePage } = createReader({
      fetchCandidatePage: vi.fn().mockResolvedValue(page([entry(1)], true)),
      filterVisible: vi.fn(async () => []) as unknown as VisibleRecycleBinPageDependencies["filterVisible"],
    });

    await expect(reader.readVisibleRecycleBinPage({ ctx, orgId: ORG_ID, input: input() })).rejects.toMatchObject({
      extensions: { code: "INTERNAL_ERROR" },
    });
    expect(fetchCandidatePage).toHaveBeenCalledTimes(10);
  });
});

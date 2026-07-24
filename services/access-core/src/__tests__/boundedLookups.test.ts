import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../cache/factCache", () => ({
  readFolderFactThrough: (
    _orgId: string,
    _id: string,
    loader: () => Promise<unknown>,
  ) => loader(),
  readItemFactThrough: (
    _orgId: string,
    _id: string,
    loader: () => Promise<unknown>,
  ) => loader(),
}));

import { getFolderMetaBatch } from "../clients/assetClient";
import { config } from "../config";

describe("getFolderMetaBatch bounded lookups", () => {
  const mockFetch = vi.fn();
  const originalFetch = global.fetch;

  beforeEach(() => {
    global.fetch = mockFetch;
    mockFetch.mockReset();
  });

  afterEach(() => {
    global.fetch = originalFetch;
  });

  it("issues exactly one HTTP request for a page's worth of distinct folder ids", async () => {
    const ids = Array.from({ length: 25 }, (_, i) => `folder-${i}`);
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        status: "success",
        folders: ids.map((id) => ({ id, path: `root.${id}` })),
      }),
    });

    const result = await getFolderMetaBatch("org-1", "user-1", ids);

    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url] = mockFetch.mock.calls[0];
    for (const id of ids) {
      expect(url).toContain(`id=${id}`);
    }
    expect(result.size).toBe(ids.length);
    expect(result.get("folder-3")).toEqual({ path: "root.folder-3" });
  });

  it("deduplicates repeated ids before issuing the request", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        status: "success",
        folders: [
          { id: "a", path: "root.a" },
          { id: "b", path: "root.b" },
        ],
      }),
    });

    await getFolderMetaBatch("org-1", "user-1", ["a", "a", "b", "a"]);

    const [url] = mockFetch.mock.calls[0];
    expect((url.match(/id=/g) ?? []).length).toBe(2);
  });

  it("returns an empty map without a network call for an empty id list", async () => {
    const result = await getFolderMetaBatch("org-1", "user-1", []);
    expect(result.size).toBe(0);
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it("falls back to the single-id endpoint for exactly one id", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ folder: { path: "root.only" } }),
    });

    const result = await getFolderMetaBatch("org-1", "user-1", ["only"]);

    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url] = mockFetch.mock.calls[0];
    expect(url).toContain(`${config.goAssetUrl}/internal/api/v1/folders`);
    expect((url.match(/id=/g) ?? []).length).toBe(1);
    expect(result.get("only")).toEqual({ path: "root.only" });
  });
});

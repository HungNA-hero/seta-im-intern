import { describe, expect, test, vi } from "vitest";
import {
  AssetAuthorizationPrecondition,
  AuthorizedAssetContext,
  createAuthorizedAssetFetch,
} from "../usecase/authorizedAssetFetch";

const ORG_ID = "org-1";
const PATH = "/internal/api/v1/folders";

function createFetcher(assertAllowed = vi.fn(async () => {})) {
  const request = vi.fn(async () => new Response(null, { status: 200 }));
  const fetcher = createAuthorizedAssetFetch({
    authorization: { assertAllowed },
    transport: { request },
  });
  return { fetcher, request, assertAllowed };
}

function context(overrides: Partial<AuthorizedAssetContext> = {}): AuthorizedAssetContext {
  return { userId: "user-1", roles: [], ...overrides };
}

const readFolder: AssetAuthorizationPrecondition = {
  action: "read",
  resourceType: "folder",
  resourceId: "folder-1",
};

describe("authorized asset fetch", () => {
  test("elevates to org admin only when the caller actually holds org_admin", async () => {
    const { fetcher, request } = createFetcher();

    await fetcher.authorizedFetch({
      ctx: context({ roles: ["org_admin"] }),
      orgId: ORG_ID,
      path: PATH,
      init: { includeOrgAdmin: true },
    });

    expect(request).toHaveBeenCalledWith(PATH, expect.objectContaining({ orgAdmin: true }));
  });

  test("withholds org admin from a caller without the role even when the call asks for it", async () => {
    const { fetcher, request } = createFetcher();

    await fetcher.authorizedFetch({
      ctx: context({ roles: ["member"] }),
      orgId: ORG_ID,
      path: PATH,
      init: { includeOrgAdmin: true },
    });

    expect(request).toHaveBeenCalledWith(PATH, expect.objectContaining({ orgAdmin: false }));
  });

  test("withholds org admin from an org_admin caller when the call does not ask for it", async () => {
    const { fetcher, request } = createFetcher();

    await fetcher.authorizedFetch({ ctx: context({ roles: ["org_admin"] }), orgId: ORG_ID, path: PATH });

    expect(request).toHaveBeenCalledWith(PATH, expect.objectContaining({ orgAdmin: undefined }));
  });

  test("never reaches asset-core when a precondition denies", async () => {
    const assertAllowed = vi.fn(async () => {
      throw new Error("forbidden");
    });
    const { fetcher, request } = createFetcher(assertAllowed);

    await expect(
      fetcher.authorizedFetch({ ctx: context(), orgId: ORG_ID, path: PATH, preconditions: [readFolder] }),
    ).rejects.toThrow("forbidden");
    expect(request).not.toHaveBeenCalled();
  });

  test("checks every precondition, not only the first", async () => {
    const { fetcher, assertAllowed } = createFetcher();
    const write: AssetAuthorizationPrecondition = {
      action: "write",
      resourceType: "metadata_item",
      resourceId: "item-1",
    };

    await fetcher.authorizedFetch({
      ctx: context(),
      orgId: ORG_ID,
      path: PATH,
      preconditions: [readFolder, write],
    });

    expect(assertAllowed).toHaveBeenCalledTimes(2);
    expect(assertAllowed).toHaveBeenLastCalledWith({ userId: "user-1", ...write, orgId: ORG_ID });
  });

  test("rejects an unauthenticated caller before touching asset-core", async () => {
    const { fetcher, request, assertAllowed } = createFetcher();

    await expect(
      fetcher.authorizedFetch({ ctx: context({ userId: null }), orgId: ORG_ID, path: PATH }),
    ).rejects.toMatchObject({ extensions: { code: "UNAUTHENTICATED" } });
    expect(request).not.toHaveBeenCalled();
    expect(assertAllowed).not.toHaveBeenCalled();
  });

  test("passes method and body through to the transport unchanged", async () => {
    const { fetcher, request } = createFetcher();
    const body = { title: "a" };

    await fetcher.authorizedFetch({
      ctx: context(),
      orgId: ORG_ID,
      path: PATH,
      init: { method: "PATCH", body },
    });

    expect(request).toHaveBeenCalledWith(PATH, expect.objectContaining({ method: "PATCH", body }));
  });

  test("assertPreconditions stays callable on its own so callers can check permission before validating input", async () => {
    const { fetcher, assertAllowed, request } = createFetcher();

    await fetcher.assertPreconditions({ ctx: context(), orgId: ORG_ID, preconditions: [readFolder] });

    expect(assertAllowed).toHaveBeenCalledWith({ userId: "user-1", ...readFolder, orgId: ORG_ID });
    expect(request).not.toHaveBeenCalled();
  });
});

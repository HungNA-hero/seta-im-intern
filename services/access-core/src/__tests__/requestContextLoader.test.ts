import { describe, expect, test, vi } from "vitest";
import { createRequestContextLoader } from "../graphql/requestContextLoader";

describe("createRequestContextLoader", () => {
  test("loads context and writes the authorization snapshot through ports", async () => {
    const dataSource = {
      findUser: vi.fn().mockResolvedValue({
        isActive: true,
        isMember: true,
        roles: [{ id: "role-1", code: "member", orgId: "org-1" }],
      }),
      getOlpEnabled: vi.fn().mockResolvedValue(true),
    };
    const authorizationRequest = { begin: vi.fn() };
    const loader = createRequestContextLoader(dataSource, authorizationRequest);

    await expect(loader.load("user-1", "org-1")).resolves.toEqual({
      userId: "user-1",
      currentOrgId: "org-1",
      isMember: true,
      roles: ["member"],
      olpEnabled: true,
    });
    expect(authorizationRequest.begin).toHaveBeenCalledWith(
      expect.objectContaining({
        userId: "user-1",
        orgId: "org-1",
        roleIds: ["role-1"],
      }),
    );
  });

  test("returns an empty context without querying for an anonymous request", async () => {
    const dataSource = {
      findUser: vi.fn(),
      getOlpEnabled: vi.fn(),
    };
    const loader = createRequestContextLoader(dataSource, { begin: vi.fn() });

    await expect(loader.load(null, null)).resolves.toEqual({
      userId: null,
      currentOrgId: null,
      isMember: false,
      roles: [],
      olpEnabled: false,
    });
    expect(dataSource.findUser).not.toHaveBeenCalled();
  });
});

import { describe, test, expect, vi, beforeEach } from "vitest";
import { GraphQLError } from "graphql";

const { mockPrisma } = vi.hoisted(() => ({
  mockPrisma: {
    user: { findUnique: vi.fn() },
    organization: { findUnique: vi.fn() },
  },
}));

vi.mock("../db/prisma", () => ({ prisma: mockPrisma }));

import { assertAuthenticated, assertOrgMember, loadRequestContext } from "../graphql/context";
import type { GraphQLContext } from "../graphql/context";

const emptyCtx: GraphQLContext = {
  userId: null,
  currentOrgId: null,
  isMember: false,
  roles: [],
  olpEnabled: false,
};

const authedCtx: GraphQLContext = {
  userId: "u1",
  currentOrgId: "o1",
  isMember: true,
  roles: ["org_admin"],
  olpEnabled: false,
};

describe("assertAuthenticated", () => {
  test("throws UNAUTHENTICATED when userId is null", () => {
    expect(() => assertAuthenticated(emptyCtx)).toThrow(
      expect.objectContaining({ extensions: { code: "UNAUTHENTICATED" } }),
    );
    expect(() => assertAuthenticated(emptyCtx)).toThrow(GraphQLError);
  });

  test("does not throw when userId is set", () => {
    expect(() => assertAuthenticated(authedCtx)).not.toThrow();
  });
});

describe("assertOrgMember", () => {
  test("throws UNAUTHENTICATED when not authenticated", () => {
    expect(() => assertOrgMember(emptyCtx)).toThrow(
      expect.objectContaining({ extensions: { code: "UNAUTHENTICATED" } }),
    );
  });

  test("throws FORBIDDEN when authenticated but not a member", () => {
    const ctx: GraphQLContext = { ...authedCtx, isMember: false };
    expect(() => assertOrgMember(ctx)).toThrow(
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
    );
  });

  test("does not throw when authenticated and a member", () => {
    expect(() => assertOrgMember(authedCtx)).not.toThrow();
  });
});

describe("loadRequestContext", () => {
  beforeEach(() => {
    vi.resetAllMocks();
  });

  test("returns empty context when userId is null", async () => {
    const ctx = await loadRequestContext(null, "o1");
    expect(ctx.userId).toBeNull();
    expect(mockPrisma.user.findUnique).not.toHaveBeenCalled();
  });

  test("returns partial context with userId when orgId is null and user is active", async () => {
    mockPrisma.user.findUnique.mockResolvedValue({ id: "u1", isActive: true });
    const ctx = await loadRequestContext("u1", null);
    expect(ctx.userId).toBe("u1");
    expect(ctx.currentOrgId).toBeNull();
    expect(ctx.isMember).toBe(false);
    expect(mockPrisma.user.findUnique).toHaveBeenCalledWith({ where: { id: "u1" } });
    expect(mockPrisma.organization.findUnique).not.toHaveBeenCalled();
  });

  test("returns empty context when orgId is null and user is inactive", async () => {
    mockPrisma.user.findUnique.mockResolvedValue({ id: "u1", isActive: false });
    const ctx = await loadRequestContext("u1", null);
    expect(ctx.userId).toBeNull();
  });

  test("returns empty context when user not found", async () => {
    mockPrisma.user.findUnique.mockResolvedValue(null);
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: false });
    const ctx = await loadRequestContext("u1", "o1");
    expect(ctx.userId).toBeNull();
  });

  test("returns empty context when user is inactive", async () => {
    mockPrisma.user.findUnique.mockResolvedValue({ id: "u1", isActive: false, orgMembers: [], userRoles: [] });
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: false });
    const ctx = await loadRequestContext("u1", "o1");
    expect(ctx.userId).toBeNull();
  });

  test("returns context with isMember false when user has no org membership", async () => {
    mockPrisma.user.findUnique.mockResolvedValue({ id: "u1", isActive: true, orgMembers: [], userRoles: [] });
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: false });
    const ctx = await loadRequestContext("u1", "o1");
    expect(ctx.userId).toBe("u1");
    expect(ctx.isMember).toBe(false);
  });

  test("returns full context for active org member", async () => {
    mockPrisma.user.findUnique.mockResolvedValue({
      id: "u1", isActive: true,
      orgMembers: [{ id: "mem-1" }],
      userRoles: [{ role: { code: "org_admin" } }, { role: { code: "viewer" } }],
    });
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
    const ctx = await loadRequestContext("u1", "o1");
    expect(ctx.userId).toBe("u1");
    expect(ctx.currentOrgId).toBe("o1");
    expect(ctx.isMember).toBe(true);
    expect(ctx.roles).toEqual(["org_admin", "viewer"]);
    expect(ctx.olpEnabled).toBe(true);
  });

  test("olpEnabled defaults to false when org not found", async () => {
    mockPrisma.user.findUnique.mockResolvedValue({ id: "u1", isActive: true, orgMembers: [{ id: "mem-1" }], userRoles: [] });
    mockPrisma.organization.findUnique.mockResolvedValue(null);
    const ctx = await loadRequestContext("u1", "o1");
    expect(ctx.olpEnabled).toBe(false);
  });

  test("fetches user and org in parallel", async () => {
    let userResolved = false;
    let orgResolved = false;
    mockPrisma.user.findUnique.mockImplementation(
      () => new Promise((res) => setTimeout(() => { userResolved = true; res({ id: "u1", isActive: true, orgMembers: [], userRoles: [] }); }, 10)),
    );
    mockPrisma.organization.findUnique.mockImplementation(
      () => new Promise((res) => setTimeout(() => { orgResolved = true; res({ olpEnabled: false }); }, 10)),
    );
    await loadRequestContext("u1", "o1");
    expect(userResolved).toBe(true);
    expect(orgResolved).toBe(true);
  });

  test("empty contexts are independent objects", async () => {
    const ctx1 = await loadRequestContext(null, null);
    const ctx2 = await loadRequestContext(null, null);
    ctx1.roles.push("hacked");
    expect(ctx2.roles).toHaveLength(0);
  });
});

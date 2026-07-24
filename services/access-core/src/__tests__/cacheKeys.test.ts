import { describe, expect, test, vi } from "vitest";
import { jitteredTtlMs } from "../cache/config";
import { decisionKey, hashRoleEpochs } from "../cache/keys";

describe("authorization decision cache keys", () => {
  test("separates users and distinct role sets at the initial epoch", () => {
    const roleHashA = hashRoleEpochs(["role-a"], [0]);
    const roleHashB = hashRoleEpochs(["role-b"], [0]);

    expect(roleHashA).not.toBe(roleHashB);
    expect(
      decisionKey({
        orgId: "org-1",
        userId: "user-a",
        assetEpoch: 0,
        userEpoch: 0,
        roleEpochsHash: roleHashA,
        action: "read",
        resourceType: "folder",
        resourceId: "folder-1",
      }),
    ).not.toBe(
      decisionKey({
        orgId: "org-1",
        userId: "user-b",
        assetEpoch: 0,
        userEpoch: 0,
        roleEpochsHash: roleHashB,
        action: "read",
        resourceType: "folder",
        resourceId: "folder-1",
      }),
    );
  });

  test("never returns a non-positive TTL when configured jitter exceeds TTL", () => {
    vi.spyOn(Math, "random").mockReturnValue(0.999999);

    expect(jitteredTtlMs(1, 4000)).toBe(1);

    vi.restoreAllMocks();
  });
});

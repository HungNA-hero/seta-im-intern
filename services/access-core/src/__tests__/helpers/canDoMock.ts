import type { Mock } from "vitest";

export function createCanDoMock(
  mockCanDo: Mock,
  mockFilterAllowedResourceIds: Mock,
) {
  return {
    canDo: mockCanDo,
    filterAllowedResourceIds: mockFilterAllowedResourceIds,
    filterVisible: async (
      userId: string,
      orgId: string,
      action: string,
      resourceType: string,
      items: { id: string }[],
      _getHierarchy?: (item: { id: string }) => unknown,
    ) => {
      const allowed = await mockFilterAllowedResourceIds(
        userId,
        orgId,
        action,
        resourceType,
        items.map((i) => i.id),
      );
      return items.filter((i) => allowed.has(i.id));
    },
  };
}

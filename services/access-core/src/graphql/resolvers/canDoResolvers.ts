import { PermissionActionCode, ResourceType } from "@prisma/client";
import { canDo } from "../../db/queries/canDo";
import { GraphQLContext } from "../context";

export const canDoResolvers = {
  Query: {
    canDo: async (
      _: unknown,
      { userId, action, resourceType, resourceId }: {
        userId: string; action: PermissionActionCode;
        resourceType: ResourceType; resourceId: string;
      },
      ctx: GraphQLContext,
    ) => {
      const result = await canDo(userId, action, resourceType, resourceId, ctx.currentOrgId);
      console.log(JSON.stringify({
        event: "canDo", userId, action, resourceType, resourceId,
        orgId: ctx.currentOrgId, allowed: result.allowed, reason: result.reason,
        ts: new Date().toISOString(),
      }));
      return result;
    },
  },
};

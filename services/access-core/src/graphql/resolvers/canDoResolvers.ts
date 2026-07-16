import { PermissionActionCode, ResourceType } from "@prisma/client";
import { canDo } from "../../db/queries/canDo";
import { assertOrgMember, GraphQLContext } from "../context";

export const canDoResolvers = {
  Query: {
    canDo: async (
      _: unknown,
      { action, resourceType, resourceId }: {
        action: PermissionActionCode;
        resourceType: ResourceType; resourceId: string;
      },
      ctx: GraphQLContext,
    ) => {
      assertOrgMember(ctx);
      return canDo(ctx.userId, action, resourceType, resourceId, ctx.currentOrgId);
    },
  },
};

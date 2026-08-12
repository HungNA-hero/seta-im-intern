import { PermissionActionCode, ResourceType } from "@prisma/client";
import { canDo } from "../../authz/decision";
import { assertOrgMember, GraphQLContext } from "../context";

export const canDoResolvers = {
  Query: {
    canDo: async (
      _: unknown,
      {
        action,
        resourceType,
        resourceId,
      }: {
        action: PermissionActionCode;
        resourceType: ResourceType;
        resourceId: string;
      },
      ctx: GraphQLContext,
    ) => {
      assertOrgMember(ctx);
      return canDo({
        userId: ctx.userId,
        action,
        resourceType,
        resourceId,
        orgId: ctx.currentOrgId,
      });
    },
  },
};

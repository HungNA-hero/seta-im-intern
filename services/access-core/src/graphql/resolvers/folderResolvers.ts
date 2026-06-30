import { GraphQLError } from "graphql";
import { canDo } from "../../db/queries/canDo";
import { GraphQLContext } from "../context";
import { config } from "../../config";

export const folderResolvers = {
  Query: {
    folder: async (
      _: unknown,
      { id }: { id: string },
      ctx: GraphQLContext,
    ) => {
      const { allowed, reason } = await canDo(
        ctx.userId!,
        "read",
        "folder",
        id,
        ctx.currentOrgId,
      );

      if (!allowed) {
        throw new GraphQLError(reason ?? "Forbidden", {
          extensions: { code: "FORBIDDEN" },
        });
      }

      const resp = await fetch(
        `${config.goAssetUrl}/internal/api/v1/folders?id=${id}`,
        {
          headers: {
            "X-User-Id": ctx.userId!,
            "X-Org-Id": ctx.currentOrgId ?? "",
          },
        },
      );

      if (resp.status === 404) {
        throw new GraphQLError("Folder not found", {
          extensions: { code: "NOT_FOUND" },
        });
      }
      if (!resp.ok) {
        throw new GraphQLError("Failed to fetch folder from asset service", {
          extensions: { code: "INTERNAL_SERVER_ERROR" },
        });
      }

      const { folder: f } = await resp.json();
      return {
        id: f.id,
        orgId: f.org_id,
        path: f.path,
        name: f.name,
        description: f.description,
        createdBy: f.created_by,
        updatedBy: f.updated_by ?? null,
        createdAt: f.created_at,
        updatedAt: f.updated_at,
      };
    },
  },
};

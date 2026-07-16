import { GraphQLError } from "graphql";
import { ResourceType } from "@prisma/client";
import {
  assetFetch,
  assetPath,
  throwGoError,
  FOLDERS_PATH,
  METADATA_PATH,
} from "./assetClient";

const RESOURCE_PATHS: Record<ResourceType, string> = {
  folder: FOLDERS_PATH,
  metadata_item: METADATA_PATH,
};

/**
 * Verifies through Asset Core that a logical permission target exists in the
 * requested organization. A real 404 retains the target's resource-specific
 * code; dependency failures are propagated so grant mutations remain fail-closed.
 */
export async function assertResourceInOrg(
  resourceType: ResourceType,
  resourceId: string,
  orgId: string,
  userId: string,
): Promise<void> {
  const resp = await assetFetch(
    assetPath(RESOURCE_PATHS[resourceType], { orgId, id: resourceId }),
    { userId, orgId },
  );

  if (resp.ok) return;
  if (resp.status === 404) {
    throw new GraphQLError("Resource not found in this organization", {
      extensions: {
        code: resourceType === "metadata_item" ? "METADATA_NOT_FOUND" : "FOLDER_NOT_FOUND",
      },
    });
  }
  await throwGoError(resp);
}

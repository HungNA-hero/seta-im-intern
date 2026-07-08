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
 * requested organization. Only a real 404 is exposed as NOT_FOUND; dependency
 * failures are propagated so grant mutations remain fail-closed.
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
      extensions: { code: "NOT_FOUND" },
    });
  }
  throwGoError(resp, "Failed to verify resource organization");
}

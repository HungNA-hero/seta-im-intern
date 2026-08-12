import { RecycleBinConnectionInput, normalizeRecycleBinConnectionInput } from "../domain/recycleBin";
import { filterVisible } from "../authz/decision";
import { decodeRecycleBinCursor } from "../domain/recycleBinCursor";
import { assertAuthenticated, GraphQLContext } from "../graphql/context";
import { fetchRecycleBinCandidatePage } from "./recycleBinCandidatePage";
import { createVisibleRecycleBinPageReader } from "./visibleRecycleBinPage";

const visibleRecycleBinPageReader = createVisibleRecycleBinPageReader({
  fetchCandidatePage: fetchRecycleBinCandidatePage,
  filterVisible,
});

export async function listRecycleBin(ctx: GraphQLContext, orgId: string, input: RecycleBinConnectionInput) {
  assertAuthenticated(ctx);
  const normalized = normalizeRecycleBinConnectionInput(input, decodeRecycleBinCursor);
  return visibleRecycleBinPageReader.readVisibleRecycleBinPage({ ctx, orgId, input: normalized });
}

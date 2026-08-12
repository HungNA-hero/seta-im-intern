import { RecycleBinConnectionInput } from "../../domain/recycleBin";
import { GraphQLContext } from "../context";
import { listRecycleBin } from "../../usecase/recycleBinUsecase";

export const recycleBinResolvers = {
  Query: {
    recycleBin: (
      _: unknown,
      { orgId, input }: { orgId: string; input: RecycleBinConnectionInput },
      ctx: GraphQLContext,
    ) => listRecycleBin(ctx, orgId, input),
  },
};

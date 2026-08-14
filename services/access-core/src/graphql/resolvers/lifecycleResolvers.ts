import { getLifecycleJob, restoreLifecycleUnit } from "../../usecase/lifecycleUsecase";
import { GraphQLContext } from "../context";

export const lifecycleResolvers = {
  Query: {
    lifecycleJob: (_: unknown, { orgId, jobId }: { orgId: string; jobId: string }, ctx: GraphQLContext) =>
      getLifecycleJob(ctx, orgId, jobId),
  },
  Mutation: {
    restoreLifecycleUnit: (_: unknown, { orgId, unitId }: { orgId: string; unitId: string }, ctx: GraphQLContext) =>
      restoreLifecycleUnit(ctx, orgId, unitId),
  },
};

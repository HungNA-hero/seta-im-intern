import { createSchema } from "graphql-yoga";
import { typeDefs }     from "./typeDefs";
import { resolvers }    from "./resolvers";

export interface RequesterContext {
  userId: string;
  currentOrgId: string;
}

export interface PolicyGuard {
  checkFolderAccess(
    requester: RequesterContext,
    action: string,
    orgId: string,
  ): Promise<boolean>;
}

export type GraphQLContext = {
  requester: string | null;
  currentOrg: string | null;
  policyGuard?: PolicyGuard;
};

export const schema = createSchema<GraphQLContext>({ typeDefs, resolvers });

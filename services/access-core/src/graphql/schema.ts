import { createSchema } from 'graphql-yoga';
import { typeDefs }     from './typeDefs';
import { resolvers }    from './resolvers';

export type GraphQLContext = {
  requester: string | null;
  currentOrg: string | null;
};

export const schema = createSchema<GraphQLContext>({ typeDefs, resolvers });

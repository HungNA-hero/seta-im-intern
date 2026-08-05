import { assetFetch } from "../clients/assetClient";
import { GraphQLError } from "graphql";
import { assertCan, GraphQLContext } from "../graphql/context";
import { AssetAuthorizationPrecondition, createAuthorizedAssetGateway } from "./authorizedAssetGateway";

export type Precondition = AssetAuthorizationPrecondition;

export interface AuthorizedFetchInit {
  method?: "GET" | "POST" | "PATCH" | "DELETE";
  body?: Record<string, unknown>;
  includeOrgAdmin?: boolean;
}

const authorizedAssetGateway = createAuthorizedAssetGateway({
  authorization: { assertAllowed: assertCan },
  transport: { request: assetFetch },
  createError: (code, message) => new GraphQLError(message, { extensions: { code } }),
});

export function assertPreconditions(ctx: GraphQLContext, orgId: string, preconditions: Precondition[]): Promise<void> {
  return authorizedAssetGateway.assertPreconditions({
    context: ctx,
    orgId,
    preconditions,
  });
}

export function authorizedFetch(
  ctx: GraphQLContext,
  orgId: string,
  preconditions: Precondition[],
  path: string,
  init: AuthorizedFetchInit = {},
): Promise<Response> {
  return authorizedAssetGateway.fetch({
    context: ctx,
    orgId,
    preconditions,
    path,
    init,
  });
}

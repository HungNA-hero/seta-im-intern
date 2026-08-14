export const lifecycleTypeDefs = /* GraphQL */ `
  enum LifecycleJobOperation {
    DELETE
    RESTORE
    PURGE
  }

  enum LifecycleJobStatus {
    QUEUED
    RUNNING
    SUCCEEDED
    FAILED
    SUPPRESSED
  }

  type LifecycleJob {
    jobId: ID!
    lifecycleUnitId: ID
    operation: LifecycleJobOperation!
    status: LifecycleJobStatus!
    attempts: Int!
    failureCode: String
    queuedAt: String
    startedAt: String
    completedAt: String
  }
`;

export const lifecycleQueryFields = /* GraphQL */ `
  lifecycleJob(orgId: ID!, jobId: ID!): LifecycleJob! @orgMember @sameOrg
`;

export const lifecycleMutationFields = /* GraphQL */ `
  restoreLifecycleUnit(orgId: ID!, unitId: ID!): LifecycleJob! @orgMember @sameOrg
`;

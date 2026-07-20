import { GraphQLError } from "graphql";

type PrismaErrorMapping = {
  message: string;
  errorCode: string;
};

export function serializeDates<T extends { createdAt: Date; updatedAt: Date }>(obj: T) {
  return { ...obj, createdAt: obj.createdAt.toISOString(), updatedAt: obj.updatedAt.toISOString() };
}

export function serializePermission<T extends { grantedAt: Date }>(p: T) {
  return { ...p, grantedAt: p.grantedAt.toISOString() };
}

export function rethrowPrismaError(
  err: unknown,
  map: Partial<Record<"P2002" | "P2025", PrismaErrorMapping>>,
): never {
  const code = (err as any)?.code as string | undefined;
  if (code === "P2002" && map.P2002)
    throw new GraphQLError(map.P2002.message, { extensions: { code: map.P2002.errorCode } });
  if (code === "P2025" && map.P2025)
    throw new GraphQLError(map.P2025.message, { extensions: { code: map.P2025.errorCode } });
  throw err;
}

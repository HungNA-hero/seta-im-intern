export function serializeDates<T extends { createdAt: Date; updatedAt: Date }>(obj: T) {
  return { ...obj, createdAt: obj.createdAt.toISOString(), updatedAt: obj.updatedAt.toISOString() };
}

import { listUsers, getUserById, User } from '../../db/queries/users';

function toUser(u: User) {
  return {
    id:          u.id,
    email:       u.email,
    displayName: u.displayName,
    isActive:    u.isActive,
    createdAt:   u.createdAt.toISOString(),
    updatedAt:   u.updatedAt.toISOString(),
  };
}

export const userResolvers = {
  Query: {
    users: async () => (await listUsers()).map(toUser),
    user:  async (_: unknown, { id }: { id: string }) => {
      const u = await getUserById(id);
      return u ? toUser(u) : null;
    },
  },
};

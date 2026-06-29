import { listUsers, getUserById, User } from '../../db/queries/users';
import { serializeDates } from './utils';

function toUser(u: User) {
  return serializeDates({
    id: u.id,
    email: u.email,
    displayName: u.displayName,
    isActive: u.isActive,
    createdAt: u.createdAt,
    updatedAt: u.updatedAt,
  });
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

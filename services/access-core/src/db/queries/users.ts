import { prisma } from "../prisma";

export type User = {
  id: string;
  email: string;
  displayName: string;
  isActive: boolean;
  createdAt: Date;
  updatedAt: Date;
};

function toUser(user: {
  id: string;
  email: string;
  displayName: string;
  isActive: boolean;
  createdAt: Date;
  updatedAt: Date;
}): User {
  return {
    id: user.id,
    email: user.email,
    displayName: user.displayName,
    isActive: user.isActive,
    createdAt: user.createdAt,
    updatedAt: user.updatedAt,
  };
}

export async function listUsers(): Promise<User[]> {
  const users = await prisma.user.findMany();
  return users.map(toUser);
}

export async function getUserById(id: string): Promise<User | null> {
  const user = await prisma.user.findUnique({ where: { id } });
  return user ? toUser(user) : null;
}

export async function createUser(email: string, displayName: string): Promise<User> {
  return toUser(await prisma.user.create({ data: { email, displayName } }));
}

export async function updateUser(id: string, displayName: string): Promise<User> {
  return toUser(await prisma.user.update({ where: { id }, data: { displayName } }));
}

export async function deactivateUser(id: string): Promise<User> {
  const deactivatedUser = await prisma.$transaction(async (tx) => {
    await Promise.all([
      tx.userRole.deleteMany({ where: { userId: id } }),
      tx.objectPermission.deleteMany({ where: { granteeUserId: id } }),
    ]);
    return tx.user.update({ where: { id }, data: { isActive: false } });
  });
  return toUser(deactivatedUser);
}

import { prisma } from "../prisma";

export type User = {
  id: string;
  email: string;
  displayName: string;
  isActive: boolean;
  createdAt: Date;
  updatedAt: Date;
};

function toUser(u: {
  id: string;
  email: string;
  displayName: string;
  isActive: boolean;
  createdAt: Date;
  updatedAt: Date;
}): User {
  return {
    id: u.id,
    email: u.email,
    displayName: u.displayName,
    isActive: u.isActive,
    createdAt: u.createdAt,
    updatedAt: u.updatedAt,
  };
}

export async function listUsers(): Promise<User[]> {
  const users = await prisma.user.findMany();
  return users.map(toUser);
}

export async function getUserById(id: string): Promise<User | null> {
  const u = await prisma.user.findUnique({ where: { id } });
  return u ? toUser(u) : null;
}

export async function createUser(email: string, displayName: string): Promise<User> {
  return toUser(await prisma.user.create({ data: { email, displayName } }));
}

export async function updateUser(id: string, displayName: string): Promise<User> {
  return toUser(await prisma.user.update({ where: { id }, data: { displayName } }));
}

export async function deactivateUser(id: string): Promise<User> {
  const u = await prisma.$transaction(async (tx) => {
    await Promise.all([
      tx.userRole.deleteMany({ where: { userId: id } }),
      tx.objectPermission.deleteMany({ where: { granteeUserId: id } }),
    ]);
    return tx.user.update({ where: { id }, data: { isActive: false } });
  });
  return toUser(u);
}

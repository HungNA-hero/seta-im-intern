import { prisma } from '../prisma';

export type User = {
  id: string;
  email: string;
  displayName: string;
  isActive: boolean;
  createdAt: Date;
  updatedAt: Date;
};

export async function listUsers(): Promise<User[]> {
  const users = await prisma.users.findMany();
  return users.map(u => ({
    id: u.id,
    email: u.email,
    displayName: u.display_name,
    isActive: u.is_active,
    createdAt: u.created_at,
    updatedAt: u.updated_at,
  }));
}

export async function getUserById(id: string): Promise<User | null> {
  const u = await prisma.users.findUnique({ where: { id } });
  if (!u) return null;
  return {
    id: u.id,
    email: u.email,
    displayName: u.display_name,
    isActive: u.is_active,
    createdAt: u.created_at,
    updatedAt: u.updated_at,
  };
}

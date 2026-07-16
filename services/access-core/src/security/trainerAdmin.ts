import { prisma } from "../db/prisma";

export type TrainerAdminGateState =
  | { enabled: true; reason: "enabled" }
  | { enabled: false; reason: "disabled" | "missing_expiry" | "invalid_expiry" | "expired" };

type TrainerAdminDecisionReason = TrainerAdminGateState["reason"] | "production";

/**
 * Returns the runtime state of the temporary trainer-admin bypass.
 * The bypass is deliberately default-off and requires a future ISO-8601 expiry.
 */
export function getTrainerAdminGateState(
  now: Date = new Date(),
): TrainerAdminGateState {
  if (process.env.TRAINER_ADMIN_ENABLED !== "true") {
    return { enabled: false, reason: "disabled" };
  }

  const rawExpiry = process.env.TRAINER_ADMIN_EXPIRES_AT;
  if (!rawExpiry) return { enabled: false, reason: "missing_expiry" };

  const expiry = new Date(rawExpiry);
  if (Number.isNaN(expiry.getTime())) {
    return { enabled: false, reason: "invalid_expiry" };
  }
  if (expiry <= now) return { enabled: false, reason: "expired" };

  return { enabled: true, reason: "enabled" };
}

export function auditTrainerAdminDecision(
  userId: string,
  allowed: boolean,
  reason: TrainerAdminDecisionReason,
): void {
  console.info(
    JSON.stringify({
      event: "trainer_admin_bypass_evaluated",
      userId,
      allowed,
      reason,
      timestamp: new Date().toISOString(),
    }),
  );
}

export async function assertTemporaryTrainerAdmin(userId: string): Promise<void> {
  const user = await prisma.user.findFirst({
    where: {
      id: userId,
      isActive: true,
      userRoles: { some: { role: { code: "trainer_admin" } } },
    },
    select: { id: true },
  });

  const state = getTrainerAdminGateState();
  const production = process.env.NODE_ENV === "production";
  const allowed = user !== null && !production && state.enabled;
  auditTrainerAdminDecision(userId, allowed, production ? "production" : state.reason);
  if (!allowed) {
    throw new Error("Trainer administrator access is not enabled");
  }
}

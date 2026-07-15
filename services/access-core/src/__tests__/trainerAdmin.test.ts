import { afterEach, describe, expect, test } from "vitest";
import { getTrainerAdminGateState } from "../security/trainerAdmin";

const originalEnabled = process.env.TRAINER_ADMIN_ENABLED;
const originalExpiry = process.env.TRAINER_ADMIN_EXPIRES_AT;

afterEach(() => {
  if (originalEnabled === undefined) delete process.env.TRAINER_ADMIN_ENABLED;
  else process.env.TRAINER_ADMIN_ENABLED = originalEnabled;
  if (originalExpiry === undefined) delete process.env.TRAINER_ADMIN_EXPIRES_AT;
  else process.env.TRAINER_ADMIN_EXPIRES_AT = originalExpiry;
});

describe("trainer admin gate", () => {
  const now = new Date("2026-07-15T00:00:00.000Z");

  test("is default-off when the enable flag is absent", () => {
    delete process.env.TRAINER_ADMIN_ENABLED;
    delete process.env.TRAINER_ADMIN_EXPIRES_AT;
    expect(getTrainerAdminGateState(now)).toEqual({ enabled: false, reason: "disabled" });
  });

  test("fails closed when expiry is missing, malformed, or expired", () => {
    process.env.TRAINER_ADMIN_ENABLED = "true";

    delete process.env.TRAINER_ADMIN_EXPIRES_AT;
    expect(getTrainerAdminGateState(now)).toEqual({ enabled: false, reason: "missing_expiry" });

    process.env.TRAINER_ADMIN_EXPIRES_AT = "not-a-date";
    expect(getTrainerAdminGateState(now)).toEqual({ enabled: false, reason: "invalid_expiry" });

    process.env.TRAINER_ADMIN_EXPIRES_AT = "2026-07-14T23:59:59.000Z";
    expect(getTrainerAdminGateState(now)).toEqual({ enabled: false, reason: "expired" });
  });

  test("allows the bypass only with a future expiry", () => {
    process.env.TRAINER_ADMIN_ENABLED = "true";
    process.env.TRAINER_ADMIN_EXPIRES_AT = "2026-07-16T00:00:00.000Z";
    expect(getTrainerAdminGateState(now)).toEqual({ enabled: true, reason: "enabled" });
  });
});

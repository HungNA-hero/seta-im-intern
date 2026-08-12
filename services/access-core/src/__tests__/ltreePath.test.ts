import { describe, expect, test } from "vitest";
import { ancestorIdsFromPath } from "../domain/ltreePath";

const ROOT_ID = "550e8400-e29b-41d4-a716-446655440000";
const PARENT_ID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8";
const SELF_ID = "6ba7b811-9dad-11d1-80b4-00c04fd430c8";

function segment(id: string): string {
  return id.replaceAll("-", "");
}

function path(...ids: string[]): string {
  return ids.map(segment).join(".");
}

describe("ancestorIdsFromPath", () => {
  test("returns ancestors root-first and excludes the folder itself", () => {
    expect(ancestorIdsFromPath(path(ROOT_ID, PARENT_ID, SELF_ID))).toEqual([ROOT_ID, PARENT_ID]);
  });

  test("restores canonical UUID dashes from the dash-stripped ltree label", () => {
    const [ancestor] = ancestorIdsFromPath(path(ROOT_ID, SELF_ID));

    expect(ancestor).toBe(ROOT_ID);
    expect(ancestor).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  });

  test("returns no ancestors for a root folder, whose path is its own label", () => {
    expect(ancestorIdsFromPath(path(ROOT_ID))).toEqual([]);
  });

  test("returns no ancestors for an empty path", () => {
    expect(ancestorIdsFromPath("")).toEqual([]);
  });

  test("drops a malformed segment rather than emitting a malformed id", () => {
    const malformed = `notauuid.${segment(PARENT_ID)}.${segment(SELF_ID)}`;

    expect(ancestorIdsFromPath(malformed)).toEqual([PARENT_ID]);
  });

  test("dropping a malformed segment loses that ancestor's inherited grants, denying rather than granting", () => {
    const intact = ancestorIdsFromPath(path(ROOT_ID, PARENT_ID, SELF_ID));
    const corrupted = ancestorIdsFromPath(`${segment(ROOT_ID).slice(0, 31)}.${segment(PARENT_ID)}.${segment(SELF_ID)}`);

    expect(intact).toContain(ROOT_ID);
    expect(corrupted).not.toContain(ROOT_ID);
    expect(corrupted.length).toBeLessThan(intact.length);
  });
});

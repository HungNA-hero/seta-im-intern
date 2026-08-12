import { describe, expect, test } from "vitest";
import { decodeRecycleBinCursor, encodeRecycleBinCursor } from "../domain/recycleBinCursor";

const position = {
  deletedAt: "2026-08-12T10:11:12.123456789Z",
  lifecycleUnitId: "00000000-0000-4000-8000-000000000001",
};

describe("recycle bin cursor codec", () => {
  test("round-trips the versioned deleted-at/lifecycle-unit tuple", () => {
    const cursor = encodeRecycleBinCursor(position);

    expect(decodeRecycleBinCursor(cursor)).toEqual(position);
    expect(JSON.parse(Buffer.from(cursor, "base64url").toString("utf8"))).toEqual({ v: 1, ...position });
  });

  test.each([
    "",
    "not-base64-json",
    Buffer.from(JSON.stringify({ v: 2, ...position })).toString("base64url"),
    Buffer.from(JSON.stringify({ v: 1, deletedAt: "not-a-time", lifecycleUnitId: position.lifecycleUnitId })).toString(
      "base64url",
    ),
    Buffer.from(JSON.stringify({ v: 1, deletedAt: position.deletedAt, lifecycleUnitId: "not-a-uuid" })).toString(
      "base64url",
    ),
  ])("returns CURSOR_INVALID for %s", (cursor) => {
    expect(() => decodeRecycleBinCursor(cursor)).toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "CURSOR_INVALID", number: 1003 }) }),
    );
  });

  test("rejects a non-canonical base64url or payload ordering", () => {
    expect(() => decodeRecycleBinCursor(`${encodeRecycleBinCursor(position)}=`)).toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "CURSOR_INVALID" }) }),
    );
    const reordered = Buffer.from(
      JSON.stringify({ lifecycleUnitId: position.lifecycleUnitId, deletedAt: position.deletedAt, v: 1 }),
    ).toString("base64url");
    expect(() => decodeRecycleBinCursor(reordered)).toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "CURSOR_INVALID" }) }),
    );
  });
});

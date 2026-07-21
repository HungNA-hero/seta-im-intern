import { describe, expect, test } from "vitest";
import {
  decodeMetadataCursor,
  encodeMetadataCursor,
} from "../domain/metadataCursor";

const position = {
  updatedAt: "2026-07-17T10:11:12.123456789Z",
  id: "00000000-0000-4000-8000-000000000001",
};

describe("metadata cursor codec", () => {
  test("round-trips the versioned ordering tuple without additional state", () => {
    const cursor = encodeMetadataCursor(position);

    expect(decodeMetadataCursor(cursor)).toEqual(position);
    expect(JSON.parse(Buffer.from(cursor, "base64url").toString("utf8"))).toEqual({
      v: 1,
      ...position,
    });
  });

  test("accepts a canonical deterministic PostgreSQL UUID", () => {
    const deterministicPosition = {
      updatedAt: "2026-07-17T10:00:00.000Z",
      id: "20000000-0000-0000-0000-000000000108",
    };

    expect(decodeMetadataCursor(encodeMetadataCursor(deterministicPosition))).toEqual(
      deterministicPosition,
    );
  });

  test.each([
    "",
    "not-base64-json",
    Buffer.from(JSON.stringify({ v: 2, ...position })).toString("base64url"),
    Buffer.from(JSON.stringify({ v: 1, updatedAt: "not-a-time", id: position.id })).toString("base64url"),
    Buffer.from(JSON.stringify({ v: 1, updatedAt: "2026-02-31T00:00:00Z", id: position.id })).toString("base64url"),
    Buffer.from(JSON.stringify({ v: 1, updatedAt: position.updatedAt, id: "not-a-uuid" })).toString("base64url"),
  ])("returns the stable CURSOR_INVALID contract for %s", (cursor) => {
    expect(() => decodeMetadataCursor(cursor)).toThrow(
      expect.objectContaining({
        extensions: expect.objectContaining({ code: "CURSOR_INVALID", number: 1003 }),
      }),
    );
  });

  test.each([
    `${encodeMetadataCursor(position)}!`,
    `${encodeMetadataCursor(position)}=`,
    Buffer.from(JSON.stringify({ id: position.id, updatedAt: position.updatedAt, v: 1 })).toString("base64url"),
  ])("rejects non-canonical Base64URL input before it can become a valid cursor", (cursor) => {
    expect(() => decodeMetadataCursor(cursor)).toThrow(
      expect.objectContaining({
        extensions: expect.objectContaining({ code: "CURSOR_INVALID", number: 1003 }),
      }),
    );
  });
});

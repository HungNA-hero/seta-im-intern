import { GraphQLError } from "graphql";
import { cursorInvalid } from "../errors/factories";

const CURSOR_VERSION = 1;
const MAX_CURSOR_LENGTH = 1024;
const base64UrlPattern = /^[A-Za-z0-9_-]+$/;
const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const rfc3339Pattern =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

export interface MetadataCursorPosition {
  updatedAt: string;
  id: string;
}

interface EncodedMetadataCursor extends MetadataCursorPosition {
  v: number;
}

function isLeapYear(year: number): boolean {
  return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
}

function isCanonicalRfc3339(value: string): boolean {
  const match = rfc3339Pattern.exec(value);
  if (!match) return false;

  const [
    ,
    yearText,
    monthText,
    dayText,
    hourText,
    minuteText,
    secondText,
    timezone,
  ] = match;
  const year = Number(yearText);
  const month = Number(monthText);
  const day = Number(dayText);
  const hour = Number(hourText);
  const minute = Number(minuteText);
  const second = Number(secondText);
  const timezoneHour = timezone === "Z" ? 0 : Number(timezone.slice(1, 3));
  const timezoneMinute = timezone === "Z" ? 0 : Number(timezone.slice(4, 6));
  const daysInMonth = [
    31,
    isLeapYear(year) ? 29 : 28,
    31,
    30,
    31,
    30,
    31,
    31,
    30,
    31,
    30,
    31,
  ];

  return (
    month >= 1 &&
    month <= 12 &&
    day >= 1 &&
    day <= daysInMonth[month - 1] &&
    hour <= 23 &&
    minute <= 59 &&
    second <= 59 &&
    timezoneHour <= 23 &&
    timezoneMinute <= 59
  );
}

export function isValidMetadataCursorPosition(
  value: unknown,
): value is MetadataCursorPosition {
  if (!value || typeof value !== "object") return false;
  const position = value as Partial<MetadataCursorPosition>;
  return (
    typeof position.updatedAt === "string" &&
    typeof position.id === "string" &&
    isCanonicalRfc3339(position.updatedAt) &&
    uuidPattern.test(position.id)
  );
}

export function encodeMetadataCursor(position: MetadataCursorPosition): string {
  const payload: EncodedMetadataCursor = {
    v: CURSOR_VERSION,
    updatedAt: position.updatedAt,
    id: position.id,
  };
  return Buffer.from(JSON.stringify(payload), "utf8").toString("base64url");
}

export function decodeMetadataCursor(cursor: string): MetadataCursorPosition {
  try {
    if (
      !cursor ||
      cursor.length > MAX_CURSOR_LENGTH ||
      cursor.length % 4 === 1 ||
      !base64UrlPattern.test(cursor)
    ) {
      throw cursorInvalid();
    }
    const decoded = Buffer.from(cursor, "base64url").toString("utf8");
    const payload = JSON.parse(decoded) as Partial<EncodedMetadataCursor>;
    if (
      payload.v !== CURSOR_VERSION ||
      !isValidMetadataCursorPosition(payload) ||
      encodeMetadataCursor(payload) !== cursor
    ) {
      throw cursorInvalid();
    }
    return { updatedAt: payload.updatedAt, id: payload.id };
  } catch (error) {
    if (error instanceof GraphQLError) throw error;
    throw cursorInvalid();
  }
}

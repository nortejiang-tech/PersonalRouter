/**
 * Deterministic, dependency-free UI formatting helpers for the PersonalRouter console.
 *
 * Conventions shared by every helper in this file:
 *   - `null` / `undefined` / `NaN` / non-finite input is "unknown" and renders as
 *     the em dash placeholder, never as a zero.
 *   - Negative numbers are also invalid for these metrics (counts, latency, cost)
 *     and render as the placeholder.
 *   - Zero is a real, meaningful value and is always formatted normally.
 *
 * Node 24 native TypeScript (type stripping only): no enums, no parameter
 * properties, no third-party libraries.
 */

const UNKNOWN = "\u2014"; // —

/** True only for a non-negative, finite number. */
function isUsable(value: number | null | undefined): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0;
}

/**
 * Thousands-separated count with at most two decimals, en-US grouping.
 * Examples: 0 -> "0", 1234 -> "1,234", 1234.5 -> "1,234.5", 1234.567 -> "1,234.57"
 */
export function formatNumber(value?: number | null): string {
  if (!isUsable(value)) return UNKNOWN;
  // Collapse -0 to 0 so the sign never leaks into the UI.
  const normalized = value === 0 ? 0 : value;
  return normalized.toLocaleString("en-US", {
    minimumFractionDigits: 0,
    maximumFractionDigits: 2,
  });
}

/**
 * Latency formatter.
 *   < 1000 ms  -> rounded integer milliseconds, e.g. 123 -> "123 ms"
 *   >= 1000 ms -> seconds with 2 decimals, e.g. 1250 -> "1.25 s"
 */
export function formatMilliseconds(value?: number | null): string {
  if (!isUsable(value)) return UNKNOWN;
  if (value < 1000) {
    return `${Math.round(value)} ms`;
  }
  return `${(value / 1000).toFixed(2)} s`;
}

/**
 * Cost formatter: dollar sign with exactly 4 decimals.
 * Examples: 0 -> "$0.0000", 0.00005 -> "$0.0001", 12.34 -> "$12.3400"
 */
export function formatUSD(value?: number | null): string {
  if (!isUsable(value)) return UNKNOWN;
  return `$${value.toFixed(4)}`;
}

/**
 * Strict ISO-8601 date-time gate. Only a full `YYYY-MM-DDTHH:mm:ss` (optionally
 * with fractional seconds) carrying an explicit zone - `Z` or `+/-HH:mm` - is
 * accepted. Date-only strings, zone-less strings and anything looser ("0", "2026",
 * "Jan 5 2026", ...) are rejected so `new Date()` can never fall back to the
 * machine's local timezone or to lenient legacy parsing.
 */
const ISO_DATE_TIME_WITH_ZONE =
  /^\d{4}-\d{2}-\d{2}[Tt ]\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:[Zz]|[+-]\d{2}:\d{2})$/;

const SHANGHAI_FORMATTER = new Intl.DateTimeFormat("en-CA", {
  timeZone: "Asia/Shanghai",
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

/**
 * Fixed Asia/Shanghai wall-clock rendering: "YYYY-MM-DD HH:mm:ss".
 * Machine timezone must never influence the result: only ISO date-times that carry
 * an explicit zone (`Z` or `+/-HH:mm`) are accepted. Zone-less strings, date-only
 * strings, non-ISO strings such as "0", invalid instants, empty and unknown input
 * all render as the placeholder.
 */
export function formatTimestamp(value?: string | null): string {
  if (typeof value !== "string") return UNKNOWN;
  const trimmed = value.trim();
  if (trimmed === "") return UNKNOWN;

  // Reject before parsing: without this guard `new Date` would interpret
  // zone-less input in the machine's local timezone.
  if (!ISO_DATE_TIME_WITH_ZONE.test(trimmed)) return UNKNOWN;

  const date = new Date(trimmed);
  if (Number.isNaN(date.getTime())) return UNKNOWN;

  // en-CA yields "YYYY-MM-DD, HH:MM:SS"; strip the comma and any "AM/PM".
  const parts = SHANGHAI_FORMATTER.formatToParts(date);
  const pick = (type: Intl.DateTimeFormatPartTypes): string => {
    const found = parts.find((part) => part.type === type);
    return found ? found.value : "";
  };

  const year = pick("year");
  const month = pick("month");
  const day = pick("day");
  let hour = pick("hour");
  const minute = pick("minute");
  const second = pick("second");

  if (!year || !month || !day || hour === "" || minute === "" || second === "") {
    return UNKNOWN;
  }

  // Some ICU builds report hour "24" at midnight in h23/h24 hybrids.
  if (hour === "24") hour = "00";

  return `${year}-${month}-${day} ${hour}:${minute}:${second}`;
}

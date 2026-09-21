import test from "node:test";
import assert from "node:assert/strict";

import {
  formatNumber,
  formatMilliseconds,
  formatUSD,
  formatTimestamp,
} from "./format.ts";

const DASH = "\u2014";

// ---------------------------------------------------------------- formatNumber

test("formatNumber: small and thousands-separated values", () => {
  assert.equal(formatNumber(7), "7");
  assert.equal(formatNumber(999), "999");
  assert.equal(formatNumber(1000), "1,000");
  assert.equal(formatNumber(1234567), "1,234,567");
});

test("formatNumber: zero is a real value, not unknown", () => {
  assert.equal(formatNumber(0), "0");
  assert.equal(formatNumber(-0), "0");
});

test("formatNumber: at most two decimals with rounding", () => {
  assert.equal(formatNumber(1234.5), "1,234.5");
  assert.equal(formatNumber(1234.56), "1,234.56");
  assert.equal(formatNumber(1234.567), "1,234.57");
  assert.equal(formatNumber(0.004), "0");
});

test("formatNumber: null and undefined are unknown", () => {
  assert.equal(formatNumber(null), DASH);
  assert.equal(formatNumber(undefined), DASH);
  assert.equal(formatNumber(), DASH);
});

test("formatNumber: negative, NaN and Infinity are unknown", () => {
  assert.equal(formatNumber(-1), DASH);
  assert.equal(formatNumber(-0.5), DASH);
  assert.equal(formatNumber(Number.NaN), DASH);
  assert.equal(formatNumber(Number.POSITIVE_INFINITY), DASH);
  assert.equal(formatNumber(Number.NEGATIVE_INFINITY), DASH);
});

// -------------------------------------------------------- formatMilliseconds

test("formatMilliseconds: sub-second values round to integer ms", () => {
  assert.equal(formatMilliseconds(0), "0 ms");
  assert.equal(formatMilliseconds(123), "123 ms");
  assert.equal(formatMilliseconds(123.4), "123 ms");
  assert.equal(formatMilliseconds(999.5), "1000 ms");
});

test("formatMilliseconds: one second and above use two-decimal seconds", () => {
  assert.equal(formatMilliseconds(1000), "1.00 s");
  assert.equal(formatMilliseconds(1250), "1.25 s");
  assert.equal(formatMilliseconds(65_432), "65.43 s");
});

test("formatMilliseconds: unknown, negative, NaN and Infinity", () => {
  assert.equal(formatMilliseconds(null), DASH);
  assert.equal(formatMilliseconds(undefined), DASH);
  assert.equal(formatMilliseconds(-1), DASH);
  assert.equal(formatMilliseconds(-0.001), DASH);
  assert.equal(formatMilliseconds(Number.NaN), DASH);
  assert.equal(formatMilliseconds(Number.POSITIVE_INFINITY), DASH);
  assert.equal(formatMilliseconds(Number.NEGATIVE_INFINITY), DASH);
});

// ------------------------------------------------------------------ formatUSD

test("formatUSD: always four decimals", () => {
  assert.equal(formatUSD(0), "$0.0000");
  assert.equal(formatUSD(1), "$1.0000");
  assert.equal(formatUSD(12.34), "$12.3400");
  assert.equal(formatUSD(0.0001), "$0.0001");
});

test("formatUSD: rounding to four decimals", () => {
  assert.equal(formatUSD(0.00005), "$0.0001");
  assert.equal(formatUSD(0.00004), "$0.0000");
  assert.equal(formatUSD(1.23456789), "$1.2346");
});

test("formatUSD: unknown, negative, NaN and Infinity", () => {
  assert.equal(formatUSD(null), DASH);
  assert.equal(formatUSD(undefined), DASH);
  assert.equal(formatUSD(-0.01), DASH);
  assert.equal(formatUSD(Number.NaN), DASH);
  assert.equal(formatUSD(Number.POSITIVE_INFINITY), DASH);
  assert.equal(formatUSD(Number.NEGATIVE_INFINITY), DASH);
});

// ----------------------------------------------------------- formatTimestamp

test("formatTimestamp: UTC instant rendered in Asia/Shanghai", () => {
  // 2026-01-01T16:00:00Z is 2026-01-02 00:00:00 in UTC+8.
  assert.equal(formatTimestamp("2026-01-01T16:00:00Z"), "2026-01-02 00:00:00");
});

test("formatTimestamp: cross-day boundary into the next Shanghai day", () => {
  assert.equal(formatTimestamp("2026-03-31T15:59:59Z"), "2026-03-31 23:59:59");
  assert.equal(formatTimestamp("2026-03-31T16:00:00Z"), "2026-04-01 00:00:00");
});

test("formatTimestamp: cross-month and cross-year boundaries", () => {
  assert.equal(formatTimestamp("2026-12-31T15:30:00Z"), "2026-12-31 23:30:00");
  assert.equal(formatTimestamp("2026-12-31T16:00:00Z"), "2027-01-01 00:00:00");
});

test("formatTimestamp: offset-aware and fractional-second inputs", () => {
  assert.equal(formatTimestamp("2026-05-04T09:08:07.500Z"), "2026-05-04 17:08:07");
  assert.equal(formatTimestamp("2026-05-04T01:02:03-07:00"), "2026-05-04 16:02:03");
  assert.equal(formatTimestamp("2026-05-04T00:00:00+08:00"), "2026-05-04 00:00:00");
});

test("formatTimestamp: invalid, empty and unknown input", () => {
  assert.equal(formatTimestamp(null), DASH);
  assert.equal(formatTimestamp(undefined), DASH);
  assert.equal(formatTimestamp(), DASH);
  assert.equal(formatTimestamp(""), DASH);
  assert.equal(formatTimestamp("   "), DASH);
  assert.equal(formatTimestamp("not a date"), DASH);
  assert.equal(formatTimestamp("2026-13-45T99:99:99Z"), DASH);
});

test("formatTimestamp: zone-less date-time is rejected, never machine-local", () => {
  assert.equal(formatTimestamp("2026-01-01T16:00:00"), DASH);
  assert.equal(formatTimestamp("2026-07-15 04:05:06"), DASH);
  assert.equal(formatTimestamp("2026-01-01T16:00"), DASH);
});

test("formatTimestamp: date-only strings are rejected", () => {
  assert.equal(formatTimestamp("2026-01-01"), DASH);
  assert.equal(formatTimestamp("2026-01"), DASH);
  assert.equal(formatTimestamp("2026"), DASH);
});

test("formatTimestamp: loose legacy strings such as \"0\" are rejected", () => {
  assert.equal(formatTimestamp("0"), DASH);
  assert.equal(formatTimestamp("00:00:00"), DASH);
  assert.equal(formatTimestamp("Jan 5 2026"), DASH);
  assert.equal(formatTimestamp("2026-01-01T16:00:00+08"), DASH);
  assert.equal(formatTimestamp("20260101T160000Z"), DASH);
});

test("formatTimestamp: valid Z and explicit offsets still render", () => {
  assert.equal(formatTimestamp("2026-01-01T16:00:00Z"), "2026-01-02 00:00:00");
  assert.equal(formatTimestamp("2026-01-01t16:00:00z"), "2026-01-02 00:00:00");
  assert.equal(formatTimestamp("2026-01-02T00:00:00+08:00"), "2026-01-02 00:00:00");
  assert.equal(formatTimestamp("2026-01-01T09:00:00-07:00"), "2026-01-02 00:00:00");
  assert.equal(formatTimestamp("2026-01-01T16:00:00.000Z"), "2026-01-02 00:00:00");
  assert.equal(formatTimestamp("  2026-01-01T16:00:00Z  "), "2026-01-02 00:00:00");
});

test("formatTimestamp: result never depends on the machine timezone shape", () => {
  const rendered = formatTimestamp("2026-07-15T04:05:06Z");
  assert.match(rendered, /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
  assert.equal(rendered, "2026-07-15 12:05:06");
});

import assert from "node:assert/strict";
import test from "node:test";

import { formatReviewTime } from "./time.ts";

test("formatReviewTime normalizes ISO timestamps to local time without fractions", () => {
  assert.match(formatReviewTime("2026-08-23T19:09:05.78721155+08:00"), /^2026-08-23 \d{2}:09:05$/);
  assert.match(formatReviewTime("2026-08-23T09:06:08Z"), /^2026-08-23 \d{2}:06:08$/);
  assert.equal(formatReviewTime(null), "-");
  assert.equal(formatReviewTime("invalid"), "-");
});

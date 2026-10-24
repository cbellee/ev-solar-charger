import { describe, expect, it } from "vitest";
import { usageRangeWindow } from "./usageRange";

const now = Date.UTC(2026, 9, 24, 12, 0, 0);
const hour = 3_600_000;

describe("usageRangeWindow", () => {
  it.each([
    ["24h", 24],
    ["7d", 24 * 7],
    ["30d", 24 * 30],
  ] as const)("%s requests and pins the axis to the last %i hours", (range, hours) => {
    const w = usageRangeWindow(range, now);
    expect(w.params).toEqual({
      from: new Date(now - hours * hour).toISOString(),
      to: new Date(now).toISOString(),
    });
    expect(w.domain).toEqual([now - hours * hour, now]);
  });

  it("all requests from the epoch and fits the axis to the data", () => {
    const w = usageRangeWindow("all", now);
    expect(w.params).toEqual({
      from: new Date(0).toISOString(),
      to: new Date(now).toISOString(),
    });
    expect(w.domain).toEqual(["dataMin", "dataMax"]);
  });

  it("uses time-of-day ticks only for 24h", () => {
    expect(usageRangeWindow("24h", now).showTime).toBe(true);
    expect(usageRangeWindow("7d", now).showTime).toBe(false);
  });
});

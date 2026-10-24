import { describe, expect, it } from "vitest";
import type { StateSnapshot } from "@/api/types";
import { batteryProgress, gridBadge, statusBanner } from "./mobileStatus";

function snap(overrides: Partial<StateSnapshot> = {}): StateSnapshot {
  return {
    state: "charging",
    mode: "auto",
    testMode: false,
    pvWatts: 5000,
    gridWatts: 0,
    loadWatts: 800,
    surplusWatts: 4200,
    targetAmps: 16,
    actualAmps: 16,
    batteryPct: 64,
    timeToLimitHours: 2.4,
    chargeLimit: 80,
    chargeLimitMin: 50,
    chargeLimitMax: 100,
    carPluggedIn: true,
    carOnline: true,
    chargingState: "Charging",
    consecutiveLow: 0,
    consecutiveSurplus: 0,
    lastUpdate: "2026-10-24T05:00:00Z",
    lastError: "",
    ...overrides,
  };
}

describe("statusBanner", () => {
  it.each([
    [{ state: "charging", actualAmps: 26 }, "Charging · 26 A", "bg-green-600"],
    [{ state: "stopped_low_surplus" }, "Paused · low surplus", "bg-orange-600"],
    [{ state: "monitoring" }, "Monitoring", "bg-yellow-600"],
    [{ state: "wake_pending" }, "Waking car", "bg-blue-600"],
    [{ state: "error" }, "Error", "bg-red-700"],
    [{ state: "idle", carPluggedIn: true }, "Idle", "bg-gray-700"],
    [{ state: "idle", carPluggedIn: false }, "Unplugged", "bg-gray-700"],
  ] as const)("%o -> %s", (o, label, bg) => {
    const b = statusBanner(snap(o));
    expect(b.label).toBe(label);
    expect(b.className).toContain(bg);
  });
});

describe("gridBadge", () => {
  it("shows import in red when drawing from the grid", () => {
    expect(gridBadge(snap({ gridWatts: 130 }))).toEqual({
      label: "Import 0.13 kW",
      importing: true,
    });
  });

  it("shows surplus when not importing", () => {
    expect(gridBadge(snap({ gridWatts: -2000, surplusWatts: 4200 }))).toEqual({
      label: "Surplus 4.20 kW",
      importing: false,
    });
  });

  it("treats small grid draw within the deadband as not importing", () => {
    expect(gridBadge(snap({ gridWatts: 40, surplusWatts: 0 })).importing).toBe(false);
  });
});

describe("batteryProgress", () => {
  it("describes time to limit while charging", () => {
    expect(batteryProgress(snap())).toEqual({
      pct: 64,
      limitPct: 80,
      caption: "to 80% in 2h 24m",
    });
  });

  it("reports when the limit is reached", () => {
    expect(batteryProgress(snap({ batteryPct: 80, timeToLimitHours: 0 })).caption).toBe(
      "limit 80% reached",
    );
  });

  it("shows the limit when not charging", () => {
    expect(
      batteryProgress(snap({ state: "monitoring", timeToLimitHours: 0 })).caption,
    ).toBe("limit 80%");
  });

  it("says when the car is not plugged in", () => {
    expect(batteryProgress(snap({ carPluggedIn: false })).caption).toBe("Not plugged in");
  });

  it("clamps out-of-range values and rounds the battery", () => {
    expect(batteryProgress(snap({ batteryPct: 120.6, chargeLimit: 0 }))).toMatchObject({
      pct: 100,
      limitPct: 0,
    });
    expect(batteryProgress(snap({ batteryPct: 63.6 })).pct).toBe(64);
  });
});

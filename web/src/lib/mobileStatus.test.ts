import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { StateSnapshot } from "@/api/types";
import DashboardPage from "@/pages/DashboardPage";
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
    vehicleDataStatus: "current",
    vehicleObservedAt: "2026-10-24T04:55:00Z",
    consecutiveLow: 0,
    consecutiveSurplus: 0,
    lastUpdate: "2026-10-24T05:00:00Z",
    lastError: "",
    ...overrides,
  };
}

describe("dashboard vehicle availability", () => {
  function renderDashboard(state: StateSnapshot) {
    const client = new QueryClient();
    client.setQueryData(["state"], state);
    try {
      return renderToStaticMarkup(
        createElement(QueryClientProvider, { client }, createElement(DashboardPage)),
      );
    } finally {
      client.clear();
    }
  }

  it("does not report an unplugged car or an empty battery from an offline startup", () => {
    const html = renderDashboard(snap({
      state: "idle",
      carOnline: false,
      carPluggedIn: false,
      chargingState: "",
      actualAmps: 0,
      targetAmps: 0,
      batteryPct: 0,
      chargeLimit: 0,
      timeToLimitHours: 0,
      vehicleDataStatus: "unknown",
      vehicleObservedAt: null,
    }));
    expect(html).not.toMatch(/unplugged|not plugged in/i);
    expect(html).toContain("Unknown");
    expect(html).not.toContain(">0 %<");
    expect(html).not.toContain(">0%<");
    expect(html).not.toContain(">80%<");
  });

  it("keeps a confirmed full, connected battery distinct from unplugged", () => {
    const html = renderDashboard(snap({
      state: "monitoring",
      chargingState: "Complete",
      batteryPct: 100,
      chargeLimit: 100,
      actualAmps: 0,
      targetAmps: 0,
      timeToLimitHours: 0,
    }));
    expect(html).not.toMatch(/unplugged|not plugged in/i);
    expect(html).toContain("Complete");
    expect(html).toContain("100 %");
    expect(html).toContain("limit 100% reached");
  });

  it("labels restored full-car readings as historical while keeping the vehicle offline", () => {
    const html = renderDashboard(snap({
      state: "idle",
      carOnline: false,
      carPluggedIn: true,
      chargingState: "Complete",
      batteryPct: 100,
      actualAmps: 0,
      targetAmps: 0,
      chargeLimit: 100,
      vehicleDataStatus: "last_known",
    }));
    expect(html).toContain("Vehicle offline");
    expect(html).toContain("Yes (last known)");
    expect(html).toContain("100 % (last known)");
    expect(html).toContain("Complete (last known)");
    expect(html).toContain("Last known: Complete");
    expect(html).toContain('aria-label="Battery (last known)"');
    expect(html).toContain("Vehicle last confirmed");
    expect(html).toContain("Solar updated");
    expect(html).not.toContain('id="pathHE"');
    expect(html).not.toMatch(/unplugged|not plugged in/i);
  });

  it("does not animate historical charging current or present it as a live measurement", () => {
    const html = renderDashboard(snap({
      state: "idle",
      carOnline: false,
      chargingState: "Charging",
      vehicleDataStatus: "last_known",
      targetAmps: 0,
    }));
    expect(html).toContain("Charging (last known)");
    expect(html).toContain("Last known: Charging");
    expect(html).not.toContain('id="pathHE"');
    expect(html).not.toContain("0 / 16 A");
    expect(html).not.toContain("to 80% in");
  });

  it("still reports a confirmed unplugged car and a measured zero battery", () => {
    const html = renderDashboard(snap({
      state: "idle",
      carOnline: true,
      carPluggedIn: false,
      chargingState: "Disconnected",
      batteryPct: 0,
    }));
    expect(html).toContain("Unplugged");
    expect(html).toContain("Not plugged in");
    expect(html).toContain(">0 %<");
    expect(html).toContain(">0%<");
  });
});

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

import type { ControllerState, StateSnapshot } from "@/api/types";
import { formatDurationHours, formatKW } from "./format";

// Grid draw above this is shown as importing; matches the desktop dashboard.
const IMPORT_DEADBAND_WATTS = 50;

const BANNER_BG: Record<ControllerState, string> = {
  charging: "bg-green-600 text-white",
  stopped_low_surplus: "bg-orange-600 text-white",
  monitoring: "bg-yellow-600 text-gray-950",
  wake_pending: "bg-blue-600 text-white",
  error: "bg-red-700 text-white",
  idle: "bg-gray-700 text-gray-100",
};

export function statusBanner(snap: StateSnapshot): { label: string; className: string } {
  const labels: Record<ControllerState, string> = {
    charging: `Charging · ${snap.actualAmps} A`,
    stopped_low_surplus: "Paused · low surplus",
    monitoring: "Monitoring",
    wake_pending: "Waking car",
    error: "Error",
    idle: snap.carPluggedIn ? "Idle" : "Unplugged",
  };
  return { label: labels[snap.state], className: BANNER_BG[snap.state] };
}

export function gridBadge(snap: StateSnapshot): { label: string; importing: boolean } {
  const importing = snap.gridWatts > IMPORT_DEADBAND_WATTS;
  return importing
    ? { label: `Import ${formatKW(snap.gridWatts)}`, importing }
    : { label: `Surplus ${formatKW(Math.max(snap.surplusWatts, 0))}`, importing };
}

const clampPct = (v: number) => Math.min(100, Math.max(0, v));

export function batteryProgress(snap: StateSnapshot): {
  pct: number;
  limitPct: number;
  caption: string;
} {
  const pct = Math.round(clampPct(snap.batteryPct));
  const limitPct = clampPct(snap.chargeLimit);

  let caption: string;
  if (!snap.carPluggedIn) {
    caption = "Not plugged in";
  } else if (limitPct > 0 && pct >= limitPct) {
    caption = `limit ${limitPct}% reached`;
  } else if (snap.state === "charging" && snap.timeToLimitHours > 0) {
    caption = `to ${limitPct}% in ${formatDurationHours(snap.timeToLimitHours)}`;
  } else {
    caption = `limit ${limitPct}%`;
  }
  return { pct, limitPct, caption };
}

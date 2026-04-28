import type { ControllerState } from "@/api/types";

export const STATE_COLORS: Record<ControllerState, string> = {
  charging: "text-green-400",
  idle: "text-gray-400",
  monitoring: "text-yellow-400",
  stopped_low_surplus: "text-orange-400",
  wake_pending: "text-blue-400",
  error: "text-red-400",
};

export const STATE_BG: Record<ControllerState, string> = {
  charging: "bg-green-400/15 text-green-300",
  idle: "bg-gray-400/15 text-gray-300",
  monitoring: "bg-yellow-400/15 text-yellow-300",
  stopped_low_surplus: "bg-orange-400/15 text-orange-300",
  wake_pending: "bg-blue-400/15 text-blue-300",
  error: "bg-red-400/15 text-red-300",
};

export function formatWatts(w: number): string {
  const abs = Math.abs(w);
  if (abs >= 1000) return `${(w / 1000).toFixed(2)} kW`;
  return `${w.toFixed(0)} W`;
}

export function formatKW(w: number, digits = 2): string {
  return `${(w / 1000).toFixed(digits)} kW`;
}

export function formatCurrency(v: number, digits = 2): string {
  return `$${v.toFixed(digits)}`;
}

export function formatTime(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

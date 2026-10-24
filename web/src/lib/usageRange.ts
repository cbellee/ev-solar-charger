export type UsageRange = "24h" | "7d" | "30d" | "all";

const rangeHours: Record<UsageRange, number | null> = {
  "24h": 24,
  "7d": 24 * 7,
  "30d": 24 * 30,
  all: null,
};

export type UsageRangeWindow = {
  params: { from: string; to: string };
  domain: [number, number] | ["dataMin", "dataMax"];
  showTime: boolean;
};

export function usageRangeWindow(range: UsageRange, nowMs: number): UsageRangeWindow {
  const hours = rangeHours[range];
  const fromMs = hours == null ? 0 : nowMs - hours * 3_600_000;
  return {
    params: { from: new Date(fromMs).toISOString(), to: new Date(nowMs).toISOString() },
    domain: hours == null ? ["dataMin", "dataMax"] : [fromMs, nowMs],
    showTime: range === "24h",
  };
}

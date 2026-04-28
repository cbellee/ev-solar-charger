import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { StateSnapshot } from "@/api/types";
import { PowerFlowDiagram } from "@/components/PowerFlowDiagram";
import { Card } from "@/components/Card";
import { ControlsPanel } from "@/components/ControlsPanel";
import { APIUsageCard } from "@/components/APIUsageCard";
import { STATE_BG, formatKW, formatTime } from "@/lib/format";
import clsx from "clsx";

export default function DashboardPage() {
  // Use cached state from the SSE stream; fall back to /api/state when the
  // stream hasn't pushed anything yet.
  const { data: snap, isLoading, error } = useQuery({
    queryKey: ["state"],
    queryFn: api.getState,
    staleTime: Infinity,
  });

  if (isLoading) return <p className="text-sm text-gray-400">Loading…</p>;
  if (error) return <p className="text-sm text-red-400">{(error as Error).message}</p>;
  if (!snap) return null;

  return (
    <div className="grid gap-6 lg:grid-cols-3">
      <Card title="System status" className="lg:col-span-2">
        <PowerFlowDiagram snap={snap} />
        <StatusFooter snap={snap} />
      </Card>

      <div className="space-y-6">
        <SolarCard snap={snap} />
        <EVChargingCard snap={snap} />
      </div>

      <ControlsPanel snap={snap} />
      <APIUsageCard />
    </div>
  );
}

function StatusFooter({ snap }: { snap: StateSnapshot }) {
  return (
    <div className="mt-4 grid grid-cols-2 sm:grid-cols-4 gap-3 text-xs">
      <Stat label="State" value={snap.state} className={clsx("uppercase tracking-wide rounded px-2 py-0.5 inline-block", STATE_BG[snap.state])} />
      <Stat label="Mode" value={snap.mode} />
      <Stat label="Vehicle" value={snap.carPluggedIn ? (snap.carOnline ? "online" : "offline") : "unplugged"} />
      <Stat label="Last update" value={formatTime(snap.lastUpdate)} />
      {snap.lastError && (
        <div className="col-span-full rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-red-300">
          <span className="font-medium">Error:</span> {snap.lastError}
        </div>
      )}
    </div>
  );
}

function Stat({
  label,
  value,
  className,
}: {
  label: string;
  value: React.ReactNode;
  className?: string;
}) {
  return (
    <div>
      <div className="text-gray-500">{label}</div>
      <div className={clsx("text-gray-100 font-medium", className)}>{value}</div>
    </div>
  );
}

function SolarCard({ snap }: { snap: StateSnapshot }) {
  return (
    <Card title="Solar Production">
      <div className="space-y-1.5 text-sm">
        <Row k="PV" v={formatKW(snap.pvWatts)} />
        <Row k="Load" v={formatKW(snap.loadWatts)} />
        <Row
          k="Grid"
          v={formatKW(snap.gridWatts)}
          vClass={snap.gridWatts > 50 ? "text-red-400" : snap.gridWatts < -50 ? "text-green-400" : "text-gray-300"}
        />
        <Row
          k="Surplus"
          v={formatKW(snap.surplusWatts)}
          vClass={snap.surplusWatts > 0 ? "text-green-400" : "text-gray-400"}
        />
      </div>
    </Card>
  );
}

function EVChargingCard({ snap }: { snap: StateSnapshot }) {
  return (
    <Card title="EV Charging">
      <div className="space-y-1.5 text-sm">
        <Row k="Plugged in" v={snap.carPluggedIn ? "Yes" : "No"} />
        <Row k="Charging state" v={snap.chargingState || "—"} />
        <Row k="Target / Actual" v={`${snap.targetAmps} / ${snap.actualAmps} A`} />
        <Row k="Battery" v={`${snap.batteryPct.toFixed(0)} %`} />
      </div>
    </Card>
  );
}

function Row({ k, v, vClass }: { k: string; v: React.ReactNode; vClass?: string }) {
  return (
    <div className="flex justify-between">
      <span className="text-gray-400">{k}</span>
      <span className={clsx("tabular-nums", vClass ?? "text-gray-200")}>{v}</span>
    </div>
  );
}

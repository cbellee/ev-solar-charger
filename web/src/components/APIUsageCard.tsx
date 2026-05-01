import { useQuery } from "@tanstack/react-query";
import { api, getErrorMessage } from "@/api/client";
import { Card } from "./Card";
import clsx from "clsx";

export function APIUsageCard() {
  const { data, isLoading, error } = useQuery({
    queryKey: ["api-usage"],
    queryFn: api.getAPIUsage,
    refetchInterval: 60_000,
    staleTime: 30_000,
  });

  return (
    <Card title="Tesla API Usage (this month)">
      {isLoading && <p className="text-xs text-gray-500">Loading…</p>}
      {error && <p className="text-xs text-red-400">{getErrorMessage(error)}</p>}
      {data && (
        <div className="space-y-3">
          <UsageBar label="Data" calls={data.dataCalls} cost={data.dataCost} budget={data.monthlyDiscount / 4} />
          <UsageBar label="Command" calls={data.commandCalls} cost={data.commandCost} budget={data.monthlyDiscount / 4} />
          <UsageBar label="Wake" calls={data.wakeCalls} cost={data.wakeCost} budget={data.monthlyDiscount / 4} />
          <UsageBar label="Stream" calls={data.streamSignals} cost={data.streamCost} budget={data.monthlyDiscount / 4} digits={4} />
          <div className="pt-2 border-t border-gray-700/50 text-xs space-y-1">
            <Row k="Estimated cost" v={`$${data.estimatedCost.toFixed(2)}`} />
            <Row k="Monthly credit" v={`-$${data.monthlyDiscount.toFixed(2)}`} />
            <Row
              k="Net cost"
              v={`$${data.netCost.toFixed(2)}`}
              vClass={data.netCost > 0 ? "text-red-400" : "text-green-400"}
            />
            {data.monthStarted && (
              <Row k="Month started" v={new Date(data.monthStarted).toLocaleDateString()} />
            )}
          </div>
        </div>
      )}
    </Card>
  );
}

function UsageBar({
  label,
  calls,
  cost,
  budget,
  digits = 2,
}: {
  label: string;
  calls: number;
  cost: number;
  budget: number;
  digits?: number;
}) {
  const pct = budget > 0 ? Math.min(200, (cost / budget) * 100) : 0;
  const color =
    pct >= 100 ? "bg-red-500" : pct >= 80 ? "bg-yellow-500" : "bg-blue-500";
  return (
    <div>
      <div className="flex items-baseline justify-between text-xs mb-1">
        <span className="text-gray-300 font-medium">{label}</span>
        <span className="text-gray-400 tabular-nums">
          {calls.toLocaleString()} calls · ${cost.toFixed(digits)}
        </span>
      </div>
      <div className="h-1.5 w-full rounded bg-gray-700/60 overflow-hidden">
        <div className={clsx("h-full transition-all", color)} style={{ width: `${Math.min(100, pct)}%` }} />
      </div>
    </div>
  );
}

function Row({ k, v, vClass }: { k: string; v: string; vClass?: string }) {
  return (
    <div className="flex justify-between">
      <span className="text-gray-400">{k}</span>
      <span className={clsx("tabular-nums", vClass ?? "text-gray-200")}>{v}</span>
    </div>
  );
}

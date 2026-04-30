import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Area,
  AreaChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { api } from "@/api/client";
import { Card } from "@/components/Card";

type Range = "24h" | "7d" | "30d" | "all";

const rangeHours: Record<Range, number | null> = {
  "24h": 24,
  "7d": 24 * 7,
  "30d": 24 * 30,
  all: null,
};

export default function UsagePage() {
  const [range, setRange] = useState<Range>("7d");

  const { data, isLoading, error } = useQuery({
    queryKey: ["usage-history"],
    queryFn: () => api.getAPIUsageHistory(),
    refetchInterval: 60_000,
  });

  const filtered = useMemo(() => {
    if (!data) return [];
    const cutoffHours = rangeHours[range];
    const cutoff =
      cutoffHours == null ? 0 : Date.now() - cutoffHours * 3_600_000;
    return data
      .slice()
      .reverse()
      .filter((s) => new Date(s.Timestamp).getTime() >= cutoff);
  }, [data, range]);

  const chartData = useMemo(
    () =>
      filtered.map((s) => ({
        ts: new Date(s.Timestamp).getTime(),
        data: s.DataCalls,
        command: s.CommandCalls,
        wake: s.WakeCalls,
        stream: s.StreamSignals,
        cost: s.EstimatedCost,
      })),
    [filtered],
  );

  // Per-snapshot deltas (rate over time). Cumulative counters reset monthly,
  // so any negative diff (start of new month) is treated as zero.
  const deltas = useMemo(() => {
    const out: {
      ts: number;
      data: number;
      command: number;
      wake: number;
      stream: number;
    }[] = [];
    for (let i = 1; i < chartData.length; i++) {
      const prev = chartData[i - 1];
      const cur = chartData[i];
      out.push({
        ts: cur.ts,
        data: Math.max(0, cur.data - prev.data),
        command: Math.max(0, cur.command - prev.command),
        wake: Math.max(0, cur.wake - prev.wake),
        stream: Math.max(0, cur.stream - prev.stream),
      });
    }
    return out;
  }, [chartData]);

  const latest = chartData[chartData.length - 1];

  return (
    <div className="space-y-6">
      <Card title="Tesla API Usage History">
        <div className="flex flex-wrap items-end gap-3 mb-4">
          <Field label="Range">
            <select
              value={range}
              onChange={(e) => setRange(e.target.value as Range)}
              className={inputCls}
            >
              <option value="24h">Last 24h</option>
              <option value="7d">Last 7 days</option>
              <option value="30d">Last 30 days</option>
              <option value="all">All</option>
            </select>
          </Field>
          {latest && (
            <div className="ml-auto flex flex-wrap gap-4 text-xs text-gray-300">
              <Stat label="Latest cost" value={`$${latest.cost.toFixed(2)}`} />
              <Stat label="Data" value={latest.data.toLocaleString()} />
              <Stat label="Command" value={latest.command.toLocaleString()} />
              <Stat label="Wake" value={latest.wake.toLocaleString()} />
              <Stat label="Stream" value={latest.stream.toLocaleString()} />
            </div>
          )}
        </div>

        {isLoading && <p className="text-sm text-gray-400">Loading…</p>}
        {error && (
          <p className="text-sm text-red-400">{(error as Error).message}</p>
        )}

        {chartData.length > 0 && (
          <>
            <h3 className="text-sm font-medium text-gray-300 mt-2 mb-2">
              Estimated cumulative cost (resets monthly)
            </h3>
            <div className="h-56 w-full">
              <ResponsiveContainer>
                <LineChart data={chartData}>
                  <CartesianGrid stroke="#374151" strokeDasharray="3 3" />
                  <XAxis
                    dataKey="ts"
                    type="number"
                    domain={["dataMin", "dataMax"]}
                    tickFormatter={(t) =>
                      new Date(t).toLocaleDateString(undefined, {
                        month: "short",
                        day: "numeric",
                      })
                    }
                    stroke="#9ca3af"
                    fontSize={11}
                  />
                  <YAxis
                    stroke="#9ca3af"
                    fontSize={11}
                    tickFormatter={(v) => `$${Number(v).toFixed(2)}`}
                  />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: "#1f2937",
                      border: "1px solid #374151",
                    }}
                    labelFormatter={(t) =>
                      new Date(t as number).toLocaleString()
                    }
                    formatter={(v: number) => `$${v.toFixed(4)}`}
                  />
                  <Line
                    type="monotone"
                    dataKey="cost"
                    stroke="#10b981"
                    name="Cost"
                    dot={false}
                  />
                </LineChart>
              </ResponsiveContainer>
            </div>

            <h3 className="text-sm font-medium text-gray-300 mt-6 mb-2">
              Cumulative calls by type
            </h3>
            <div className="h-56 w-full">
              <ResponsiveContainer>
                <LineChart data={chartData}>
                  <CartesianGrid stroke="#374151" strokeDasharray="3 3" />
                  <XAxis
                    dataKey="ts"
                    type="number"
                    domain={["dataMin", "dataMax"]}
                    tickFormatter={(t) =>
                      new Date(t).toLocaleDateString(undefined, {
                        month: "short",
                        day: "numeric",
                      })
                    }
                    stroke="#9ca3af"
                    fontSize={11}
                  />
                  <YAxis stroke="#9ca3af" fontSize={11} />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: "#1f2937",
                      border: "1px solid #374151",
                    }}
                    labelFormatter={(t) =>
                      new Date(t as number).toLocaleString()
                    }
                  />
                  <Legend />
                  <Line
                    type="monotone"
                    dataKey="data"
                    stroke="#60a5fa"
                    name="Data"
                    dot={false}
                  />
                  <Line
                    type="monotone"
                    dataKey="command"
                    stroke="#f472b6"
                    name="Command"
                    dot={false}
                  />
                  <Line
                    type="monotone"
                    dataKey="wake"
                    stroke="#fbbf24"
                    name="Wake"
                    dot={false}
                  />
                  <Line
                    type="monotone"
                    dataKey="stream"
                    stroke="#a78bfa"
                    name="Stream"
                    dot={false}
                  />
                </LineChart>
              </ResponsiveContainer>
            </div>

            {deltas.length > 0 && (
              <>
                <h3 className="text-sm font-medium text-gray-300 mt-6 mb-2">
                  Calls per snapshot interval
                </h3>
                <div className="h-56 w-full">
                  <ResponsiveContainer>
                    <AreaChart data={deltas} stackOffset="none">
                      <CartesianGrid stroke="#374151" strokeDasharray="3 3" />
                      <XAxis
                        dataKey="ts"
                        type="number"
                        domain={["dataMin", "dataMax"]}
                        tickFormatter={(t) =>
                          new Date(t).toLocaleDateString(undefined, {
                            month: "short",
                            day: "numeric",
                          })
                        }
                        stroke="#9ca3af"
                        fontSize={11}
                      />
                      <YAxis stroke="#9ca3af" fontSize={11} />
                      <Tooltip
                        contentStyle={{
                          backgroundColor: "#1f2937",
                          border: "1px solid #374151",
                        }}
                        labelFormatter={(t) =>
                          new Date(t as number).toLocaleString()
                        }
                      />
                      <Legend />
                      <Area
                        type="monotone"
                        dataKey="data"
                        stackId="1"
                        stroke="#60a5fa"
                        fill="#60a5fa"
                        fillOpacity={0.4}
                        name="Data"
                      />
                      <Area
                        type="monotone"
                        dataKey="command"
                        stackId="1"
                        stroke="#f472b6"
                        fill="#f472b6"
                        fillOpacity={0.4}
                        name="Command"
                      />
                      <Area
                        type="monotone"
                        dataKey="wake"
                        stackId="1"
                        stroke="#fbbf24"
                        fill="#fbbf24"
                        fillOpacity={0.4}
                        name="Wake"
                      />
                      <Area
                        type="monotone"
                        dataKey="stream"
                        stackId="1"
                        stroke="#a78bfa"
                        fill="#a78bfa"
                        fillOpacity={0.4}
                        name="Stream"
                      />
                    </AreaChart>
                  </ResponsiveContainer>
                </div>
              </>
            )}
          </>
        )}

        {!isLoading && chartData.length === 0 && (
          <p className="text-sm text-gray-400">
            No usage snapshots in the selected range yet.
          </p>
        )}
      </Card>
    </div>
  );
}

const inputCls =
  "rounded bg-gray-900 border border-gray-700 px-2 py-1 text-sm text-gray-100";

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1 text-xs text-gray-400">
      {label}
      {children}
    </label>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col">
      <span className="text-[10px] uppercase tracking-wide text-gray-500">
        {label}
      </span>
      <span className="text-sm tabular-nums text-gray-200">{value}</span>
    </div>
  );
}

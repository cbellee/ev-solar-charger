import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { api, getErrorMessage } from "@/api/client";
import type { HistoryInterval } from "@/api/types";
import { Card } from "@/components/Card";

function isoMinusHours(h: number): string {
  return new Date(Date.now() - h * 3_600_000).toISOString();
}

export default function HistoryPage() {
  const [from, setFrom] = useState<string>(isoMinusHours(24));
  const [to, setTo] = useState<string>(new Date().toISOString());
  const [interval, setInterval] = useState<HistoryInterval>("minute");
  const [limit, setLimit] = useState<number>(500);

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ["history", from, to, interval, limit],
    queryFn: () => api.getHistory({ from, to, interval, limit, offset: 0 }),
  });

  const chartData = (data ?? [])
    .slice()
    .reverse()
    .map((r) => ({
      ts: new Date(r.Timestamp).getTime(),
      pv: r.PVWatts,
      load: r.LoadWatts,
      grid: r.GridWatts,
      surplus: r.SurplusWatts,
    }));

  return (
    <div className="space-y-6">
      <Card title="History">
        <div className="flex flex-wrap items-end gap-3 mb-4">
          <Field label="From">
            <input type="datetime-local" aria-label="From" title="From" value={toLocalInput(from)} onChange={(e) => setFrom(fromLocalInput(e.target.value))} className={inputCls} />
          </Field>
          <Field label="To">
            <input type="datetime-local" aria-label="To" title="To" value={toLocalInput(to)} onChange={(e) => setTo(fromLocalInput(e.target.value))} className={inputCls} />
          </Field>
          <Field label="Interval">
            <select aria-label="Interval" title="Interval" value={interval} onChange={(e) => setInterval(e.target.value as HistoryInterval)} className={inputCls}>
              <option value="minute">minute</option>
              <option value="hour">hour</option>
              <option value="day">day</option>
            </select>
          </Field>
          <Field label="Limit">
            <input type="number" aria-label="Limit" title="Limit" min={1} max={1000} value={limit} onChange={(e) => setLimit(Number(e.target.value))} className={inputCls} />
          </Field>
          <button type="button" onClick={() => refetch()} className="rounded bg-blue-600 hover:bg-blue-500 px-3 py-1.5 text-sm font-medium">
            Refresh
          </button>
        </div>

        {isLoading && <p className="text-sm text-gray-400">Loading…</p>}
  {error && <p className="text-sm text-red-400">{getErrorMessage(error)}</p>}

        {chartData.length > 0 && (
          <div className="h-72 w-full">
            <ResponsiveContainer>
              <LineChart data={chartData}>
                <CartesianGrid stroke="#374151" strokeDasharray="3 3" />
                <XAxis
                  dataKey="ts"
                  type="number"
                  domain={["dataMin", "dataMax"]}
                  tickFormatter={(t) => new Date(t).toLocaleTimeString()}
                  stroke="#9ca3af"
                  fontSize={11}
                />
                <YAxis stroke="#9ca3af" fontSize={11} />
                <Tooltip
                  contentStyle={{ backgroundColor: "#1f2937", border: "1px solid #374151" }}
                  labelFormatter={(t) => new Date(t as number).toLocaleString()}
                />
                <Legend />
                <Line type="monotone" dataKey="pv" stroke="#fbbf24" name="PV (W)" dot={false} />
                <Line type="monotone" dataKey="load" stroke="#a78bfa" name="Load (W)" dot={false} />
                <Line type="monotone" dataKey="grid" stroke="#ef4444" name="Grid (W)" dot={false} />
                <Line type="monotone" dataKey="surplus" stroke="#10b981" name="Surplus (W)" dot={false} />
              </LineChart>
            </ResponsiveContainer>
          </div>
        )}
      </Card>

      {data && data.length > 0 && (
        <Card title={`Readings (${data.length})`}>
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead className="text-gray-400 border-b border-gray-700">
                <tr>
                  <Th>Timestamp</Th>
                  <Th>State</Th>
                  <Th>PV</Th>
                  <Th>Load</Th>
                  <Th>Grid</Th>
                  <Th>Surplus</Th>
                  <Th>Amps</Th>
                  <Th>Batt %</Th>
                </tr>
              </thead>
              <tbody>
                {data.slice(0, 200).map((r) => (
                  <tr key={r.ID} className="border-b border-gray-800/60">
                    <Td>{new Date(r.Timestamp).toLocaleString()}</Td>
                    <Td>{r.State}</Td>
                    <Td>{r.PVWatts.toFixed(0)}</Td>
                    <Td>{r.LoadWatts.toFixed(0)}</Td>
                    <Td>{r.GridWatts.toFixed(0)}</Td>
                    <Td>{r.SurplusWatts.toFixed(0)}</Td>
                    <Td>{r.ChargeAmps}</Td>
                    <Td>{r.BatteryPct.toFixed(0)}</Td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}

const inputCls =
  "rounded bg-gray-900 border border-gray-700 px-2 py-1 text-sm text-gray-100";

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs text-gray-400">
      {label}
      {children}
    </label>
  );
}

function Th({ children }: { children: React.ReactNode }) {
  return <th className="px-2 py-1.5 text-left font-medium">{children}</th>;
}
function Td({ children }: { children: React.ReactNode }) {
  return <td className="px-2 py-1 tabular-nums text-gray-200">{children}</td>;
}

function toLocalInput(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromLocalInput(v: string): string {
  if (!v) return new Date().toISOString();
  return new Date(v).toISOString();
}

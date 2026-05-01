import { useQuery } from "@tanstack/react-query";
import { api, getErrorMessage } from "@/api/client";
import { Card } from "@/components/Card";

export default function SessionsPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ["sessions"],
    queryFn: api.getSessions,
  });

  return (
    <Card title="Charge Sessions (last 30 days)">
      {isLoading && <p className="text-sm text-gray-400">Loading…</p>}
      {error && <p className="text-sm text-red-400">{getErrorMessage(error)}</p>}
      {data && data.length === 0 && <p className="text-sm text-gray-400">No sessions yet.</p>}
      {data && data.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead className="text-gray-400 border-b border-gray-700">
              <tr>
                <Th>Start</Th>
                <Th>End</Th>
                <Th>Duration</Th>
                <Th>Battery</Th>
                <Th>kWh</Th>
                <Th>Peak A</Th>
                <Th>Avg A</Th>
              </tr>
            </thead>
            <tbody>
              {data.map((s) => {
                const start = new Date(s.StartTime);
                const end = new Date(s.EndTime);
                const ms = end.getTime() - start.getTime();
                const mins = Math.max(0, Math.round(ms / 60_000));
                return (
                  <tr key={s.ID} className="border-b border-gray-800/60">
                    <Td>{start.toLocaleString()}</Td>
                    <Td>{end.toLocaleString()}</Td>
                    <Td>{`${Math.floor(mins / 60)}h ${mins % 60}m`}</Td>
                    <Td>{`${s.StartBattery.toFixed(0)} → ${s.EndBattery.toFixed(0)}%`}</Td>
                    <Td>{s.EnergyKWh.toFixed(2)}</Td>
                    <Td>{s.PeakAmps}</Td>
                    <Td>{s.AvgAmps.toFixed(1)}</Td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function Th({ children }: { children: React.ReactNode }) {
  return <th className="px-2 py-1.5 text-left font-medium">{children}</th>;
}
function Td({ children }: { children: React.ReactNode }) {
  return <td className="px-2 py-1 tabular-nums text-gray-200">{children}</td>;
}

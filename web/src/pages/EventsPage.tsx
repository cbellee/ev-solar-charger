import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, getErrorMessage } from "@/api/client";
import { Card } from "@/components/Card";

export default function EventsPage() {
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");

  useEffect(() => {
    const t = setTimeout(() => setDebounced(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  const { data, isLoading, error } = useQuery({
    queryKey: ["events", debounced],
    queryFn: () =>
      debounced ? api.searchEvents(debounced) : api.listEvents(100),
    enabled: true,
  });

  return (
    <Card
      title="Events"
      action={
        <input
          type="search"
          placeholder="Search events…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="rounded bg-gray-900 border border-gray-700 px-2 py-1 text-sm text-gray-100"
        />
      }
    >
      {isLoading && <p className="text-sm text-gray-400">Loading…</p>}
      {error && <p className="text-sm text-red-400">{getErrorMessage(error)}</p>}
      {data && data.length === 0 && <p className="text-sm text-gray-400">No events.</p>}
      {data && data.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead className="text-gray-400 border-b border-gray-700">
              <tr>
                <Th>Timestamp</Th>
                <Th>Type</Th>
                <Th>Message</Th>
                <Th>Details</Th>
              </tr>
            </thead>
            <tbody>
              {data.map((e) => (
                <tr key={e.ID} className="border-b border-gray-800/60">
                  <Td>{new Date(e.Timestamp).toLocaleString()}</Td>
                  <Td>
                    <span className="rounded bg-gray-700/60 px-1.5 py-0.5 text-[10px] uppercase tracking-wide">
                      {e.Type}
                    </span>
                  </Td>
                  <Td>{e.Message}</Td>
                  <Td className="text-gray-400 max-w-md truncate" title={e.Details}>
                    {e.Details}
                  </Td>
                </tr>
              ))}
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
function Td({
  children,
  className,
  title,
}: {
  children: React.ReactNode;
  className?: string;
  title?: string;
}) {
  return (
    <td className={`px-2 py-1 text-gray-200 ${className ?? ""}`} title={title}>
      {children}
    </td>
  );
}

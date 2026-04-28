import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { StateSnapshot } from "@/api/types";

export type SSEStatus = "connecting" | "open" | "error" | "closed";

// Subscribes to /events SSE and seeds the TanStack Query cache for ["state"].
// Reconnects automatically via the browser's built-in EventSource behaviour.
export function useStateStream(): { status: SSEStatus; snapshot: StateSnapshot | null } {
  const qc = useQueryClient();
  const [status, setStatus] = useState<SSEStatus>("connecting");
  const [snapshot, setSnapshot] = useState<StateSnapshot | null>(
    () => qc.getQueryData<StateSnapshot>(["state"]) ?? null,
  );

  useEffect(() => {
    const es = new EventSource("/events", { withCredentials: true });

    es.onopen = () => setStatus("open");

    es.onmessage = (ev) => {
      try {
        const snap = JSON.parse(ev.data) as StateSnapshot;
        qc.setQueryData(["state"], snap);
        setSnapshot(snap);
      } catch {
        // ignore malformed payload
      }
    };

    es.onerror = () => {
      setStatus("error");
    };

    return () => {
      es.close();
      setStatus("closed");
    };
  }, [qc]);

  return { status, snapshot };
}

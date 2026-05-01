import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { fetchEventSource } from "@microsoft/fetch-event-source";
import type { StateSnapshot } from "@/api/types";
import { AuthError, getErrorMessage } from "@/api/client";
import { useIdToken } from "@/auth/useIdToken";

export type SSEStatus = "connecting" | "open" | "error" | "closed" | "auth-error";

// Subscribes to /events SSE and seeds the TanStack Query cache for ["state"].
// Uses @microsoft/fetch-event-source so we can attach the MSAL ID token via
// the Authorization header (native EventSource cannot set headers).
export function useStateStream(): {
  status: SSEStatus;
  snapshot: StateSnapshot | null;
  error: string | null;
} {
  const qc = useQueryClient();
  const getToken = useIdToken();
  const [status, setStatus] = useState<SSEStatus>("connecting");
  const [error, setError] = useState<string | null>(null);
  const [snapshot, setSnapshot] = useState<StateSnapshot | null>(
    () => qc.getQueryData<StateSnapshot>(["state"]) ?? null,
  );

  useEffect(() => {
    const ctrl = new AbortController();

    void fetchEventSource("/events", {
      signal: ctrl.signal,
      fetch: async (input, init) => {
        let token: string;
        try {
          token = await getToken();
        } catch (streamError) {
          throw new AuthError(getErrorMessage(streamError));
        }

        const headers = new Headers(init?.headers);
        headers.set("Authorization", `Bearer ${token}`);
        return fetch(input, { ...init, headers });
      },
      openWhenHidden: true,
      onopen: async (res) => {
        if (res.ok && res.headers.get("content-type")?.includes("text/event-stream")) {
          setError(null);
          setStatus("open");
          return;
        }
        if (res.status === 401 || res.status === 403) {
          throw new AuthError("Sign in again to continue.");
        }
        setError(`unexpected SSE response: ${res.status}`);
        setStatus("error");
        throw new Error(`unexpected SSE response: ${res.status}`);
      },
      onmessage: (ev) => {
        if (!ev.data) return;
        try {
          const snap = JSON.parse(ev.data) as StateSnapshot;
          qc.setQueryData(["state"], snap);
          setSnapshot(snap);
        } catch {
          // ignore malformed payload
        }
      },
      onerror: (streamError) => {
        const message = getErrorMessage(streamError);
        if (streamError instanceof AuthError) {
          qc.removeQueries({ queryKey: ["state"], exact: true });
          setSnapshot(null);
          setError(message);
          setStatus("auth-error");
          ctrl.abort();
          throw streamError;
        }

        setError(message);
        setStatus("error");
      },
      onclose: () => {
        setStatus((current) => (current === "auth-error" ? current : "closed"));
      },
    });

    return () => {
      ctrl.abort();
      setStatus("closed");
    };
  }, [qc, getToken]);

  return { status, snapshot, error };
}

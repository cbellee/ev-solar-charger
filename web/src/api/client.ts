import type {
  APIUsageResponse,
  APIUsageSnapshot,
  ChargeSession,
  ControlAction,
  EventRecord,
  HistoryQuery,
  Mode,
  Reading,
  StateSnapshot,
} from "./types";

async function jsonFetch<T>(input: RequestInfo, init?: RequestInit): Promise<T> {
  const res = await fetch(input, {
    credentials: "same-origin",
    ...init,
    headers: {
      Accept: "application/json",
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // ignore parse failures
    }
    throw new Error(message);
  }
  return (await res.json()) as T;
}

export const api = {
  getState(): Promise<StateSnapshot> {
    return jsonFetch<StateSnapshot>("/api/state");
  },

  setMode(mode: Mode): Promise<{ result: string }> {
    return jsonFetch("/api/mode", {
      method: "POST",
      body: JSON.stringify({ mode }),
    });
  },

  control(action: ControlAction, amps?: number): Promise<{ result: string }> {
    const body: Record<string, unknown> = { action };
    if (typeof amps === "number") body.amps = amps;
    return jsonFetch("/api/control", {
      method: "POST",
      body: JSON.stringify(body),
    });
  },

  getHistory(q: HistoryQuery): Promise<Reading[]> {
    const params = new URLSearchParams({
      from: q.from,
      to: q.to,
      interval: q.interval,
      limit: String(q.limit),
      offset: String(q.offset),
    });
    return jsonFetch<Reading[]>(`/api/history?${params}`);
  },

  getSessions(): Promise<ChargeSession[]> {
    return jsonFetch<ChargeSession[]>("/api/sessions");
  },

  searchEvents(q: string): Promise<EventRecord[]> {
    return jsonFetch<EventRecord[]>(`/api/search?q=${encodeURIComponent(q)}`);
  },

  getAPIUsage(): Promise<APIUsageResponse> {
    return jsonFetch<APIUsageResponse>("/api/usage");
  },

  getAPIUsageHistory(): Promise<APIUsageSnapshot[]> {
    return jsonFetch<APIUsageSnapshot[]>("/api/usage/history");
  },
};

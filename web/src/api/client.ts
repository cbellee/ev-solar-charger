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

// TokenGetter is supplied by the React tree (typically via useIdToken) so the
// fetch helpers don't have to depend on MSAL directly. Returning null disables
// the Authorization header (useful for tests); throwing surfaces auth failures
// to the caller instead of silently degrading to unauthenticated requests.
export type TokenGetter = () => Promise<string | null>;

let getToken: TokenGetter = async () => null;

export class AuthError extends Error {
  constructor(message = "Sign in again to continue.") {
    super(message);
    this.name = "AuthError";
  }
}

export function getErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message.trim() !== "") {
    return error.message;
  }
  if (typeof error === "string" && error.trim() !== "") {
    return error;
  }
  return "Unexpected error";
}

function toAuthError(error: unknown): AuthError {
  if (error instanceof AuthError) {
    return error;
  }

  const message = getErrorMessage(error);
  if (message === "no signed-in account") {
    return new AuthError("Sign in again to continue.");
  }

  return new AuthError(message);
}

export function setTokenGetter(fn: TokenGetter): void {
  getToken = fn;
}

export function currentTokenGetter(): TokenGetter {
  return getToken;
}

async function jsonFetch<T>(input: RequestInfo, init?: RequestInit): Promise<T> {
  let token: string | null;
  try {
    token = await getToken();
  } catch (error) {
    throw toAuthError(error);
  }

  const baseHeaders: Record<string, string> = {
    Accept: "application/json",
    ...(init?.body ? { "Content-Type": "application/json" } : {}),
    ...((init?.headers as Record<string, string> | undefined) ?? {}),
  };
  if (token) {
    baseHeaders.Authorization = `Bearer ${token}`;
  }

  const res = await fetch(input, {
    credentials: "same-origin",
    ...init,
    headers: baseHeaders,
  });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // ignore parse failures
    }

    if (res.status === 401 || res.status === 403) {
      throw new AuthError(message);
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

  forceRefresh(): Promise<{ result: string }> {
    return jsonFetch("/api/refresh", { method: "POST" });
  },

  control(action: ControlAction, amps?: number): Promise<{ result: string }> {
    const body: Record<string, unknown> = { action };
    if (typeof amps === "number") body.amps = amps;
    return jsonFetch("/api/control", {
      method: "POST",
      body: JSON.stringify(body),
    });
  },

  setChargeLimit(percent: number): Promise<{ result: string }> {
    return jsonFetch("/api/charge-limit", {
      method: "POST",
      body: JSON.stringify({ percent }),
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

  listEvents(limit = 100): Promise<EventRecord[]> {
    return jsonFetch<EventRecord[]>(`/api/events?limit=${limit}`);
  },

  getAPIUsage(): Promise<APIUsageResponse> {
    return jsonFetch<APIUsageResponse>("/api/usage");
  },

  getAPIUsageHistory(): Promise<APIUsageSnapshot[]> {
    return jsonFetch<APIUsageSnapshot[]>("/api/usage/history");
  },
};

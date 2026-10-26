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
import { CSRF_HEADER } from "@/auth/serverSession";

// TokenGetter is supplied by the React tree (typically via useIdToken) so the
// fetch helpers don't have to depend on MSAL directly. Returning null omits
// the Authorization header so the server session cookie is used instead; throwing surfaces auth failures
// to the caller instead of silently degrading to unauthenticated requests.
// forceRefresh asks for a newly minted token after the backend rejected the
// cached one.
export type TokenGetter = (options?: { forceRefresh?: boolean }) => Promise<string | null>;

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

async function fetchWithToken(
  getTokenFn: TokenGetter,
  forceRefresh: boolean,
  input: RequestInfo | URL,
  init?: RequestInit,
): Promise<Response> {
  let token: string | null;
  try {
    token = await getTokenFn(forceRefresh ? { forceRefresh } : undefined);
  } catch (error) {
    throw toAuthError(error);
  }

  const headers = new Headers(init?.headers);
  if (token) {
    headers.set("Authorization", "Bearer " + token);
  }
  // Required by the server for cookie-authenticated writes (CSRF defence).
  headers.set(CSRF_HEADER, "XMLHttpRequest");
  return fetch(input, { ...init, headers });
}

// authorizedFetch attaches the bearer token and, if the backend rejects it
// with 401 (typically an expired ID token), forces one silent token refresh
// and retries. 403 means the user is authenticated but not allowed, so it is
// returned unchanged. Request bodies must be replayable (strings, not streams).
export async function authorizedFetch(
  getTokenFn: TokenGetter,
  input: RequestInfo | URL,
  init?: RequestInit,
): Promise<Response> {
  const res = await fetchWithToken(getTokenFn, false, input, init);
  if (res.status !== 401) {
    return res;
  }
  await res.body?.cancel();
  return fetchWithToken(getTokenFn, true, input, init);
}

async function jsonFetch<T>(input: RequestInfo, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  if (!headers.has("Accept")) {
    headers.set("Accept", "application/json");
  }
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const res = await authorizedFetch(getToken, input, {
    credentials: "same-origin",
    ...init,
    headers,
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

  getAPIUsageHistory(
    params: { from?: string; to?: string; points?: number } = {},
  ): Promise<APIUsageSnapshot[]> {
    const qs = new URLSearchParams();
    if (params.from) qs.set("from", params.from);
    if (params.to) qs.set("to", params.to);
    if (params.points) qs.set("points", String(params.points));
    const query = qs.toString();
    return jsonFetch<APIUsageSnapshot[]>(
      `/api/usage/history${query ? `?${query}` : ""}`,
    );
  },
};

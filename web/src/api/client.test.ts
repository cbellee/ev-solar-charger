import { afterEach, describe, expect, it, vi } from "vitest";
import { api, AuthError, authorizedFetch, setTokenGetter, type TokenGetter } from "./client";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function tokenGetter(): TokenGetter & ReturnType<typeof vi.fn> {
  return vi.fn(async (options?: { forceRefresh?: boolean }) =>
    options?.forceRefresh ? "fresh-token" : "stale-token",
  );
}

function authHeader(call: unknown[]): string | null {
  const init = call[1] as RequestInit;
  return new Headers(init.headers).get("Authorization");
}

afterEach(() => {
  vi.unstubAllGlobals();
  setTokenGetter(async () => null);
});

describe("authorizedFetch", () => {
  it("does not refresh when the first request succeeds", async () => {
    const getToken = tokenGetter();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, {}));
    vi.stubGlobal("fetch", fetchMock);

    const res = await authorizedFetch(getToken, "/api/state");

    expect(res.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(authHeader(fetchMock.mock.calls[0])).toBe("Bearer stale-token");
    expect(getToken).toHaveBeenCalledTimes(1);
  });

  // Regression for #8: an expired ID token must trigger one silent refresh
  // and retry rather than surfacing "Sign in again".
  it("forces a token refresh and retries once on 401", async () => {
    const getToken = tokenGetter();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { error: "invalid_token" }))
      .mockResolvedValueOnce(jsonResponse(200, {}));
    vi.stubGlobal("fetch", fetchMock);

    const res = await authorizedFetch(getToken, "/events");

    expect(res.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(authHeader(fetchMock.mock.calls[1])).toBe("Bearer fresh-token");
    expect(getToken).toHaveBeenLastCalledWith({ forceRefresh: true });
  });

  it("returns the second 401 without retrying again", async () => {
    const getToken = tokenGetter();
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse(401, { error: "invalid_token" }));
    vi.stubGlobal("fetch", fetchMock);

    const res = await authorizedFetch(getToken, "/api/state");

    expect(res.status).toBe(401);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("does not retry 403 responses", async () => {
    const getToken = tokenGetter();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(403, { error: "user_not_allowed" }));
    vi.stubGlobal("fetch", fetchMock);

    const res = await authorizedFetch(getToken, "/api/state");

    expect(res.status).toBe(403);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("wraps token acquisition failures in AuthError", async () => {
    const getToken = vi.fn().mockRejectedValue(new Error("no signed-in account"));
    vi.stubGlobal("fetch", vi.fn());

    await expect(authorizedFetch(getToken, "/api/state")).rejects.toBeInstanceOf(AuthError);
  });
});

describe("api requests", () => {
  it("retries a POST with its body after a 401", async () => {
    setTokenGetter(tokenGetter());
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { error: "invalid_token" }))
      .mockResolvedValueOnce(jsonResponse(200, { result: "ok" }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.setMode("solar" as never)).resolves.toEqual({ result: "ok" });
    const retryInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect(retryInit.method).toBe("POST");
    expect(retryInit.body).toBe(JSON.stringify({ mode: "solar" }));
    expect(new Headers(retryInit.headers).get("Content-Type")).toBe("application/json");
  });

  it("surfaces AuthError when the refreshed token is also rejected", async () => {
    setTokenGetter(tokenGetter());
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(async () => jsonResponse(401, { error: "invalid_token" })),
    );

    await expect(api.getState()).rejects.toBeInstanceOf(AuthError);
  });
});

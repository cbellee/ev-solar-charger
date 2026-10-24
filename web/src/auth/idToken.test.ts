import { describe, expect, it, vi } from "vitest";
import type { SilentRequest } from "@azure/msal-browser";
import { acquireFreshIdToken, idTokenNeedsRefresh } from "./idToken";

const NOW_MS = Date.UTC(2026, 9, 24, 1, 0, 0);
const nowSec = NOW_MS / 1000;

function makeIdToken(claims: Record<string, unknown>): string {
  const encode = (v: unknown) =>
    btoa(JSON.stringify(v)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `${encode({ alg: "RS256", typ: "JWT" })}.${encode(claims)}.sig`;
}

describe("idTokenNeedsRefresh", () => {
  const cases: Array<{ name: string; token: string; want: boolean }> = [
    { name: "valid for an hour", token: makeIdToken({ exp: nowSec + 3600 }), want: false },
    { name: "already expired", token: makeIdToken({ exp: nowSec - 60 }), want: true },
    { name: "inside the 5 minute skew", token: makeIdToken({ exp: nowSec + 120 }), want: true },
    { name: "just outside the skew", token: makeIdToken({ exp: nowSec + 301 }), want: false },
    { name: "missing exp", token: makeIdToken({ aud: "x" }), want: true },
    { name: "malformed token", token: "not-a-jwt", want: true },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(idTokenNeedsRefresh(tc.token, NOW_MS)).toBe(tc.want);
    });
  }
});

describe("acquireFreshIdToken", () => {
  const request = { scopes: ["openid", "profile"] } as SilentRequest;
  const now = () => NOW_MS;

  it("returns the cached ID token when it is still valid", async () => {
    const valid = makeIdToken({ exp: nowSec + 3600 });
    const acquireTokenSilent = vi.fn().mockResolvedValue({ idToken: valid });

    await expect(acquireFreshIdToken({ acquireTokenSilent }, request, { now })).resolves.toBe(valid);
    expect(acquireTokenSilent).toHaveBeenCalledTimes(1);
    expect(acquireTokenSilent.mock.calls[0][0].forceRefresh).toBeFalsy();
  });

  // Regression for #8: MSAL serves a stale ID token from cache while its
  // (unused) access token is still valid; we must force a refresh.
  it("forces a silent refresh when MSAL returns an expired cached ID token", async () => {
    const expired = makeIdToken({ exp: nowSec - 30 });
    const fresh = makeIdToken({ exp: nowSec + 3600 });
    const acquireTokenSilent = vi
      .fn()
      .mockResolvedValueOnce({ idToken: expired })
      .mockResolvedValueOnce({ idToken: fresh });

    await expect(acquireFreshIdToken({ acquireTokenSilent }, request, { now })).resolves.toBe(fresh);
    expect(acquireTokenSilent).toHaveBeenCalledTimes(2);
    expect(acquireTokenSilent.mock.calls[1][0]).toMatchObject({ ...request, forceRefresh: true });
  });

  it("goes straight to a forced refresh when asked to", async () => {
    const fresh = makeIdToken({ exp: nowSec + 3600 });
    const acquireTokenSilent = vi.fn().mockResolvedValue({ idToken: fresh });

    await expect(
      acquireFreshIdToken({ acquireTokenSilent }, request, { now, forceRefresh: true }),
    ).resolves.toBe(fresh);
    expect(acquireTokenSilent).toHaveBeenCalledTimes(1);
    expect(acquireTokenSilent.mock.calls[0][0].forceRefresh).toBe(true);
  });

  it("rejects an empty ID token", async () => {
    const acquireTokenSilent = vi.fn().mockResolvedValue({ idToken: "" });

    await expect(acquireFreshIdToken({ acquireTokenSilent }, request, { now })).rejects.toThrow(
      "MSAL returned empty idToken",
    );
  });

  it("propagates silent refresh failures so the caller can redirect", async () => {
    const failure = new Error("interaction_required");
    const acquireTokenSilent = vi
      .fn()
      .mockResolvedValueOnce({ idToken: makeIdToken({ exp: nowSec - 30 }) })
      .mockRejectedValueOnce(failure);

    await expect(acquireFreshIdToken({ acquireTokenSilent }, request, { now })).rejects.toBe(failure);
  });
});

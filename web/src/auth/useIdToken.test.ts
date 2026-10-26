import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  BrowserAuthError,
  BrowserAuthErrorCodes,
  InteractionRequiredAuthError,
  type AccountInfo,
  type IPublicClientApplication,
} from "@azure/msal-browser";

// useIdToken keeps a module-level "redirect in flight" flag (the redirect
// navigates away in a browser), so each test loads a fresh module.
let acquireIdToken: typeof import("./useIdToken").acquireIdToken;

beforeEach(async () => {
  vi.resetModules();
  ({ acquireIdToken } = await import("./useIdToken"));
});

const account = { homeAccountId: "h", username: "user@example.com" } as AccountInfo;

function fakeInstance(silentError: unknown): {
  instance: IPublicClientApplication;
  acquireTokenRedirect: ReturnType<typeof vi.fn>;
} {
  const acquireTokenRedirect = vi.fn().mockResolvedValue(undefined);
  const instance = {
    getActiveAccount: () => account,
    acquireTokenSilent: vi.fn().mockRejectedValue(silentError),
    acquireTokenRedirect,
    loginRedirect: vi.fn().mockResolvedValue(undefined),
  } as unknown as IPublicClientApplication;
  return { instance, acquireTokenRedirect };
}

function stubSessionFetch(sessionValid: boolean): void {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url === "/auth/session" && sessionValid) {
        return new Response(JSON.stringify({ name: "user@example.com" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("{}", { status: 401, headers: { "Content-Type": "application/json" } });
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("acquireIdToken recovery", () => {
  const recoverable = [
    { name: "interaction required", error: new InteractionRequiredAuthError("login_required") },
    // msal-browser 5 throws timed_out when the hidden-iframe renewal gets no
    // answer (refresh token past its 24h SPA lifetime).
    { name: "silent iframe timed out", error: new BrowserAuthError(BrowserAuthErrorCodes.timedOut) },
  ];

  it.each(recoverable)("falls back to the session cookie on $name", async ({ error }) => {
    stubSessionFetch(true);
    const { instance, acquireTokenRedirect } = fakeInstance(error);

    await expect(acquireIdToken(instance, [account])).resolves.toBeNull();
    expect(acquireTokenRedirect).not.toHaveBeenCalled();
  });

  it.each(recoverable)("redirects to sign in on $name without a session", async ({ error }) => {
    stubSessionFetch(false);
    const { instance, acquireTokenRedirect } = fakeInstance(error);

    await expect(acquireIdToken(instance, [account])).rejects.toBe(error);
    expect(acquireTokenRedirect).toHaveBeenCalledTimes(1);
  });

  it("propagates unrelated errors", async () => {
    stubSessionFetch(true);
    const error = new Error("network down");
    const { instance, acquireTokenRedirect } = fakeInstance(error);

    await expect(acquireIdToken(instance, [account])).rejects.toBe(error);
    expect(acquireTokenRedirect).not.toHaveBeenCalled();
  });
});

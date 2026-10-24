import type { AuthenticationResult, SilentRequest } from "@azure/msal-browser";

// Refresh the ID token this long before it expires so requests in flight and
// small client/server clock differences don't hit the backend's expiry check.
export const ID_TOKEN_REFRESH_SKEW_SECONDS = 5 * 60;

export interface SilentTokenClient {
  acquireTokenSilent(request: SilentRequest): Promise<Pick<AuthenticationResult, "idToken">>;
}

export interface AcquireOptions {
  forceRefresh?: boolean;
  now?: () => number;
}

// idTokenNeedsRefresh reports whether a JWT is expired, about to expire, or
// unreadable. The signature is not checked; the backend does that.
export function idTokenNeedsRefresh(
  token: string,
  nowMs: number,
  skewSeconds = ID_TOKEN_REFRESH_SKEW_SECONDS,
): boolean {
  const exp = readExp(token);
  if (exp === null) {
    return true;
  }
  return exp - skewSeconds <= nowMs / 1000;
}

function readExp(token: string): number | null {
  const payload = token.split(".")[1];
  if (!payload) {
    return null;
  }
  try {
    const base64 = payload.replace(/-/g, "+").replace(/_/g, "/");
    const padded = base64.padEnd(base64.length + ((4 - (base64.length % 4)) % 4), "=");
    const claims = JSON.parse(atob(padded)) as { exp?: unknown };
    return typeof claims.exp === "number" ? claims.exp : null;
  } catch {
    return null;
  }
}

// acquireFreshIdToken returns an ID token that is valid for at least the
// refresh skew. MSAL's silent flow only checks the cached *access* token's
// expiry before serving from cache, so it can hand back an expired ID token;
// in that case we force a refresh-token redemption, which mints a new one.
export async function acquireFreshIdToken(
  client: SilentTokenClient,
  request: SilentRequest,
  { forceRefresh = false, now = Date.now }: AcquireOptions = {},
): Promise<string> {
  let result = await client.acquireTokenSilent({ ...request, forceRefresh });
  if (!forceRefresh && result.idToken && idTokenNeedsRefresh(result.idToken, now())) {
    result = await client.acquireTokenSilent({ ...request, forceRefresh: true });
  }
  if (!result.idToken) {
    throw new Error("MSAL returned empty idToken");
  }
  return result.idToken;
}

import { useCallback } from "react";
import { useMsal } from "@azure/msal-react";
import {
  BrowserAuthErrorCodes,
  InteractionRequiredAuthError,
  type AccountInfo,
  type IPublicClientApplication,
} from "@azure/msal-browser";
import { loginRequest } from "./msalConfig";
import { acquireFreshIdToken } from "./idToken";
import { interactiveRequest } from "./loginHint";
import { getServerSession, signInRequest } from "./serverSession";

export interface IdTokenOptions {
  forceRefresh?: boolean;
}

let redirectInFlight = false;

function isInteractionInProgress(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "errorCode" in error &&
    String(error.errorCode) === BrowserAuthErrorCodes.interactionInProgress
  );
}

// needsReauthentication reports whether a silent-renewal failure means MSAL
// can no longer mint tokens without the user. Besides InteractionRequired,
// that includes timed_out: once the refresh token passes its 24h SPA lifetime
// MSAL falls back to a hidden iframe, which can time out (e.g. when the
// browser blocks Entra's third-party cookies).
function needsReauthentication(error: unknown): boolean {
  if (error instanceof InteractionRequiredAuthError) return true;
  return (
    typeof error === "object" &&
    error !== null &&
    "errorCode" in error &&
    String(error.errorCode) === BrowserAuthErrorCodes.timedOut
  );
}

// redirectToSignIn starts an interactive sign-in carrying a server-login
// nonce, so the resulting ID token can be exchanged for a session cookie.
async function redirectToSignIn(
  instance: IPublicClientApplication,
  account: Parameters<IPublicClientApplication["acquireTokenRedirect"]>[0]["account"],
): Promise<void> {
  if (redirectInFlight) return;
  redirectInFlight = true;
  try {
    const request = await signInRequest(interactiveRequest(loginRequest));
    if (account) {
      await instance.acquireTokenRedirect({ ...request, account });
    } else {
      await instance.loginRedirect(request);
    }
  } catch (error) {
    if (!isInteractionInProgress(error)) {
      redirectInFlight = false;
    }
    throw error;
  }
}

// useIdToken returns an async getter that produces a fresh Microsoft Entra
// ID token for the signed-in account. The Go backend validates this token
// (audience = client ID) on /api/* and /events.
//
// It resolves to null when MSAL has no usable account but the server session
// cookie is valid (typically iOS after MSAL's cache was dropped); requests
// then authenticate with the cookie. Callers pass forceRefresh after the
// backend rejects a request with 401; if neither MSAL nor the session can
// recover, the user is redirected to sign in.
export function useIdToken(): (options?: IdTokenOptions) => Promise<string | null> {
  const { instance, accounts } = useMsal();

  return useCallback(
    (options?: IdTokenOptions) => acquireIdToken(instance, accounts, options),
    [instance, accounts],
  );
}

// acquireIdToken implements useIdToken's getter; exported for tests.
export async function acquireIdToken(
  instance: IPublicClientApplication,
  accounts: readonly AccountInfo[],
  options?: IdTokenOptions,
): Promise<string | null> {
  const account = accounts[0] ?? instance.getActiveAccount();
  if (!account) {
    if (options?.forceRefresh) {
      await redirectToSignIn(instance, undefined);
      throw new Error("no signed-in account");
    }
    return null;
  }

  try {
    // The ID token always has `aud = clientId` for tokens minted for this
    // app, so it's the right credential for backend validation.
    return await acquireFreshIdToken(
      instance,
      { ...loginRequest, account },
      { forceRefresh: options?.forceRefresh },
    );
  } catch (err) {
    if (!needsReauthentication(err)) {
      throw err;
    }
    // MSAL can no longer renew silently; fall back to the session cookie
    // before forcing an interactive sign-in.
    if (await getServerSession()) {
      return null;
    }
    await redirectToSignIn(instance, account);
    // acquireTokenRedirect navigates away; control rarely returns here.
    throw err;
  }
}

import { useCallback } from "react";
import { useMsal } from "@azure/msal-react";
import {
  BrowserAuthErrorCodes,
  InteractionRequiredAuthError,
} from "@azure/msal-browser";
import { loginRequest } from "./msalConfig";
import { acquireFreshIdToken } from "./idToken";
import { interactiveRequest } from "./loginHint";

export interface IdTokenOptions {
  forceRefresh?: boolean;
}

let redirectInFlight = false;

// useIdToken returns an async getter that produces a fresh Microsoft Entra
// ID token for the signed-in account. The Go backend validates this token
// (audience = client ID) on /api/* and /events.
//
// MSAL caches tokens but only refreshes based on the access token's expiry,
// so acquireFreshIdToken also checks the ID token's own expiry and forces a
// silent refresh-token redemption when needed. Callers pass forceRefresh
// after the backend rejects a token with 401. If silent refresh fails
// (refresh token expired, consent revoked, etc.), the caller is redirected
// to the login flow.
export function useIdToken(): (options?: IdTokenOptions) => Promise<string> {
  const { instance, accounts } = useMsal();

  return useCallback(async (options?: IdTokenOptions) => {
    const account = accounts[0] ?? instance.getActiveAccount();
    if (!account) {
      throw new Error("no signed-in account");
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
      if (err instanceof InteractionRequiredAuthError) {
        if (redirectInFlight) {
          throw err;
        }

        // Silent refresh failed; force a redirect to re-authenticate.
        redirectInFlight = true;
        try {
          await instance.acquireTokenRedirect({ ...interactiveRequest(loginRequest), account });
        } catch (redirectError) {
          const errorCode =
            typeof redirectError === "object" && redirectError !== null && "errorCode" in redirectError
              ? String(redirectError.errorCode)
              : "";
          if (errorCode !== BrowserAuthErrorCodes.interactionInProgress) {
            redirectInFlight = false;
          }
          throw redirectError;
        }
        // acquireTokenRedirect navigates away; this throw is for type
        // narrowing — control rarely returns here.
        throw err;
      }
      throw err;
    }
  }, [instance, accounts]);
}

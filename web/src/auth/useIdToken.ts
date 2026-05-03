import { useCallback } from "react";
import { useMsal } from "@azure/msal-react";
import {
  BrowserAuthErrorCodes,
  InteractionRequiredAuthError,
} from "@azure/msal-browser";
import { loginRequest } from "./msalConfig";

let redirectInFlight = false;

// useIdToken returns an async getter that produces a fresh Microsoft Entra
// ID token for the signed-in account. The Go backend validates this token
// (audience = client ID) on /api/* and /events.
//
// MSAL caches tokens and refreshes silently when expired. If the cache is
// missing or refresh fails (consent revoked, password changed, etc.), the
// caller is redirected to the login flow.
export function useIdToken(): () => Promise<string> {
  const { instance, accounts } = useMsal();

  return useCallback(async () => {
    const account = accounts[0] ?? instance.getActiveAccount();
    if (!account) {
      throw new Error("no signed-in account");
    }

    try {
      const result = await instance.acquireTokenSilent({
        ...loginRequest,
        account,
      });
      // The ID token always has `aud = clientId` for tokens minted for this
      // app, so it's the right credential for backend validation.
      if (!result.idToken) {
        throw new Error("MSAL returned empty idToken");
      }
      return result.idToken;
    } catch (err) {
      if (err instanceof InteractionRequiredAuthError) {
        if (redirectInFlight) {
          throw err;
        }

        // Silent refresh failed; force a redirect to re-authenticate.
        redirectInFlight = true;
        try {
          await instance.acquireTokenRedirect(loginRequest);
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

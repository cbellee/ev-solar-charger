import { useEffect, type ReactNode } from "react";
import {
  AuthenticatedTemplate,
  MsalProvider,
  UnauthenticatedTemplate,
  useMsal,
} from "@azure/msal-react";
import { EventType, InteractionStatus } from "@azure/msal-browser";
import type { AuthenticationResult, EventMessage } from "@azure/msal-browser";
import { loginRequest, msalInstance } from "./msalConfig";
import { setTokenGetter } from "@/api/client";
import { useIdToken } from "./useIdToken";

// AuthBootstrap registers the MSAL token getter with the API client and
// forces interactive login when there's no signed-in account. Renders the
// app only once an account is present.
function AuthBootstrap({ children }: { children: ReactNode }): JSX.Element {
  const { instance, accounts, inProgress } = useMsal();
  const getIdToken = useIdToken();

  useEffect(() => {
    setTokenGetter(getIdToken);

    return () => {
      setTokenGetter(async () => null);
    };
  }, [getIdToken]);

  useEffect(() => {
    if (accounts.length > 0 && !instance.getActiveAccount()) {
      instance.setActiveAccount(accounts[0]);
    }
  }, [accounts, instance]);

  useEffect(() => {
    if (
      accounts.length === 0 &&
      inProgress === InteractionStatus.None
    ) {
      void instance.loginRedirect(loginRequest);
    }
  }, [accounts.length, inProgress, instance]);

  return (
    <>
      <AuthenticatedTemplate>{children}</AuthenticatedTemplate>
      <UnauthenticatedTemplate>
        <div className="min-h-screen flex items-center justify-center text-slate-300">
          Signing in&hellip;
        </div>
      </UnauthenticatedTemplate>
    </>
  );
}

// AuthProvider wraps the app in MsalProvider and handles the redirect promise
// so the active account is set before children render.
export function AuthProvider({ children }: { children: ReactNode }): JSX.Element {
  useEffect(() => {
    const callbackId = msalInstance.addEventCallback((msg: EventMessage) => {
      if (msg.eventType === EventType.LOGIN_SUCCESS && msg.payload) {
        const result = msg.payload as AuthenticationResult;
        if (result.account) {
          msalInstance.setActiveAccount(result.account);
        }
      }
    });

    void msalInstance.initialize().then(() => msalInstance.handleRedirectPromise());

    return () => {
      if (callbackId) {
        msalInstance.removeEventCallback(callbackId);
      }
    };
  }, []);

  return (
    <MsalProvider instance={msalInstance}>
      <AuthBootstrap>{children}</AuthBootstrap>
    </MsalProvider>
  );
}

export function SignOutButton(): JSX.Element {
  const { instance } = useMsal();
  return (
    <button
      type="button"
      className="text-sm text-slate-300 hover:text-white"
      onClick={() => {
        void instance.logoutRedirect();
      }}
    >
      Sign out
    </button>
  );
}

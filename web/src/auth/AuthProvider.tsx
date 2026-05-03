import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import {
  AuthenticatedTemplate,
  MsalProvider,
  UnauthenticatedTemplate,
  useMsal,
} from "@azure/msal-react";
import {
  BrowserAuthErrorCodes,
  EventType,
  InteractionStatus,
  PublicClientApplication,
} from "@azure/msal-browser";
import type { AuthenticationResult, EventMessage } from "@azure/msal-browser";
import { createMsalInstance, loginRequest } from "./msalConfig";
import { setTokenGetter } from "@/api/client";
import { useIdToken } from "./useIdToken";

// AuthBootstrap registers the MSAL token getter with the API client and
// forces interactive login when there's no signed-in account. Renders the
// app only once an account is present.
function AuthBootstrap({ children }: { children: ReactNode }): JSX.Element {
  const { instance, accounts, inProgress } = useMsal();
  const getIdToken = useIdToken();
  const [ready, setReady] = useState(false);
  const [startupError, setStartupError] = useState<string | null>(null);
  const loginRedirectStarted = useRef(false);

  useLayoutEffect(() => {
    setTokenGetter(getIdToken);

    return () => {
      setTokenGetter(async () => null);
    };
  }, [getIdToken]);

  useEffect(() => {
    if (accounts.length > 0 && !instance.getActiveAccount()) {
      instance.setActiveAccount(accounts[0]);
    }
    if (accounts.length > 0) {
      loginRedirectStarted.current = false;
    }
  }, [accounts, instance]);

  useEffect(() => {
    let disposed = false;

    async function initialize() {
      try {
        await instance.initialize();
        const result = await instance.handleRedirectPromise();
        if (disposed) {
          return;
        }
        if (result?.account) {
          instance.setActiveAccount(result.account);
        } else if (!instance.getActiveAccount()) {
          const [account] = instance.getAllAccounts();
          if (account) {
            instance.setActiveAccount(account);
          }
        }
        setReady(true);
      } catch (error) {
        if (disposed) {
          return;
        }
        const message = error instanceof Error ? error.message : "Authentication initialization failed.";
        setStartupError(message);
      }
    }

    void initialize();

    return () => {
      disposed = true;
    };
  }, [instance]);

  useEffect(() => {
    if (ready && accounts.length === 0 && inProgress === InteractionStatus.None) {
      if (loginRedirectStarted.current) {
        return;
      }
      loginRedirectStarted.current = true;
      void instance.loginRedirect(loginRequest).catch((error: unknown) => {
        const errorCode =
          typeof error === "object" && error !== null && "errorCode" in error
            ? String(error.errorCode)
            : "";
        if (errorCode === BrowserAuthErrorCodes.interactionInProgress) {
          return;
        }
        loginRedirectStarted.current = false;
        const message = error instanceof Error ? error.message : "Sign-in redirect failed.";
        setStartupError(message);
      });
    }
  }, [accounts.length, inProgress, instance, ready]);

  if (startupError) {
    return <AuthConfigError message={startupError} />;
  }

  if (!ready) {
    return (
      <div className="min-h-screen flex items-center justify-center text-slate-300">
        Loading authentication...
      </div>
    );
  }

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

function AuthConfigError({ message }: { message: string }): JSX.Element {
  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-950 px-6 text-gray-100">
      <div className="max-w-xl rounded-2xl border border-red-900/50 bg-red-950/30 p-6 shadow-xl">
        <h1 className="text-xl font-semibold text-white">Authentication unavailable</h1>
        <p className="mt-3 text-sm leading-6 text-red-100">{message}</p>
        <p className="mt-3 text-sm leading-6 text-red-200/90">
          For embedded deployments, ensure the server is started with
          ENTRA_TENANT_ID and ENTRA_CLIENT_ID. For local Vite development,
          set VITE_ENTRA_TENANT_ID and VITE_ENTRA_CLIENT_ID.
        </p>
      </div>
    </div>
  );
}

// AuthProvider wraps the app in MsalProvider and handles the redirect promise
// so the active account is set before children render.
export function AuthProvider({ children }: { children: ReactNode }): JSX.Element {
  const [msalInstance, setMsalInstance] = useState<PublicClientApplication | null>(null);
  const [authError, setAuthError] = useState<string | null>(null);

  useEffect(() => {
    let disposed = false;
    let callbackId: string | null = null;
    let instance: PublicClientApplication | null = null;

    async function initialize() {
      const resolved = await createMsalInstance();
      if (disposed) {
        return;
      }
      if (!resolved.instance) {
        setAuthError(resolved.error ?? "Authentication initialization failed.");
        return;
      }

      instance = resolved.instance;
      callbackId = instance.addEventCallback((msg: EventMessage) => {
        if (msg.eventType === EventType.LOGIN_SUCCESS && msg.payload) {
          const result = msg.payload as AuthenticationResult;
          if (result.account) {
            instance?.setActiveAccount(result.account);
          }
        }
      });

      if (!disposed) {
        setMsalInstance(instance);
      }
    }

    void initialize();

    return () => {
      disposed = true;
      if (callbackId && instance) {
        instance.removeEventCallback(callbackId);
      }
    };
  }, []);

  if (authError) {
    return <AuthConfigError message={authError} />;
  }

  if (!msalInstance) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-gray-950 text-slate-300">
        Loading authentication...
      </div>
    );
  }

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

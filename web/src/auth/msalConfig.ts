import {
  Configuration,
  LogLevel,
  PublicClientApplication,
} from "@azure/msal-browser";

// Vite injects VITE_-prefixed env vars at build time.
const tenantId = import.meta.env.VITE_ENTRA_TENANT_ID;
const clientId = import.meta.env.VITE_ENTRA_CLIENT_ID;

if (!tenantId || !clientId) {
  // Fail loudly in dev/prod rather than silently producing broken auth.
  // eslint-disable-next-line no-console
  console.error(
    "MSAL config missing: set VITE_ENTRA_TENANT_ID and VITE_ENTRA_CLIENT_ID",
  );
}

export const msalConfig: Configuration = {
  auth: {
    clientId: clientId ?? "",
    authority: `https://login.microsoftonline.com/${tenantId ?? "common"}`,
    redirectUri: window.location.origin,
    postLogoutRedirectUri: window.location.origin,
  },
  cache: {
    // sessionStorage clears on tab close; safer than localStorage for an
    // admin app while still surviving full-page reloads within the tab.
    cacheLocation: "sessionStorage",
  },
  system: {
    loggerOptions: {
      logLevel: LogLevel.Warning,
      piiLoggingEnabled: false,
      loggerCallback: (level, message) => {
        if (level === LogLevel.Error) {
          // eslint-disable-next-line no-console
          console.error("[msal]", message);
        }
      },
    },
  },
};

// Scopes used during interactive login. We only need the ID token (which
// always carries `aud=clientId` when minted for this app) to authenticate
// against the Go backend; openid + profile are sufficient.
export const loginRequest = {
  scopes: ["openid", "profile"],
};

// Singleton instance shared by the React tree via MsalProvider.
export const msalInstance = new PublicClientApplication(msalConfig);

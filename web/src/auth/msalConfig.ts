import {
  Configuration,
  LogLevel,
  PublicClientApplication,
} from "@azure/msal-browser";

export type EntraAuthConfig = {
  tenantId: string;
  clientId: string;
};

const invalidClientID = "00000000-0000-0000-0000-000000000000";

const viteFallbackConfig: EntraAuthConfig = {
  tenantId: import.meta.env.VITE_ENTRA_TENANT_ID ?? "",
  clientId: import.meta.env.VITE_ENTRA_CLIENT_ID ?? "",
};

function normalizeAuthConfig(candidate: Partial<EntraAuthConfig>): EntraAuthConfig {
  return {
    tenantId: (candidate.tenantId ?? "").trim(),
    clientId: (candidate.clientId ?? "").trim(),
  };
}

function validateAuthConfig(config: EntraAuthConfig): string | null {
  if (!config.tenantId) {
    return "Authentication is not configured: missing Entra tenant ID.";
  }
  if (!config.clientId || config.clientId === invalidClientID) {
    return "Authentication is not configured: missing Entra client ID.";
  }
  return null;
}

function buildMsalConfig(config: EntraAuthConfig): Configuration {
  return {
    auth: {
      clientId: config.clientId,
      authority: `https://login.microsoftonline.com/${config.tenantId}`,
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
}

async function loadRuntimeAuthConfig(): Promise<EntraAuthConfig | null> {
  const response = await fetch("/auth/entra/config", {
    headers: { Accept: "application/json" },
  });
  const contentType = response.headers.get("content-type") ?? "";
  if (!response.ok || !contentType.includes("application/json")) {
    return null;
  }
  const payload = (await response.json()) as Partial<EntraAuthConfig>;
  return normalizeAuthConfig(payload);
}

export async function createMsalInstance(): Promise<{
  instance: PublicClientApplication | null;
  error: string | null;
}> {
  let resolvedConfig = normalizeAuthConfig(viteFallbackConfig);

  try {
    const runtimeConfig = await loadRuntimeAuthConfig();
    if (runtimeConfig) {
      resolvedConfig = runtimeConfig;
    }
  } catch {
    // Local Vite dev does not serve the Go runtime endpoint; fall back to
    // compile-time Vite env vars in that case.
  }

  const error = validateAuthConfig(resolvedConfig);
  if (error) {
    // eslint-disable-next-line no-console
    console.error("[msal]", error);
    return { instance: null, error };
  }

  return {
    instance: new PublicClientApplication(buildMsalConfig(resolvedConfig)),
    error: null,
  };
}

// Scopes used during interactive login. We only need the ID token (which
// always carries `aud=clientId` when minted for this app) to authenticate
// against the Go backend; openid + profile are sufficient.
export const loginRequest = {
  scopes: ["openid", "profile"],
};

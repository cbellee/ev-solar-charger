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

export function buildMsalConfig(
  config: EntraAuthConfig,
  origin: string = window.location.origin,
): Configuration {
  return {
    auth: {
      clientId: config.clientId,
      authority: `https://login.microsoftonline.com/${config.tenantId}`,
      redirectUri: origin,
      postLogoutRedirectUri: origin,
    },
    cache: {
      // localStorage shares the session across tabs and survives mobile tab
      // discards; sessionStorage forced a new sign-in on almost every visit.
      // msal-browser encrypts these tokens with a key held in a
      // browser-session cookie, so they are unusable after the browser
      // session ends.
      cacheLocation: "localStorage",
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

  const instance = new PublicClientApplication(buildMsalConfig(resolvedConfig));
  try {
    // The localStorage cache already holds account keys after a refresh, and
    // reading them before initialize() completes throws
    // uninitialized_public_client_application. Initialize before handing the
    // instance to MsalProvider.
    await instance.initialize();
  } catch (err) {
    const message = err instanceof Error ? err.message : "Authentication initialization failed.";
    // eslint-disable-next-line no-console
    console.error("[msal]", message);
    return { instance: null, error: message };
  }

  return { instance, error: null };
}

// Scopes used during interactive login. We only need the ID token (which
// always carries `aud=clientId` when minted for this app) to authenticate
// against the Go backend; openid + profile are sufficient.
export const loginRequest = {
  scopes: ["openid", "profile"],
};

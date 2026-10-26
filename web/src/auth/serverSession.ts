// Client for the server-side session cookie (/auth/session). iOS clears the
// session cookie holding MSAL's cache-encryption key whenever Safari or the
// home-screen app is terminated, which wipes the MSAL sign-in. The server
// session cookie (HttpOnly, 24h sliding idle timeout) keeps the dashboard
// signed in across that.
//
// A session can only be created from an ID token whose nonce came from
// startServerLogin, so the nonce must be fetched before every interactive
// sign-in redirect.

export const CSRF_HEADER = "X-Requested-With";
const CSRF_VALUE = "XMLHttpRequest";

export interface ServerSessionUser {
  name: string;
}

function sessionInit(method: string, headers?: Record<string, string>): RequestInit {
  return {
    method,
    credentials: "same-origin",
    cache: "no-store",
    headers: { [CSRF_HEADER]: CSRF_VALUE, ...headers },
  };
}

// startServerLogin begins a login transaction and returns its nonce, or
// undefined if sessions are unavailable (e.g. local dev without the server).
export async function startServerLogin(): Promise<string | undefined> {
  try {
    const res = await fetch("/auth/session/login", sessionInit("POST"));
    if (!res.ok) return undefined;
    const body = (await res.json()) as { nonce?: unknown };
    return typeof body.nonce === "string" && body.nonce !== "" ? body.nonce : undefined;
  } catch {
    return undefined;
  }
}

// signInRequest adds a fresh server-login nonce to an interactive request.
export async function signInRequest<T extends object>(request: T): Promise<T & { nonce?: string }> {
  const nonce = await startServerLogin();
  return nonce ? { ...request, nonce } : request;
}

// createServerSession exchanges the ID token from a just-completed
// interactive sign-in for a session cookie.
export async function createServerSession(idToken: string): Promise<boolean> {
  try {
    const res = await fetch(
      "/auth/session",
      sessionInit("POST", { Authorization: `Bearer ${idToken}` }),
    );
    return res.ok;
  } catch {
    return false;
  }
}

// getServerSession returns the signed-in user if the session cookie is valid.
export async function getServerSession(): Promise<ServerSessionUser | null> {
  try {
    const res = await fetch("/auth/session", sessionInit("GET"));
    if (!res.ok) return null;
    const body = (await res.json()) as { name?: unknown };
    return { name: typeof body.name === "string" ? body.name : "" };
  } catch {
    return null;
  }
}

// endServerSession revokes all of the user's server sessions (every device).
// The ID token, if available, identifies the user when no cookie is present.
export async function endServerSession(idToken?: string | null): Promise<void> {
  try {
    await fetch(
      "/auth/session",
      sessionInit("DELETE", idToken ? { Authorization: `Bearer ${idToken}` } : undefined),
    );
  } catch {
    // Signing out of Entra still proceeds; the session expires on its own.
  }
}

// Remembers the signed-in username (never tokens) so sign-in redirects can
// pass it to Entra as `loginHint`. Entra then skips the account picker and,
// if its own session cookie is still valid, returns without prompting.

export const LOGIN_HINT_KEY = "solar-ev.loginHint";

function defaultStorage(): Storage | undefined {
  try {
    return globalThis.localStorage;
  } catch {
    return undefined;
  }
}

export function rememberLoginHint(
  account: { username?: string } | null | undefined,
  storage: Storage | undefined = defaultStorage(),
): void {
  const username = account?.username?.trim();
  if (!username || !storage) return;
  try {
    storage.setItem(LOGIN_HINT_KEY, username);
  } catch {
    // Storage full or blocked; the hint is only a convenience.
  }
}

export function readLoginHint(storage: Storage | undefined = defaultStorage()): string | undefined {
  try {
    return storage?.getItem(LOGIN_HINT_KEY) || undefined;
  } catch {
    return undefined;
  }
}

export function clearLoginHint(storage: Storage | undefined = defaultStorage()): void {
  try {
    storage?.removeItem(LOGIN_HINT_KEY);
  } catch {
    // Nothing to clear if storage is unavailable.
  }
}

export function interactiveRequest<T extends object>(
  request: T,
  storage: Storage | undefined = defaultStorage(),
): T & { loginHint?: string } {
  const loginHint = readLoginHint(storage);
  return loginHint ? { ...request, loginHint } : request;
}

import { describe, expect, it } from "vitest";
import {
  LOGIN_HINT_KEY,
  clearLoginHint,
  interactiveRequest,
  readLoginHint,
  rememberLoginHint,
} from "./loginHint";
import { buildMsalConfig } from "./msalConfig";

function memoryStorage(initial: Record<string, string> = {}): Storage {
  const data = new Map(Object.entries(initial));
  return {
    get length() {
      return data.size;
    },
    clear: () => data.clear(),
    getItem: (k) => data.get(k) ?? null,
    key: (i) => [...data.keys()][i] ?? null,
    removeItem: (k) => void data.delete(k),
    setItem: (k, v) => void data.set(k, String(v)),
  };
}

function throwingStorage(): Storage {
  const fail = () => {
    throw new Error("storage disabled");
  };
  return { length: 0, clear: fail, getItem: fail, key: fail, removeItem: fail, setItem: fail };
}

const base = { scopes: ["openid", "profile"] };

describe("login hint", () => {
  it("remembers the account username and reads it back", () => {
    const s = memoryStorage();
    rememberLoginHint({ username: "chris@example.com" }, s);
    expect(s.getItem(LOGIN_HINT_KEY)).toBe("chris@example.com");
    expect(readLoginHint(s)).toBe("chris@example.com");
  });

  it("ignores missing accounts and blank usernames", () => {
    const s = memoryStorage({ [LOGIN_HINT_KEY]: "kept@example.com" });
    rememberLoginHint(null, s);
    rememberLoginHint({ username: "  " }, s);
    expect(readLoginHint(s)).toBe("kept@example.com");
  });

  it("clears the hint on sign-out", () => {
    const s = memoryStorage({ [LOGIN_HINT_KEY]: "chris@example.com" });
    clearLoginHint(s);
    expect(readLoginHint(s)).toBeUndefined();
  });

  it("adds loginHint to interactive requests when remembered", () => {
    const s = memoryStorage({ [LOGIN_HINT_KEY]: "chris@example.com" });
    expect(interactiveRequest(base, s)).toEqual({ ...base, loginHint: "chris@example.com" });
  });

  it("leaves the request unchanged when nothing is remembered", () => {
    expect(interactiveRequest(base, memoryStorage())).toEqual(base);
  });

  it("does not throw when storage is unavailable", () => {
    const s = throwingStorage();
    expect(() => rememberLoginHint({ username: "chris@example.com" }, s)).not.toThrow();
    expect(() => clearLoginHint(s)).not.toThrow();
    expect(readLoginHint(s)).toBeUndefined();
    expect(interactiveRequest(base, s)).toEqual(base);
  });
});

describe("buildMsalConfig", () => {
  it("shares the MSAL cache across tabs and reloads via localStorage", () => {
    const cfg = buildMsalConfig({ tenantId: "t", clientId: "c" }, "https://ev.example");
    expect(cfg.cache?.cacheLocation).toBe("localStorage");
    expect(cfg.auth.redirectUri).toBe("https://ev.example");
  });
});

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  CSRF_HEADER,
  createServerSession,
  endServerSession,
  getServerSession,
  signInRequest,
  startServerLogin,
} from "./serverSession";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function call(fetchMock: ReturnType<typeof vi.fn>, i = 0): { url: string; init: RequestInit; headers: Headers } {
  const [url, init] = fetchMock.mock.calls[i] as [string, RequestInit];
  return { url, init, headers: new Headers(init.headers) };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("startServerLogin", () => {
  it("returns the nonce and sends the CSRF header", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { nonce: "n1" }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(startServerLogin()).resolves.toBe("n1");
    const { url, init, headers } = call(fetchMock);
    expect(url).toBe("/auth/session/login");
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("same-origin");
    expect(headers.get(CSRF_HEADER)).toBe("XMLHttpRequest");
  });

  it.each([
    ["404", () => Promise.resolve(new Response(null, { status: 404 }))],
    ["network error", () => Promise.reject(new TypeError("offline"))],
    ["missing nonce", () => Promise.resolve(jsonResponse(200, {}))],
  ])("returns undefined on %s", async (_name, impl) => {
    vi.stubGlobal("fetch", vi.fn(impl));
    await expect(startServerLogin()).resolves.toBeUndefined();
  });
});

describe("signInRequest", () => {
  it("adds the nonce when available", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(200, { nonce: "n2" })));
    await expect(signInRequest({ scopes: ["openid"] })).resolves.toEqual({ scopes: ["openid"], nonce: "n2" });
  });

  it("leaves the request unchanged when sessions are unavailable", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 404 })));
    await expect(signInRequest({ scopes: ["openid"] })).resolves.toEqual({ scopes: ["openid"] });
  });
});

describe("createServerSession", () => {
  it("posts the ID token as a bearer", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(createServerSession("id-tok")).resolves.toBe(true);
    const { url, init, headers } = call(fetchMock);
    expect(url).toBe("/auth/session");
    expect(init.method).toBe("POST");
    expect(headers.get("Authorization")).toBe(["Bearer", "id-tok"].join(" "));
    expect(headers.get(CSRF_HEADER)).toBe("XMLHttpRequest");
  });

  it("reports rejection", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(401, { error: "login_not_verified" })));
    await expect(createServerSession("id-tok")).resolves.toBe(false);
  });
});

describe("getServerSession", () => {
  it("returns the user for a valid cookie", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { name: "user@example.com" }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(getServerSession()).resolves.toEqual({ name: "user@example.com" });
    expect(call(fetchMock).init.method).toBe("GET");
  });

  it("returns null when there is no session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(401, { error: "missing_token" })));
    await expect(getServerSession()).resolves.toBeNull();
  });
});

describe("endServerSession", () => {
  it("sends DELETE with the ID token when available", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    await endServerSession("id-tok");
    const { init, headers } = call(fetchMock);
    expect(init.method).toBe("DELETE");
    expect(headers.get("Authorization")).toBe(["Bearer", "id-tok"].join(" "));
    expect(headers.get(CSRF_HEADER)).toBe("XMLHttpRequest");
  });

  it("omits Authorization and swallows network errors", async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("offline"));
    vi.stubGlobal("fetch", fetchMock);
    await expect(endServerSession(null)).resolves.toBeUndefined();
    expect(call(fetchMock).headers.has("Authorization")).toBe(false);
  });
});

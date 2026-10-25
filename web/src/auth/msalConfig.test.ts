// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMsalInstance } from "./msalConfig";

describe("createMsalInstance", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    window.localStorage.clear();
  });

  // Regression for #16: with the localStorage cache, a page refresh starts
  // with account keys from the previous load. Any account lookup on an
  // uninitialized instance then throws uninitialized_public_client_application,
  // so the instance must be initialized before MsalProvider renders.
  it("returns an initialized instance when accounts are already cached", async () => {
    window.localStorage.setItem("msal.3.account.keys", JSON.stringify(["cached-account"]));
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            tenantId: "11111111-1111-1111-1111-111111111111",
            clientId: "22222222-2222-2222-2222-222222222222",
          }),
          { headers: { "content-type": "application/json" } },
        ),
      ),
    );

    const { instance, error } = await createMsalInstance();

    expect(error).toBeNull();
    expect(instance).not.toBeNull();
    expect(() => instance?.getActiveAccount()).not.toThrow();
    expect(() => instance?.getAllAccounts()).not.toThrow();
  });
});

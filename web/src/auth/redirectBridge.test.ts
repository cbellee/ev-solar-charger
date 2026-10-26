import { describe, expect, it } from "vitest";
import { isAuthResponseInChildWindow } from "./redirectBridge";

type Win = Parameters<typeof isAuthResponseInChildWindow>[0];

function win({ framed = false, opener = false, hash = "", search = "" }): Win {
  const w = { location: { hash, search }, opener: opener ? {} : null } as unknown as { parent: unknown };
  w.parent = framed ? {} : w;
  return w as unknown as Win;
}

describe("isAuthResponseInChildWindow", () => {
  const cases = [
    { name: "hidden iframe with code", w: win({ framed: true, hash: "#code=abc&state=xyz" }), want: true },
    { name: "hidden iframe with error", w: win({ framed: true, hash: "#error=login_required&state=xyz" }), want: true },
    { name: "popup with code in query", w: win({ opener: true, search: "?code=abc&state=xyz" }), want: true },
    { name: "top-level redirect return", w: win({ hash: "#code=abc&state=xyz" }), want: false },
    { name: "iframe without response", w: win({ framed: true, hash: "#/history" }), want: false },
    { name: "iframe with state only", w: win({ framed: true, hash: "#state=xyz" }), want: false },
  ];
  it.each(cases)("$name", ({ w, want }) => {
    expect(isAuthResponseInChildWindow(w)).toBe(want);
  });
});

// PROTOTYPE (throwaway): five mobile dashboard layouts, switchable via
// ?variant=. Question: "How should the dashboard look on a phone so icons and
// key numbers are easy to read?" Uses static sample data; no auth or backend.
import { StrictMode, useEffect, useState, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import clsx from "clsx";
import type { StateSnapshot } from "@/api/types";
import { PowerFlowDiagram } from "@/components/PowerFlowDiagram";
import { STATE_BG, formatDurationHours, formatKW } from "@/lib/format";
import "../index.css";

const snap: StateSnapshot = {
  state: "charging",
  mode: "auto",
  testMode: false,
  pvWatts: 7560,
  gridWatts: 130,
  loadWatts: 7690,
  surplusWatts: 0,
  targetAmps: 26,
  actualAmps: 26,
  batteryPct: 64,
  timeToLimitHours: 2.4,
  chargeLimit: 80,
  chargeLimitMin: 50,
  chargeLimitMax: 100,
  carPluggedIn: true,
  carOnline: true,
  chargingState: "Charging",
  consecutiveLow: 0,
  consecutiveSurplus: 0,
  lastUpdate: new Date(Date.now() - 12_000).toISOString(),
  lastError: "",
};

const IMG = {
  solar: "/images/solar_panels.png",
  house: "/images/house.png",
  grid: "/images/electricity_pylon.png",
  ev: "/images/tesla_ev.png",
};

const importing = snap.gridWatts > 50;
const gridLabel = importing ? "Importing" : snap.gridWatts < -50 ? "Exporting" : "Idle";
const kw = (w: number) => (Math.abs(w) / 1000).toFixed(2);

// ---------- shared bits (header/nav are deliberately per-variant) ----------

function OldHeader() {
  return (
    <header className="border-b border-gray-800 bg-gray-950/60 px-4 py-3">
      <div className="flex items-center gap-3">
        <span className="text-xl">⚡</span>
        <h1 className="text-lg font-semibold">Solar EV Charger</h1>
      </div>
      <nav className="mt-2 flex gap-1 overflow-x-auto text-sm">
        {["Dashboard", "History", "Sessions", "Events", "Usage"].map((l, i) => (
          <span key={l} className={clsx("rounded px-3 py-1.5", i === 0 ? "bg-gray-800 text-white" : "text-gray-400")}>
            {l}
          </span>
        ))}
      </nav>
    </header>
  );
}

function Panel({ title, children, className }: { title?: string; children: ReactNode; className?: string }) {
  return (
    <section className={clsx("rounded-2xl border border-gray-800 bg-gray-800/40 p-4", className)}>
      {title && <h2 className="mb-3 text-base font-semibold text-gray-200">{title}</h2>}
      {children}
    </section>
  );
}

function BigButtons() {
  return (
    <div className="grid grid-cols-2 gap-3">
      <button className="h-14 rounded-xl bg-green-600 text-lg font-semibold">Auto</button>
      <button className="h-14 rounded-xl bg-gray-700 text-lg font-semibold text-gray-300">Manual</button>
      <button className="col-span-2 h-14 rounded-xl border border-red-500/50 bg-red-500/10 text-lg font-semibold text-red-300">
        Stop charging
      </button>
    </div>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex justify-between">
      <span className="text-gray-400">{k}</span>
      <span>{v}</span>
    </div>
  );
}

// ---------- 0: current layout for comparison ----------

function Current() {
  return (
    <>
      <OldHeader />
      <main className="space-y-6 p-4">
        <Panel title="System status">
          <PowerFlowDiagram snap={snap} />
          <div className="mt-4 grid grid-cols-2 gap-3 text-xs">
            <div>
              <div className="text-gray-500">State</div>
              <span className={clsx("rounded px-2 py-0.5 uppercase", STATE_BG[snap.state])}>{snap.state}</span>
            </div>
            <div>
              <div className="text-gray-500">Mode</div>
              {snap.mode}
            </div>
          </div>
        </Panel>
        <Panel title="Solar Production">
          <div className="space-y-1.5 text-sm">
            <Row k="PV" v={formatKW(snap.pvWatts)} />
            <Row k="Load" v={formatKW(snap.loadWatts)} />
            <Row k="Grid" v={formatKW(snap.gridWatts)} />
          </div>
        </Panel>
      </main>
    </>
  );
}

// ---------- 1: big summary tiles ----------

function Tile({ img, label, value, unit, tone, badge }: { img: string; label: string; value: string; unit: string; tone: string; badge?: string }) {
  return (
    <div className={clsx("rounded-2xl border-2 bg-gray-800/60 p-4", tone)}>
      <div className="flex items-center justify-between">
        <img src={img} alt="" className="h-12 w-12 object-contain" />
        {badge && <span className="rounded-full bg-white/10 px-2 py-0.5 text-xs font-semibold">{badge}</span>}
      </div>
      <div className="mt-3 text-sm font-medium text-gray-400">{label}</div>
      <div className="text-3xl font-bold tabular-nums text-white">
        {value}
        <span className="ml-1 text-lg font-medium text-gray-400">{unit}</span>
      </div>
    </div>
  );
}

function VariantTiles() {
  return (
    <>
      <OldHeader />
      <main className="space-y-4 p-4">
        <div className="grid grid-cols-2 gap-3">
          <Tile img={IMG.solar} label="Solar" value={kw(snap.pvWatts)} unit="kW" tone="border-yellow-400/60 text-yellow-300" />
          <Tile img={IMG.house} label="House" value={kw(snap.loadWatts)} unit="kW" tone="border-violet-400/60 text-violet-300" />
          <Tile img={IMG.grid} label="Grid" value={kw(snap.gridWatts)} unit="kW" tone={importing ? "border-red-500/60 text-red-300" : "border-emerald-400/60 text-emerald-300"} badge={gridLabel} />
          <Tile img={IMG.ev} label="EV" value={String(snap.actualAmps)} unit="A" tone="border-blue-400/60 text-blue-300" badge="Charging" />
        </div>
        <Panel title="Power flow">
          <PowerFlowDiagram snap={snap} />
        </Panel>
      </main>
    </>
  );
}

// ---------- 2: portrait power-flow diagram ----------

function FlowNode({ img, label, value, ring, size = "h-28 w-28" }: { img: string; label: string; value: string; ring: string; size?: string }) {
  return (
    <div className="flex flex-col items-center">
      <div className={clsx("flex items-center justify-center rounded-full border-4 bg-gray-950", ring, size)}>
        <img src={img} alt="" className="h-3/4 w-3/4 object-contain" />
      </div>
      <div className="mt-2 text-base font-semibold">{label}</div>
      <div className="text-xl font-bold tabular-nums">{value}</div>
    </div>
  );
}

function Flow({ color }: { color: string }) {
  return (
    <div className="flex justify-center py-1">
      <div className={clsx("h-10 w-1.5 animate-pulse rounded-full", color)} />
    </div>
  );
}

function VariantPortraitFlow() {
  return (
    <>
      <OldHeader />
      <main className="p-4">
        <Panel>
          <div className="flex items-start justify-between px-2">
            <FlowNode img={IMG.solar} label="Solar" value={`${kw(snap.pvWatts)} kW`} ring="border-yellow-400" />
            <FlowNode img={IMG.grid} label={`Grid · ${gridLabel}`} value={`${kw(snap.gridWatts)} kW`} ring={importing ? "border-red-500" : "border-emerald-400"} />
          </div>
          <div className="flex justify-around">
            <Flow color="bg-yellow-400 -rotate-[35deg]" />
            <Flow color={clsx(importing ? "bg-red-500" : "bg-emerald-400", "rotate-[35deg]")} />
          </div>
          <div className="flex justify-center">
            <FlowNode img={IMG.house} label="House" value={`${kw(snap.loadWatts)} kW`} ring="border-violet-400" size="h-32 w-32" />
          </div>
          <Flow color="bg-blue-500" />
          <div className="flex justify-center">
            <FlowNode img={IMG.ev} label="Tesla" value={`${snap.actualAmps} A · ${snap.batteryPct}%`} ring="border-blue-400" />
          </div>
        </Panel>
      </main>
    </>
  );
}

// ---------- 3: sticky status bar + EV hero ----------

function VariantStickyHero() {
  return (
    <>
      <OldHeader />
      <div className="sticky top-0 z-10 flex items-center justify-between bg-green-600 px-4 py-3 text-base font-semibold text-white shadow-lg">
        <span>⚡ Charging · {snap.actualAmps} A</span>
        <span className={clsx("rounded px-2", importing && "bg-red-600")}>
          {importing ? `Import ${kw(snap.gridWatts)} kW` : `Surplus ${kw(snap.surplusWatts)} kW`}
        </span>
      </div>
      <main className="space-y-4 p-4">
        <Panel>
          <div className="flex items-center gap-4">
            <img src={IMG.ev} alt="" className="h-20 w-28 object-contain" />
            <div>
              <div className="text-5xl font-bold tabular-nums">{snap.batteryPct}%</div>
              <div className="text-base text-gray-400">
                to {snap.chargeLimit}% in {formatDurationHours(snap.timeToLimitHours)}
              </div>
            </div>
          </div>
          <div className="relative mt-4 h-4 overflow-hidden rounded-full bg-gray-700">
            <div className="h-full bg-green-500" style={{ width: `${snap.batteryPct}%` }} />
            <div className="absolute inset-y-0 w-0.5 bg-white" style={{ left: `${snap.chargeLimit}%` }} />
          </div>
          <div className="mt-1 flex justify-between text-sm text-gray-400">
            <span>0%</span>
            <span>limit {snap.chargeLimit}%</span>
          </div>
        </Panel>
        <Panel title="Power now">
          <div className="space-y-3 text-lg">
            {[
              [IMG.solar, "Solar", `${kw(snap.pvWatts)} kW`, "text-yellow-300"],
              [IMG.house, "House", `${kw(snap.loadWatts)} kW`, "text-violet-300"],
              [IMG.grid, `Grid (${gridLabel})`, `${kw(snap.gridWatts)} kW`, importing ? "text-red-400" : "text-emerald-400"],
            ].map(([img, k, v, c]) => (
              <div key={k} className="flex items-center gap-3">
                <img src={img} alt="" className="h-10 w-10 object-contain" />
                <span className="flex-1 text-gray-300">{k}</span>
                <span className={clsx("font-bold tabular-nums", c)}>{v}</span>
              </div>
            ))}
          </div>
        </Panel>
        <Panel title="Controls">
          <BigButtons />
        </Panel>
        <div className="h-64" />
      </main>
    </>
  );
}

// ---------- 4: bottom tab bar navigation ----------

function VariantBottomNav() {
  const tabs = [
    ["🏠", "Home"],
    ["📈", "History"],
    ["🔌", "Sessions"],
    ["📜", "Events"],
    ["💲", "Usage"],
  ];
  return (
    <div className="pb-24">
      <header className="flex items-center justify-between border-b border-gray-800 px-4 py-3">
        <div className="flex items-center gap-2">
          <span className="text-2xl">⚡</span>
          <h1 className="text-xl font-semibold">Solar EV</h1>
          <span className="ml-1 h-2.5 w-2.5 rounded-full bg-green-400" title="live" />
        </div>
        <button className="flex h-11 w-11 items-center justify-center rounded-full bg-gray-800 text-xl">⋯</button>
      </header>
      <main className="space-y-4 p-4">
        <Panel title="System status">
          <PowerFlowDiagram snap={snap} />
        </Panel>
        <Panel title="Solar">
          <div className="space-y-2 text-lg">
            <Row k="PV" v={formatKW(snap.pvWatts)} />
            <Row k="Load" v={formatKW(snap.loadWatts)} />
            <Row k="Grid" v={formatKW(snap.gridWatts)} />
          </div>
        </Panel>
      </main>
      <nav className="fixed inset-x-0 bottom-0 z-20 grid grid-cols-5 border-t border-gray-800 bg-gray-950/95 pb-[env(safe-area-inset-bottom)] backdrop-blur">
        {tabs.map(([icon, label], i) => (
          <button key={label} className={clsx("flex h-16 flex-col items-center justify-center gap-0.5", i === 0 ? "text-green-400" : "text-gray-400")}>
            <span className="text-2xl">{icon}</span>
            <span className="text-xs font-medium">{label}</span>
          </button>
        ))}
      </nav>
    </div>
  );
}

// ---------- 5: large type, glanceable list ----------

function BigStat({ img, label, value, sub, tone }: { img: string; label: string; value: string; sub?: string; tone: string }) {
  return (
    <div className="flex items-center gap-4 border-b border-gray-800 py-4 last:border-0">
      <div className={clsx("flex h-16 w-16 shrink-0 items-center justify-center rounded-2xl", tone)}>
        <img src={img} alt="" className="h-12 w-12 object-contain" />
      </div>
      <div className="flex-1">
        <div className="text-lg text-gray-300">{label}</div>
        {sub && <div className="text-sm text-gray-500">{sub}</div>}
      </div>
      <div className="text-3xl font-bold tabular-nums">{value}</div>
    </div>
  );
}

function VariantLargeType() {
  return (
    <>
      <OldHeader />
      <main className="space-y-4 p-3">
        <div className="flex items-center justify-between px-1">
          <span className={clsx("rounded-full px-4 py-1.5 text-lg font-bold uppercase", STATE_BG[snap.state])}>{snap.state}</span>
          <span className="text-base text-gray-400">Updated 12 s ago</span>
        </div>
        <Panel className="py-1">
          <BigStat img={IMG.solar} label="Solar" sub="kW" value={kw(snap.pvWatts)} tone="bg-yellow-400/15" />
          <BigStat img={IMG.house} label="House" sub="kW" value={kw(snap.loadWatts)} tone="bg-violet-400/15" />
          <BigStat img={IMG.grid} label="Grid" sub={`${gridLabel} · kW`} value={kw(snap.gridWatts)} tone={importing ? "bg-red-500/20" : "bg-emerald-400/15"} />
          <BigStat img={IMG.ev} label="Tesla" sub={`${snap.batteryPct}% → ${snap.chargeLimit}%`} value={`${snap.actualAmps} A`} tone="bg-blue-400/15" />
        </Panel>
        <BigButtons />
      </main>
    </>
  );
}

// ---------- switcher ----------

const VARIANTS: Array<[string, string, () => JSX.Element]> = [
  ["0", "Current", Current],
  ["1", "Summary tiles", VariantTiles],
  ["2", "Portrait flow", VariantPortraitFlow],
  ["3", "Sticky status + EV hero", VariantStickyHero],
  ["4", "Bottom tab bar", VariantBottomNav],
  ["5", "Large type list", VariantLargeType],
];

function PrototypeSwitcher() {
  const params = new URLSearchParams(location.search);
  const initial = params.get("variant") ?? "1";
  const [idx, setIdx] = useState(Math.max(0, VARIANTS.findIndex(([k]) => k === initial)));
  const hideBar = params.has("nobar");

  const go = (d: number) => setIdx((i) => (i + d + VARIANTS.length) % VARIANTS.length);

  useEffect(() => {
    const url = new URL(location.href);
    url.searchParams.set("variant", VARIANTS[idx][0]);
    history.replaceState(null, "", url);
    window.scrollTo(0, 0);
  }, [idx]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "ArrowLeft") go(-1);
      if (e.key === "ArrowRight") go(1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const [key, name, View] = VARIANTS[idx];
  return (
    <div className="min-h-screen bg-gray-900 text-gray-100">
      <View />
      {!hideBar && (
        <div className="fixed bottom-20 left-1/2 z-50 flex -translate-x-1/2 items-center gap-2 rounded-full bg-fuchsia-600 px-2 py-1 text-sm font-semibold text-white shadow-2xl">
          <button className="px-2" onClick={() => go(-1)}>
            ◀
          </button>
          <span className="whitespace-nowrap">
            {key} ({name})
          </span>
          <button className="px-2" onClick={() => go(1)}>
            ▶
          </button>
        </div>
      )}
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <PrototypeSwitcher />
  </StrictMode>,
);

import { useState } from "react";
import clsx from "clsx";
import type { StateSnapshot } from "@/api/types";
import { ChargeLimitCard } from "@/components/ChargeLimitCard";
import { useChargerControls } from "@/hooks/useChargerControls";
import { formatKW } from "@/lib/format";
import { batteryProgress, gridBadge, statusBanner } from "@/lib/mobileStatus";

const MIN_AMPS = 5;
const MAX_AMPS = 32;

// Phone layout (below md): sticky status bar, EV hero, large power list and
// large tap-target controls. Desktop keeps the existing grid layout.
export function MobileDashboard({ snap }: { snap: StateSnapshot }) {
  const banner = statusBanner(snap);
  const badge = gridBadge(snap);

  return (
    <div className="space-y-4">
      <div
        className={clsx(
          "sticky top-0 z-10 -mx-4 -mt-6 flex items-center justify-between gap-3 px-4 py-3 text-base font-semibold shadow-lg",
          banner.className,
        )}
        role="status"
      >
        <span className="truncate">⚡ {banner.label}</span>
        <span
          className={clsx(
            "shrink-0 rounded px-2 py-0.5 tabular-nums",
            badge.importing ? "bg-red-600 text-white" : "bg-black/20",
          )}
        >
          {badge.label}
        </span>
      </div>

      {snap.lastError && (
        <div className="rounded-lg border border-red-500/40 bg-red-500/10 px-4 py-3 text-base text-red-200">
          <span className="font-semibold">Error:</span> {snap.lastError}
        </div>
      )}

      <EVHero snap={snap} />
      <PowerNow snap={snap} importing={badge.importing} />
      <MobileControls snap={snap} />
      <ChargeLimitCard snap={snap} />

      <dl className="grid grid-cols-3 gap-3 px-1 text-sm">
        <FooterStat label="Mode" value={snap.mode} />
        <FooterStat
          label="Vehicle"
          value={snap.carPluggedIn ? (snap.carOnline ? "online" : "offline") : "unplugged"}
        />
        <FooterStat label="Updated" value={formatClock(snap.lastUpdate)} />
      </dl>
    </div>
  );
}

function Panel({ title, children }: { title?: string; children: React.ReactNode }) {
  return (
    <section className="rounded-xl border border-gray-800 bg-gray-800/50 p-4">
      {title && <h2 className="mb-3 text-lg font-semibold text-gray-100">{title}</h2>}
      {children}
    </section>
  );
}

function EVHero({ snap }: { snap: StateSnapshot }) {
  const p = batteryProgress(snap);
  return (
    <Panel>
      <div className="flex items-center gap-4">
        <img src="/images/tesla_ev.png" alt="Tesla" className="h-20 w-28 object-contain" />
        <div className="min-w-0">
          <div className="text-5xl font-bold tabular-nums">{p.pct}%</div>
          <div className="text-base text-gray-400">{p.caption}</div>
        </div>
      </div>
      <div
        className="relative mt-4 h-4 overflow-hidden rounded-full bg-gray-700"
        role="progressbar"
        aria-label="Battery"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={p.pct}
      >
        <div className="h-full bg-green-500" style={{ width: `${p.pct}%` }} />
        {p.limitPct > 0 && (
          <div
            className="absolute inset-y-0 w-0.5 bg-white"
            style={{ left: `${p.limitPct}%` }}
            title={`Charge limit ${p.limitPct}%`}
          />
        )}
      </div>
      <div className="mt-1 flex justify-between text-sm text-gray-400">
        <span>0%</span>
        <span>limit {p.limitPct}%</span>
      </div>
    </Panel>
  );
}

function PowerNow({ snap, importing }: { snap: StateSnapshot; importing: boolean }) {
  const gridLabel = importing ? "importing" : snap.gridWatts < -50 ? "exporting" : "balanced";
  const rows: Array<{ img: string; label: string; value: string; tone: string }> = [
    { img: "/images/solar_panels.png", label: "Solar", value: formatKW(snap.pvWatts), tone: "text-yellow-300" },
    { img: "/images/house.png", label: "House", value: formatKW(snap.loadWatts), tone: "text-violet-300" },
    {
      img: "/images/electricity_pylon.png",
      label: `Grid (${gridLabel})`,
      value: formatKW(Math.abs(snap.gridWatts)),
      tone: importing ? "text-red-400" : "text-emerald-400",
    },
  ];
  return (
    <Panel title="Power now">
      <ul className="space-y-3 text-lg">
        {rows.map((r) => (
          <li key={r.label} className="flex items-center gap-3">
            <img src={r.img} alt="" className="h-10 w-10 object-contain" />
            <span className="flex-1 text-gray-300">{r.label}</span>
            <span className={clsx("font-bold tabular-nums", r.tone)}>{r.value}</span>
          </li>
        ))}
      </ul>
    </Panel>
  );
}

function MobileControls({ snap }: { snap: StateSnapshot }) {
  const c = useChargerControls(snap);
  const [amps, setAmps] = useState<number>(snap.targetAmps || 16);
  const manualDisabled = !c.isManual || c.busy;
  const btn =
    "h-14 rounded-xl text-lg font-semibold transition-colors disabled:cursor-not-allowed disabled:opacity-50";

  return (
    <Panel title="Controls">
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3" role="group" aria-label="Mode">
          <button
            type="button"
            aria-pressed={!c.isManual}
            disabled={c.busy}
            onClick={() => c.setMode("auto")}
            className={clsx(btn, !c.isManual ? "bg-green-600 text-white" : "bg-gray-700 text-gray-200")}
          >
            Auto
          </button>
          <button
            type="button"
            aria-pressed={c.isManual}
            disabled={c.busy}
            onClick={() => c.setMode("manual")}
            className={clsx(btn, c.isManual ? "bg-blue-600 text-white" : "bg-gray-700 text-gray-200")}
          >
            Manual
          </button>
        </div>

        {c.isManual ? (
          <>
            <div className="grid grid-cols-2 gap-3">
              <button type="button" disabled={manualDisabled} onClick={c.start} className={clsx(btn, "bg-green-600 text-white")}>
                Start
              </button>
              <button
                type="button"
                disabled={manualDisabled}
                onClick={c.stop}
                className={clsx(btn, "border-2 border-red-500 text-red-300")}
              >
                Stop charging
              </button>
            </div>
            <div className="flex items-center gap-3">
              <button
                type="button"
                aria-label="Decrease amps"
                disabled={manualDisabled || amps <= MIN_AMPS}
                onClick={() => setAmps((a) => Math.max(MIN_AMPS, a - 1))}
                className={clsx(btn, "w-14 bg-gray-700 text-2xl")}
              >
                −
              </button>
              <div className="flex-1 text-center text-2xl font-bold tabular-nums" aria-live="polite">
                {amps} A
              </div>
              <button
                type="button"
                aria-label="Increase amps"
                disabled={manualDisabled || amps >= MAX_AMPS}
                onClick={() => setAmps((a) => Math.min(MAX_AMPS, a + 1))}
                className={clsx(btn, "w-14 bg-gray-700 text-2xl")}
              >
                +
              </button>
              <button
                type="button"
                disabled={manualDisabled}
                onClick={() => c.setAmps(amps)}
                className={clsx(btn, "bg-blue-600 px-5 text-white")}
              >
                Set
              </button>
            </div>
          </>
        ) : (
          <p className="text-base text-gray-400">
            Auto mode adjusts charging to match solar surplus. Switch to Manual to start, stop or set amps.
          </p>
        )}

        <button
          type="button"
          disabled={c.busy}
          onClick={c.refresh}
          className={clsx(btn, "h-12 w-full bg-gray-800 text-base text-gray-200")}
        >
          {c.refreshing ? "Refreshing…" : "Force refresh"}
        </button>

        {c.error && <p className="text-base text-red-400">{c.error}</p>}
      </div>
    </Panel>
  );
}

function formatClock(iso: string): string {
  const d = new Date(iso);
  return iso && !Number.isNaN(d.getTime())
    ? d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" })
    : "—";
}

function FooterStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-gray-500">{label}</dt>
      <dd className="truncate font-medium text-gray-200" title={value}>
        {value}
      </dd>
    </div>
  );
}

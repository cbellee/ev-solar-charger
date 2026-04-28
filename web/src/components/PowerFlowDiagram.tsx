import type { StateSnapshot } from "@/api/types";
import clsx from "clsx";

interface Props {
  snap: StateSnapshot;
}

// Replicates the legacy SVG power-flow diagram:
// Solar -> House, Solar/Grid -> House, House -> EV, House <-> Grid.
// Animated dot when energy flows along that path.
export function PowerFlowDiagram({ snap }: Props) {
  const pv = snap.pvWatts;
  const grid = snap.gridWatts;
  const surplus = snap.surplusWatts;

  const hasPv = pv > 50;
  const isExporting = grid < -50;
  const isImporting = grid > 50;
  const isCharging = snap.state === "charging" || snap.actualAmps > 0;

  // EV branch only visible when plugged in AND (charging or surplus available).
  const evVisible = snap.carPluggedIn && (isCharging || surplus > 50);

  const evLabel = !snap.carPluggedIn
    ? "Unplugged"
    : isCharging
      ? `${snap.actualAmps} A`
      : surplus > 50
        ? `${(surplus / 1000).toFixed(2)} kW available`
        : "Idle";

  return (
    <div className="relative">
      <svg viewBox="0 0 600 320" className="w-full h-auto">
        <defs>
          <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse">
            <path d="M0,0 L10,5 L0,10 z" fill="currentColor" />
          </marker>
        </defs>

        {/* Solar -> House */}
        <path id="pathSH" d="M120 80 C 220 80, 240 160, 300 160" stroke={hasPv ? "#fbbf24" : "#374151"} strokeWidth={3} fill="none" />
        {hasPv && <FlowDot pathId="pathSH" color="#fbbf24" />}

        {/* Grid -> House (import) or House -> Grid (export) */}
        <path id="pathGH" d="M480 80 C 380 80, 360 160, 300 160" stroke={isImporting || isExporting ? (isImporting ? "#ef4444" : "#10b981") : "#374151"} strokeWidth={3} fill="none" />
        {(isImporting || isExporting) && <FlowDot pathId="pathGH" color={isImporting ? "#ef4444" : "#10b981"} reverse={isExporting} />}

        {/* House -> EV */}
        {evVisible && (
          <>
            <path id="pathHE" d="M300 160 C 360 220, 360 260, 420 280" stroke="#3b82f6" strokeWidth={3} fill="none" />
            <FlowDot pathId="pathHE" color="#3b82f6" />
          </>
        )}

        {/* Solar -> Grid (export from PV bypassing) - keep stylistic from legacy */}
        <path id="pathSG" d="M120 80 C 280 40, 320 40, 480 80" stroke={isExporting && hasPv ? "#10b981" : "#374151"} strokeWidth={2} fill="none" strokeDasharray="4 4" />

        {/* Nodes */}
        <Node x={120} y={80} label="Solar" sub={hasPv ? `${(pv / 1000).toFixed(2)} kW` : "0 kW"} color="#fbbf24" image="/images/solar_panels.png" />
        <Node x={300} y={160} label="House" sub={`${(snap.loadWatts / 1000).toFixed(2)} kW`} color="#a78bfa" image="/images/house.png" />
        <Node x={480} y={80} label="Grid" sub={isImporting ? `Import ${(grid / 1000).toFixed(2)} kW` : isExporting ? `Export ${(-grid / 1000).toFixed(2)} kW` : "0 kW"} color={isImporting ? "#ef4444" : "#10b981"} image="/images/electricity_pylon.png" />
        <Node x={420} y={280} label="EV" sub={evLabel} color={evVisible ? "#3b82f6" : "#6b7280"} image="/images/tesla_ev.png" muted={!snap.carPluggedIn} />
      </svg>
    </div>
  );
}

function Node({
  x,
  y,
  label,
  sub,
  color,
  image,
  muted,
}: {
  x: number;
  y: number;
  label: string;
  sub: string;
  color: string;
  image: string;
  muted?: boolean;
}) {
  const r = 36;
  return (
    <g className={clsx(muted && "opacity-50")}>
      <circle cx={x} cy={y} r={r} fill="#111827" stroke={color} strokeWidth={2} />
      <image
        href={image}
        x={x - r + 6}
        y={y - r + 6}
        width={(r - 6) * 2}
        height={(r - 6) * 2}
        preserveAspectRatio="xMidYMid meet"
      />
      <text x={x} y={y + r + 14} textAnchor="middle" fontSize="11" fill="#e5e7eb" fontWeight="600">
        {label}
      </text>
      <text x={x} y={y + r + 28} textAnchor="middle" fontSize="11" fill="#9ca3af">
        {sub}
      </text>
    </g>
  );
}

function FlowDot({ pathId, color, reverse }: { pathId: string; color: string; reverse?: boolean }) {
  return (
    <circle r={4} fill={color}>
      <animateMotion dur="2.5s" repeatCount="indefinite" keyPoints={reverse ? "1;0" : "0;1"} keyTimes="0;1">
        <mpath xlinkHref={`#${pathId}`} />
      </animateMotion>
    </circle>
  );
}

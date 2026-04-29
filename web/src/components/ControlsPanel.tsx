import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Mode, StateSnapshot } from "@/api/types";
import { Card } from "./Card";
import clsx from "clsx";

interface Props {
  snap: StateSnapshot;
}

export function ControlsPanel({ snap }: Props) {
  const qc = useQueryClient();
  const [amps, setAmps] = useState<number>(snap.targetAmps || 16);
  const [error, setError] = useState<string | null>(null);

  const setMode = useMutation({
    mutationFn: (mode: Mode) => api.setMode(mode),
    onError: (e) => setError((e as Error).message),
    onSuccess: () => {
      setError(null);
      qc.invalidateQueries({ queryKey: ["state"] });
    },
  });

  const control = useMutation({
    mutationFn: (vars: { action: "start" | "stop" | "setAmps"; amps?: number }) =>
      api.control(vars.action, vars.amps),
    onError: (e) => setError((e as Error).message),
    onSuccess: () => setError(null),
  });

  const refresh = useMutation({
    mutationFn: () => api.forceRefresh(),
    onError: (e) => setError((e as Error).message),
    onSuccess: () => {
      setError(null);
      qc.invalidateQueries({ queryKey: ["state"] });
    },
  });

  const isManual = snap.mode === "manual";
  const busy = setMode.isPending || control.isPending || refresh.isPending;

  return (
    <Card title="Controls">
      <div className="space-y-4">
        <div>
          <div className="text-xs text-gray-400 mb-1.5">Mode</div>
          <div className="flex items-center gap-2">
            <div className="inline-flex rounded-md border border-gray-700 overflow-hidden">
              <ModeBtn active={!isManual} onClick={() => setMode.mutate("auto")} disabled={busy}>
                Auto
              </ModeBtn>
              <ModeBtn active={isManual} onClick={() => setMode.mutate("manual")} disabled={busy}>
                Manual
              </ModeBtn>
            </div>
            <button
              type="button"
              className="rounded border border-gray-700 bg-gray-800 hover:bg-gray-700 disabled:opacity-50 px-3 py-1.5 text-sm font-medium text-gray-200"
              disabled={busy}
              onClick={() => refresh.mutate()}
              title="Clear cooldowns and re-poll Tesla immediately"
            >
              {refresh.isPending ? "Refreshing…" : "Force refresh"}
            </button>
          </div>
        </div>

        <div className={clsx(!isManual && "opacity-50")}>
          <div className="text-xs text-gray-400 mb-1.5">Vehicle controls (manual mode)</div>
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              className="rounded bg-green-600 hover:bg-green-500 disabled:opacity-50 px-3 py-1.5 text-sm font-medium"
              disabled={!isManual || busy}
              onClick={() => control.mutate({ action: "start" })}
            >
              Start
            </button>
            <button
              type="button"
              className="rounded bg-red-600 hover:bg-red-500 disabled:opacity-50 px-3 py-1.5 text-sm font-medium"
              disabled={!isManual || busy}
              onClick={() => control.mutate({ action: "stop" })}
            >
              Stop
            </button>
            <div className="flex items-center gap-2 ml-2">
              <input
                type="number"
                min={5}
                max={32}
                value={amps}
                onChange={(e) => setAmps(Number(e.target.value))}
                className="w-20 rounded bg-gray-900 border border-gray-700 px-2 py-1 text-sm"
                disabled={!isManual || busy}
              />
              <button
                type="button"
                className="rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-50 px-3 py-1.5 text-sm font-medium"
                disabled={!isManual || busy || amps <= 0}
                onClick={() => control.mutate({ action: "setAmps", amps })}
              >
                Set Amps
              </button>
            </div>
          </div>
        </div>

        {error && <p className="text-xs text-red-400">{error}</p>}
      </div>
    </Card>
  );
}

function ModeBtn({
  active,
  disabled,
  onClick,
  children,
}: {
  active: boolean;
  disabled?: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={clsx(
        "px-3 py-1.5 text-sm font-medium transition-colors",
        active ? "bg-blue-600 text-white" : "bg-gray-800 hover:bg-gray-700 text-gray-300",
        disabled && "opacity-50 cursor-not-allowed",
      )}
    >
      {children}
    </button>
  );
}

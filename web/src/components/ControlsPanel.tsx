import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Mode, StateSnapshot } from "@/api/types";
import { COMMON_TIMEZONE_GROUPS, formatHour } from "@/lib/timezones";
import { Card } from "./Card";
import clsx from "clsx";

interface Props {
  snap: StateSnapshot;
}

export function ControlsPanel({ snap }: Props) {
  const qc = useQueryClient();
  const [amps, setAmps] = useState<number>(snap.targetAmps || 16);
  const [error, setError] = useState<string | null>(null);
  const [freeWindowEnabled, setFreeWindowEnabled] = useState<boolean>(snap.freeWindowEnabled);
  const [freeWindowStartHour, setFreeWindowStartHour] = useState<number>(snap.freeWindowStartHour);
  const [freeWindowEndHour, setFreeWindowEndHour] = useState<number>(snap.freeWindowEndHour);
  const [freeWindowTimezone, setFreeWindowTimezone] = useState<string>(snap.freeWindowTimezone || "Australia/Sydney");
  const [settingsError, setSettingsError] = useState<string | null>(null);

  const freeWindowQuery = useQuery({
    queryKey: ["free-window"],
    queryFn: api.getFreeWindow,
    staleTime: Infinity,
  });

  useEffect(() => {
    if (!freeWindowQuery.data) {
      return;
    }
    setFreeWindowEnabled(freeWindowQuery.data.enabled);
    setFreeWindowStartHour(freeWindowQuery.data.startHour);
    setFreeWindowEndHour(freeWindowQuery.data.endHour);
    setFreeWindowTimezone(freeWindowQuery.data.timezone);
  }, [freeWindowQuery.data]);

  useEffect(() => {
    if (freeWindowEndHour <= freeWindowStartHour) {
      setFreeWindowEndHour(Math.min(24, freeWindowStartHour + 1));
    }
  }, [freeWindowStartHour, freeWindowEndHour]);

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

  const saveFreeWindow = useMutation({
    mutationFn: () =>
      api.setFreeWindow({
        enabled: freeWindowEnabled,
        startHour: freeWindowStartHour,
        endHour: freeWindowEndHour,
        timezone: freeWindowTimezone,
      }),
    onError: (e) => setSettingsError((e as Error).message),
    onSuccess: () => {
      setSettingsError(null);
      qc.invalidateQueries({ queryKey: ["free-window"] });
      qc.invalidateQueries({ queryKey: ["state"] });
    },
  });

  const isManual = snap.mode === "manual";
  const busy = setMode.isPending || control.isPending || refresh.isPending;
  const settingsBusy = freeWindowQuery.isLoading || saveFreeWindow.isPending;
  const endHourOptions = Array.from({ length: 24 - freeWindowStartHour }, (_, i) => freeWindowStartHour + 1 + i);
  const settingsValidationError =
    freeWindowEndHour <= freeWindowStartHour ? "End hour must be greater than start hour" : null;

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

        <div className="border-t border-gray-800 pt-4 space-y-3">
          <div className="text-xs text-gray-400">Free electricity window (auto mode)</div>

          <label className="inline-flex items-center gap-2 text-sm text-gray-200">
            <input
              type="checkbox"
              checked={freeWindowEnabled}
              onChange={(e) => setFreeWindowEnabled(e.target.checked)}
              disabled={settingsBusy}
              className="accent-blue-500"
            />
            Enabled
          </label>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <label className="text-xs text-gray-400">
              Start hour
              <select
                value={freeWindowStartHour}
                onChange={(e) => setFreeWindowStartHour(Number(e.target.value))}
                disabled={settingsBusy}
                className="mt-1 w-full rounded bg-gray-900 border border-gray-700 px-2 py-1 text-sm text-gray-200"
              >
                {Array.from({ length: 24 }, (_, i) => i).map((hour) => (
                  <option key={hour} value={hour}>
                    {formatHour(hour)}
                  </option>
                ))}
              </select>
            </label>

            <label className="text-xs text-gray-400">
              End hour
              <select
                value={freeWindowEndHour}
                onChange={(e) => setFreeWindowEndHour(Number(e.target.value))}
                disabled={settingsBusy}
                className="mt-1 w-full rounded bg-gray-900 border border-gray-700 px-2 py-1 text-sm text-gray-200"
              >
                  {endHourOptions.map((hour) => (
                  <option key={hour} value={hour}>
                    {formatHour(hour)}
                  </option>
                ))}
              </select>
            </label>
          </div>

          <label className="text-xs text-gray-400 block">
            Timezone
            <select
              value={freeWindowTimezone}
              onChange={(e) => setFreeWindowTimezone(e.target.value)}
              disabled={settingsBusy}
              className="mt-1 w-full rounded bg-gray-900 border border-gray-700 px-2 py-1 text-sm text-gray-200"
            >
              {COMMON_TIMEZONE_GROUPS.map((group) => (
                <optgroup key={group.region} label={group.region}>
                  {group.zones.map((zone) => (
                    <option key={zone} value={zone}>
                      {zone}
                    </option>
                  ))}
                </optgroup>
              ))}
            </select>
          </label>

          <button
            type="button"
            className="rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-50 px-3 py-1.5 text-sm font-medium"
            disabled={settingsBusy || Boolean(settingsValidationError)}
            onClick={() => saveFreeWindow.mutate()}
          >
            {saveFreeWindow.isPending ? "Saving window…" : "Save free window"}
          </button>

          {settingsValidationError && <p className="text-xs text-red-400">{settingsValidationError}</p>}
          {settingsError && <p className="text-xs text-red-400">{settingsError}</p>}
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

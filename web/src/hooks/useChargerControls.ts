import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Mode, StateSnapshot } from "@/api/types";

// Shared mode / start / stop / amps / refresh actions for the desktop and
// mobile control panels.
export function useChargerControls(snap: StateSnapshot) {
  const qc = useQueryClient();
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

  return {
    error,
    isManual: snap.mode === "manual",
    busy: setMode.isPending || control.isPending || refresh.isPending,
    refreshing: refresh.isPending,
    setMode: (mode: Mode) => setMode.mutate(mode),
    start: () => control.mutate({ action: "start" }),
    stop: () => control.mutate({ action: "stop" }),
    setAmps: (amps: number) => control.mutate({ action: "setAmps", amps }),
    refresh: () => refresh.mutate(),
  };
}

import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { StateSnapshot } from "@/api/types";
import { Card } from "./Card";

interface Props {
  snap: StateSnapshot;
}

// ChargeLimitCard shows a slider that controls the vehicle's charge_limit_soc.
// The vehicle reports its allowed range (typically 50–100); we fall back to
// 50/100 if the values aren't yet populated. The slider stays in sync with
// the live snapshot when the user isn't actively dragging.
export function ChargeLimitCard({ snap }: Props) {
  const qc = useQueryClient();
  const min = snap.chargeLimitMin > 0 ? snap.chargeLimitMin : 50;
  const max = snap.chargeLimitMax > 0 ? snap.chargeLimitMax : 100;
  const current = snap.chargeLimit > 0 ? snap.chargeLimit : 80;

  const [pending, setPending] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const value = pending ?? current;

  // Reset pending value once the live snapshot catches up (or drifts to the
  // value we requested), so subsequent external changes are reflected.
  useEffect(() => {
    if (pending !== null && current === pending) {
      setPending(null);
    }
  }, [current, pending]);

  const mutate = useMutation({
    mutationFn: (percent: number) => api.setChargeLimit(percent),
    onError: (e) => {
      setError((e as Error).message);
      setPending(null);
    },
    onSuccess: () => {
      setError(null);
      qc.invalidateQueries({ queryKey: ["state"] });
    },
  });

  const disabled = !snap.carOnline || snap.testMode || mutate.isPending;

  return (
    <Card title="Charge Limit">
      <div className="space-y-3">
        <div className="flex items-baseline justify-between">
          <span className="text-2xl font-semibold tabular-nums text-gray-100">
            {value}%
          </span>
          <span className="text-xs text-gray-500">
            range {min}–{max}%
          </span>
        </div>
        <input
          type="range"
          min={min}
          max={max}
          step={1}
          value={value}
          disabled={disabled}
          onChange={(e) => setPending(Number(e.target.value))}
          onMouseUp={(e) => {
            const v = Number((e.target as HTMLInputElement).value);
            if (v !== current) mutate.mutate(v);
          }}
          onTouchEnd={(e) => {
            const v = Number((e.target as HTMLInputElement).value);
            if (v !== current) mutate.mutate(v);
          }}
          onKeyUp={(e) => {
            const v = Number((e.target as HTMLInputElement).value);
            if (v !== current) mutate.mutate(v);
          }}
          className="w-full accent-blue-500 disabled:opacity-50"
        />
        <div className="flex justify-between text-xs text-gray-500 tabular-nums">
          <span>{min}%</span>
          <span>{max}%</span>
        </div>
        {!snap.carOnline && (
          <p className="text-xs text-gray-500">
            Vehicle offline — limit will apply once the car wakes.
          </p>
        )}
        {snap.testMode && (
          <p className="text-xs text-gray-500">Disabled in test mode.</p>
        )}
        {mutate.isPending && (
          <p className="text-xs text-gray-400">Setting limit…</p>
        )}
        {error && <p className="text-xs text-red-400">{error}</p>}
      </div>
    </Card>
  );
}

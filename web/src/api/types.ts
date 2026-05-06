// Backend response types. Mirrors Go structs:
//   - StateSnapshot: internal/controller/controller.go (json-tagged camelCase)
//   - Reading, ChargeSession, Event, APIUsageSnapshot: internal/storage/storage.go
//     (no JSON tags -> PascalCase field names)
//   - apiUsageResponse: internal/web/handlers.go (json-tagged camelCase)
//
// Keep field casing as the wire format: dashboard SSE uses camelCase, the
// history/sessions/events/usage-history endpoints serialise the storage
// structs as PascalCase. Fragile but matches today's behavior; consolidate
// behind explicit JSON tags later if convenient.

export type ControllerState =
  | "idle"
  | "monitoring"
  | "charging"
  | "stopped_low_surplus"
  | "wake_pending"
  | "error";

export type Mode = "auto" | "manual";

export interface StateSnapshot {
  state: ControllerState;
  mode: Mode;
  testMode: boolean;
  pvWatts: number;
  gridWatts: number;
  loadWatts: number;
  surplusWatts: number;
  targetAmps: number;
  actualAmps: number;
  batteryPct: number;
  timeToLimitHours: number;
  chargeLimit: number;
  chargeLimitMin: number;
  chargeLimitMax: number;
  carPluggedIn: boolean;
  carOnline: boolean;
  chargingState: string;
  consecutiveLow: number;
  consecutiveSurplus: number;
  lastUpdate: string;
  lastError: string;
}

export interface Reading {
  ID: number;
  Timestamp: string;
  PVWatts: number;
  GridWatts: number;
  LoadWatts: number;
  SurplusWatts: number;
  ChargeAmps: number;
  BatteryPct: number;
  State: string;
}

export interface ChargeSession {
  ID: number;
  StartTime: string;
  EndTime: string;
  StartBattery: number;
  EndBattery: number;
  EnergyKWh: number;
  PeakAmps: number;
  AvgAmps: number;
}

export interface EventRecord {
  ID: number;
  Timestamp: string;
  Type: string;
  Message: string;
  Details: string;
}

export interface APIUsageResponse {
  dataCalls: number;
  dataCost: number;
  commandCalls: number;
  commandCost: number;
  wakeCalls: number;
  wakeCost: number;
  streamSignals: number;
  streamCost: number;
  estimatedCost: number;
  monthlyDiscount: number;
  netCost: number;
  monthStarted: string;
}

export interface APIUsageSnapshot {
  ID: number;
  Timestamp: string;
  DataCalls: number;
  CommandCalls: number;
  WakeCalls: number;
  StreamSignals: number;
  EstimatedCost: number;
}

export type HistoryInterval = "minute" | "hour" | "day";

export interface HistoryQuery {
  from: string;
  to: string;
  interval: HistoryInterval;
  limit: number;
  offset: number;
}

export type ControlAction = "start" | "stop" | "setAmps";

# Solar EV Charger Controller — Implementation Plan

## 1. Overview

A single Go binary, packaged in one Docker container, that continuously monitors surplus solar generation from a Sungrow SG8.0RS inverter (via WiNet-S dongle) and automatically regulates the charging amperage of a 2026 Tesla Model 3 RWD via the Tesla Fleet API. The system ensures the car consumes only electricity that would otherwise be exported to the grid.

All data is persisted in a SQLite database for historical reporting and searchable via the web interface.

### Design Principles

- **12-Factor App** — config via env vars, logs to stdout, stateless process, port binding, dev/prod parity
- **Idiomatic Go** — follow [Effective Go](https://go.dev/doc/effective_go), [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments), and the project's [`.github/copilot-instructions.md`](.github/copilot-instructions.md)
  - `gofmt`/`goimports` for formatting; `MixedCaps` naming; short lowercase package names (no underscores/hyphens)
  - Happy path left-aligned — return early to reduce nesting; prefer `if err != nil { return }` over if-else chains
  - Accept interfaces, return concrete types; keep interfaces small (1–3 methods); define interfaces close to where they are **used**, not where they are implemented
  - Errors as values — wrap with `fmt.Errorf("pkg: context: %w", err)`; error messages lowercase, no trailing punctuation; use `errors.Is`/`errors.As` for checking
  - Use `any` instead of `interface{}` (Go 1.18+); prefer generics with constraints over unconstrained types
  - Make the zero value useful; use strong typing to prevent invalid states
  - Favour standard library solutions — `strings.Builder`, `filepath.Join`, `net/http` ServeMux (Go 1.22+ pattern routing)
  - No emoji in code or comments
- **Interface-driven testability** — all external systems (inverter, vehicle, storage) accessed via narrow Go interfaces; concrete implementations injected at startup; tests use hand-written mocks
- **Observable** — OpenTelemetry traces, metrics, and logs via `otelslog` bridge to `log/slog`; stdout export by default, OTLP collector when configured
- **HTTP client discipline** — client structs hold only configuration and dependencies (base URL, `*http.Client`, auth, logger); never store `*http.Request` or per-request state on the struct; construct a fresh request per method call; set `req.GetBody` for retry/redirect body reuse; always `defer resp.Body.Close()`
- **Concurrency safety** — use `sync.Mutex` / `sync.RWMutex` for protecting shared state; keep critical sections small; always know how a goroutine will exit; use `sync.WaitGroup.Go` (Go 1.25+)
- **Security** — validate all external input; sanitize FTS5 queries; use `crypto/rand` for randomness; TLS for Tesla API; no secrets in logs

### Hardware

| Component | Model | Interface |
|---|---|---|
| Solar Inverter | Sungrow SG8.0RS (string) | WiNet-S dongle, WebSocket `ws://<ip>:8082` |
| Smart Meter | Sungrow DTSU666 (installed) | Read via inverter registers 5083/5091 |
| EV Charger | Tesla Gen 3 Wall Connector | Controlled via vehicle (not directly) |
| Vehicle | Tesla Model 3 2026 RWD | Fleet API `set_charging_amps` / `charge_start` / `charge_stop` |
| Line Voltage | 240V single-phase (Australia) | — |

### Go Coding Standards Reference

This project follows the coding standards defined in [`.github/copilot-instructions.md`](.github/copilot-instructions.md). Key conventions enforced in all Go code:

| Area | Convention |
|---|---|
| **Naming** | `MixedCaps` (exported) / `mixedCaps` (unexported); no underscores; avoid stuttering (e.g. `http.Server` not `http.HTTPServer`); interfaces named with `-er` suffix |
| **Packages** | Lowercase, singular, single-word; `cmd/` for mains, `internal/` for private packages; each `.go` file has exactly one `package` declaration matching the directory |
| **Errors** | Lowercase messages, no trailing punctuation; wrap with `%w`; create sentinels with `errors.New`; check with `errors.Is`/`errors.As`; don't log **and** return (choose one) |
| **Types** | Use `any` not `interface{}`; prefer generics with constraints; use struct tags for JSON; explicit type conversions |
| **HTTP clients** | Struct holds config only (base URL, `*http.Client`, auth); never store `*http.Request`; build fresh request per call; set `req.GetBody` for retries; `defer resp.Body.Close()` |
| **IO / Readers** | Readers are single-use; buffer with `io.ReadAll` + `bytes.NewReader` for reuse; use `io.NopCloser` for request body reset |
| **Concurrency** | Use `sync.WaitGroup.Go` (Go 1.25+); goroutines must have known exit paths; prefer caller-controlled concurrency in libraries |
| **Testing** | Table-driven tests; `Test_functionName_scenario` naming; `t.Helper()` on helpers; `t.Cleanup()` for resource teardown; `-race` flag; `_test` package for black-box tests |
| **Formatting** | `gofmt` + `goimports`; no emoji in code; comments in complete sentences starting with the symbol name |
| **Router** | Use enhanced `net/http` `ServeMux` with method+pattern routing (Go 1.22+) |
| **Path construction** | Use `filepath.Join` for file paths, never manual string concatenation |

---

## 2. Project Structure

```
solar-ev-charger/
├── cmd/
│   └── server/
│       └── main.go                         # Entry point: config → OTel → storage → clients → controller → web → serve
├── internal/
│   ├── config/
│   │   ├── config.go                       # Config struct + Load() from env vars
│   │   └── config_test.go                  # Table-driven: valid, missing required, bad values, defaults
│   ├── observability/
│   │   ├── otel.go                         # SetupOTelSDK(), NewLogger(), metric instrument registration
│   │   └── otel_test.go                    # SDK init, shutdown, logger output, meter registration
│   ├── storage/
│   │   ├── storage.go                      # Store interface + Reading/ChargeSession/Event types
│   │   ├── sqlite.go                       # SQLiteStore implementation (pure Go driver)
│   │   └── sqlite_test.go                  # Table-driven insert/query/prune/search/concurrency tests
│   ├── inverter/
│   │   ├── inverter.go                     # InverterReader interface + PowerData struct
│   │   ├── sungrow.go                      # SungrowClient implementing InverterReader
│   │   └── sungrow_test.go                 # httptest mock for HTTP registers + WebSocket connect
│   ├── tesla/
│   │   ├── tesla.go                        # VehicleController interface + ChargeState struct + APIUsage types + sentinel errors
│   │   ├── client.go                       # TeslaClient implementing VehicleController
│   │   ├── testmode.go                     # TestModeClient: no-op VehicleController for TESLA_TEST_MODE
│   │   └── client_test.go                  # httptest mock for Fleet API endpoints + APIUsageTracker tests
│   ├── controller/
│   │   ├── controller.go                   # State machine + control loop + Tick() + 1-min averaging
│   │   └── controller_test.go              # State transitions, surplus math, deadband, wake logic, storage flush
│   └── web/
│       ├── server.go                       # NewServer(), route registration, otelhttp middleware
│       ├── handlers.go                     # HTTP handler functions (state, control, mode, history, search)
│       ├── handlers_test.go                # httptest.NewRecorder per endpoint
│       ├── sse.go                          # SSE Hub + client management
│       ├── sse_test.go                     # Broadcast, subscribe, disconnect cleanup
│       └── templates/
│           └── index.html                  # Dashboard: Tailwind CSS + htmx + EventSource + history/search
├── Dockerfile                              # Multi-stage: golang:1.25-alpine → distroless/static
├── docker-compose.yml                      # Single service, port 8080, secrets volume, data volume, env_file
├── .env.example                            # Documented env var template with defaults
├── go.mod
├── go.sum
├── function/                               # Azure Function app for Tesla OAuth flow (separate go.mod)
└── infra/                                  # Azure infrastructure (Bicep templates)
```

---

## 3. Environment Variables (12-Factor III)

All configuration is read from environment variables at startup. No config files. The application fails fast with a clear error message if required variables are missing.

| Variable | Required | Default | Description |
|---|---|---|---|
| `SUNGROW_HOST` | **yes** | — | WiNet-S dongle IP address on LAN |
| `SUNGROW_PORT` | no | `8082` | WiNet-S WebSocket port |
| `TESLA_CLIENT_ID` | **yes** | — | Fleet API application client ID |
| `TESLA_CLIENT_SECRET` | **yes** | — | Fleet API application client secret |
| `TESLA_REFRESH_TOKEN` | **yes** | — | OAuth2 refresh token (obtained via `/auth/tesla` flow) |
| `TESLA_VIN` | **yes** | — | Vehicle Identification Number |
| `TESLA_PRIVATE_KEY_PATH` | no | `/secrets/fleet-key.pem` | Path to EC private key PEM for Virtual Key signing |
| `TESLA_REGION` | no | `na` | Fleet API region: `na`, `eu`, `cn` |
| `POLL_INTERVAL_SECONDS` | no | `10` | Seconds between control loop ticks |
| `MIN_CHARGE_AMPS` | no | `5` | Minimum amps to sustain charging (Tesla minimum) |
| `MAX_CHARGE_AMPS` | no | `32` | Maximum amps (circuit breaker / EVSE limit) |
| `LINE_VOLTAGE` | no | `240` | Mains voltage for watts-to-amps conversion |
| `DEADBAND_POLLS` | no | `3` | Consecutive low-surplus polls before stopping charge |
| `WAKE_THRESHOLD_POLLS` | no | `6` | Consecutive surplus polls before waking sleeping car |
| `TESLA_CHARGING_POLL_SECONDS` | no | `60` | Seconds between Tesla API polls while actively charging |
| `TESLA_IDLE_POLL_SECONDS` | no | `300` | Seconds between Tesla API polls when idle with surplus |
| `AMPS_CHANGE_THRESHOLD` | no | `2` | Minimum amp change required to send a `SetChargingAmps` command |
| `TESLA_TEST_MODE` | no | `false` | Skip Tesla connectivity; publish projected charging values only |
| `HTTP_PORT` | no | `8080` | Web UI and API listen port |
| `HTTP_AUTH_USER` | no | `admin` | HTTP basic auth username |
| `HTTP_AUTH_PASSWORD` | **yes** | — | HTTP basic auth password |
| `LOG_LEVEL` | no | `info` | Minimum slog level: `debug`, `info`, `warn`, `error` |
| `DB_PATH` | no | `/data/solar-ev-charger.db` | Path to SQLite database file |
| `DB_RETENTION_DAYS` | no | `365` | Auto-prune records older than this |
| `OTEL_SERVICE_NAME` | no | `solar-ev-charger` | OTel resource service name |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | *(unset = stdout)* | OTLP collector endpoint; omit for stdout export |
| `OTEL_TRACES_EXPORTER` | no | *(auto)* | `otlp`, `none`, or blank (stdout) |
| `OTEL_METRICS_EXPORTER` | no | *(auto)* | `otlp`, `prometheus`, `none` |
| `OTEL_LOGS_EXPORTER` | no | *(auto)* | `otlp`, `none` |

---

## 4. Go Module Dependencies

```
module github.com/cbellee/solar-ev-charger

go 1.25

require (
    github.com/teslamotors/vehicle-command          // Tesla Virtual Key signing + Fleet API commands
    nhooyr.io/websocket                             // WebSocket client for WiNet-S dongle
    modernc.org/sqlite                              // Pure Go SQLite driver (no CGO)

    go.opentelemetry.io/otel                        // OTel core API
    go.opentelemetry.io/otel/sdk                    // OTel SDK (trace, metric, log providers)
    go.opentelemetry.io/otel/sdk/metric             // Metric SDK
    go.opentelemetry.io/otel/sdk/log                // Log SDK (beta)
    go.opentelemetry.io/otel/exporters/stdout/stdouttrace
    go.opentelemetry.io/otel/exporters/stdout/stdoutmetric
    go.opentelemetry.io/otel/exporters/stdout/stdoutlog
    go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
    go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp
    go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp
    go.opentelemetry.io/contrib/exporters/autoexport       // 12-factor exporter selection via OTEL_*_EXPORTER env vars
    go.opentelemetry.io/contrib/bridges/otelslog           // slog → OTel log bridge
    go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp  // HTTP middleware for auto-tracing
)
```

---

## 5. Phase-by-Phase Implementation

### Phase 1 — Project Scaffold & Configuration

**File: `internal/config/config.go`**

Define a `Config` struct with exported fields. `Load()` reads env vars via `os.Getenv`, applies defaults, and validates required fields. Return `(Config, error)` — never panic on bad config; return a descriptive error. Use Go's `strconv.Atoi` for integer parsing.

```go
type Config struct {
    SungrowHost               string
    SungrowPort               int
    TeslaClientID             string
    TeslaClientSecret         string
    TeslaRefreshToken         string
    TeslaVIN                  string
    TeslaPrivateKeyPath       string
    TeslaRegion               string
    TeslaTestMode             bool
    PollInterval              time.Duration
    MinChargeAmps             int
    MaxChargeAmps             int
    LineVoltage               int
    DeadbandPolls             int
    WakeThresholdPolls        int
    TeslaChargingPollInterval time.Duration
    TeslaIdlePollInterval     time.Duration
    AmpsChangeThreshold       int
    HTTPHost                  string
    HTTPPort                  int
    HTTPAuthUser              string
    HTTPAuthPassword          string
    LogLevel                  slog.Level
    DBPath                    string
    DBRetentionDays           int
}

func Load() (Config, error)
    // For each field:
    // 1. os.Getenv(key)
    // 2. If empty and required → return error with field name
    // 3. If empty and optional → apply default
    // 4. Parse/validate (int range, non-empty string, valid slog level)
    // 5. Populate struct field
    // Return (cfg, nil) on success
```

**Go best practices applied:**
- Unexported helper `envOrDefault(key, fallback string) string` for DRY env var reads
- `error` returned with `fmt.Errorf("config: %s is required", key)` — lowercase error messages, no trailing punctuation (per Go convention)
- No `init()` — explicit call from `main()`
- Use `filepath.Join` for constructing file paths (e.g. `DBPath` default)

**File: `internal/config/config_test.go`**

Table-driven tests using `t.Setenv()` (Go 1.17+, auto-restored after test).

**Test naming convention:** `Test_functionName_scenario` with underscore separator (per project standard). Example: `Test_Load_missingRequiredVar`.

| Test case | Setup | Expected |
|---|---|---|
| All required vars set | `t.Setenv` all required | `Config` populated, `err == nil` |
| Missing `SUNGROW_HOST` | Omit it | `err` contains `"SUNGROW_HOST"` |
| Missing `TESLA_VIN` | Omit it | `err` contains `"TESLA_VIN"` |
| Invalid `POLL_INTERVAL_SECONDS` = `"abc"` | Set invalid | `err` contains parse message |
| `MIN_CHARGE_AMPS` = `0` | Set to 0 | `err` (must be >= 1) |
| `MAX_CHARGE_AMPS` < `MIN_CHARGE_AMPS` | max=3, min=5 | `err` |
| Defaults applied | Set only required | Port=8082, PollInterval=10s, etc. |
| `LOG_LEVEL` = `"debug"` | Set it | `cfg.LogLevel == slog.LevelDebug` |
| `LOG_LEVEL` = `"invalid"` | Set it | `err` |
| `DB_PATH` default | Omit it | `cfg.DBPath == "/data/solar-ev-charger.db"` |
| `DB_RETENTION_DAYS` = `"90"` | Set it | `cfg.DBRetentionDays == 90` |

---

### Phase 2 — OpenTelemetry SDK + slog Bridge

**File: `internal/observability/otel.go`**

```go
// SetupOTelSDK initializes OpenTelemetry providers for traces, metrics, and logs.
// Returns a shutdown function that flushes and closes all providers.
// Exporter selection is driven by OTEL_*_EXPORTER env vars via autoexport.
// When OTEL_EXPORTER_OTLP_ENDPOINT is unset, autoexport defaults to stdout.
func SetupOTelSDK(ctx context.Context, serviceName, serviceVersion string) (shutdown func(context.Context) error, err error)
    // 1. Build resource:
    //    res = sdkresource.Merge(sdkresource.Default(),
    //          sdkresource.NewWithAttributes(semconv.SchemaURL,
    //              semconv.ServiceName(serviceName),
    //              semconv.ServiceVersion(serviceVersion)))
    //
    // 2. Set text map propagator:
    //    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
    //        propagation.TraceContext{}, propagation.Baggage{}))
    //
    // 3. Traces:
    //    spanExporter = autoexport.NewSpanExporter(ctx)
    //    tp = sdktrace.NewTracerProvider(WithBatcher(spanExporter), WithResource(res))
    //    otel.SetTracerProvider(tp)
    //
    // 4. Metrics:
    //    metricReader = autoexport.NewMetricReader(ctx)
    //    mp = sdkmetric.NewMeterProvider(WithResource(res), WithReader(metricReader))
    //    otel.SetMeterProvider(mp)
    //
    // 5. Logs:
    //    logExporter = autoexport.NewLogExporter(ctx)
    //    lp = sdklog.NewLoggerProvider(WithResource(res), WithProcessor(sdklog.NewBatchProcessor(logExporter)))
    //    global.SetLoggerProvider(lp)
    //
    // 6. Collect shutdown funcs; return unified shutdown
```

```go
// NewLogger creates a *slog.Logger that writes to both stdout (JSON) and OTel log pipeline.
// The stdout handler ensures 12-factor compliance (logs as event streams to stdout).
// The otelslog bridge sends log records to the OTel LoggerProvider for export.
func NewLogger(name string, level slog.Level) *slog.Logger
    // stdoutHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level, AddSource: true})
    // otelHandler   = otelslog.NewHandler(name, otelslog.WithLoggerProvider(global.GetLoggerProvider()))
    // multiHandler  = custom handler that fans out to both
    // return slog.New(multiHandler)
```

```go
// Metrics registers custom OTel metric instruments and returns a Metrics struct
// holding references to all instruments for use by controller/web packages.
type Metrics struct {
    PVPower          metric.Float64Gauge        // solar.pv_power (W)
    GridPower        metric.Float64Gauge        // solar.grid_power (W, signed)
    SurplusPower     metric.Float64Gauge        // solar.surplus_power (W)
    LoadPower        metric.Float64Gauge        // solar.load_power (W)
    ChargeAmps       metric.Int64Gauge          // ev.charge_amps (A)
    BatteryPct       metric.Float64Gauge        // ev.battery_pct (%)
    ChargeCommands   metric.Int64Counter        // ev.charge_commands ({command})
    StateChanges     metric.Int64Counter        // controller.state_changes ({transition})
    PollDuration     metric.Float64Histogram    // controller.poll_duration (s)
}

func NewMetrics() (Metrics, error)
    // meter = otel.Meter("solar-ev-charger")
    // Register each instrument via meter.Float64Gauge(...), meter.Int64Counter(...), etc.
    // Each with metric.WithDescription() and metric.WithUnit()
```

**Go best practices applied:**
- `errors.Join` for aggregating shutdown errors (Go 1.20+)
- Caller supplies `serviceName`/`serviceVersion` — no package-level globals
- `Metrics` struct passes instruments explicitly, avoiding global OTel meter lookups in hot paths
- All metric instruments use `metric.WithDescription()` and `metric.WithUnit()` for self-documenting metrics

**File: `internal/observability/otel_test.go`**

| Test case | What it verifies |
|---|---|
| `Test_SetupOTelSDK_success` | Calls `SetupOTelSDK`, verifies no error, calls `shutdown(ctx)`, verifies no error |
| `Test_SetupOTelSDK_shutdownIdempotent` | Verifies shutdown is idempotent (call twice, no panic) |
| `Test_NewLogger_writesToStdout` | Redirect stdout, call `logger.Info("test")`, verify JSON output contains `"msg":"test"` |
| `Test_NewLogger_includesSource` | Verify JSON output contains `"source"` key |
| `Test_NewMetrics_allInstrumentsNonNil` | Call `NewMetrics()`, verify no error, verify all instrument fields are non-nil |

---

### Phase 3 — Storage (SQLite)

**File: `internal/storage/storage.go`**

```go
// Reading is a 1-minute averaged data point persisted to the database.
type Reading struct {
    ID           int64
    Timestamp    time.Time   // Rounded to the minute
    PVWatts      float64     // Average PV generation over the minute
    GridWatts    float64     // Average grid power (signed)
    LoadWatts    float64     // Average home consumption
    SurplusWatts float64     // Average surplus
    ChargeAmps   int         // Amps being sent to car (0 if not charging)
    BatteryPct   float64     // Car battery % at end of minute
    State        string      // Controller state at end of minute
}

// ChargeSession records a contiguous charging period.
type ChargeSession struct {
    ID           int64
    StartTime    time.Time
    EndTime      time.Time   // Zero if still charging
    StartBattery float64
    EndBattery   float64
    EnergyKWh    float64     // Estimated: sum(amps * voltage * duration) across readings
    PeakAmps     int
    AvgAmps      float64
}

// Event records a state machine transition or notable event.
type Event struct {
    ID        int64
    Timestamp time.Time
    Type      string   // "state_change", "command", "error", "mode_change"
    Message   string   // Human-readable description
    Details   string   // JSON blob with structured data (optional)
}

// ReadingFilter for querying historical readings.
type ReadingFilter struct {
    From     time.Time
    To       time.Time
    Interval string     // "minute", "hour", "day" — controls GROUP BY aggregation
    Limit    int
    Offset   int
}

// Store persists and queries historical solar/charging data.
type Store interface {
    // Schema
    Migrate(ctx context.Context) error

    // Readings
    InsertReading(ctx context.Context, r Reading) error
    QueryReadings(ctx context.Context, f ReadingFilter) ([]Reading, error)

    // Charge sessions
    StartSession(ctx context.Context, s ChargeSession) (int64, error)
    EndSession(ctx context.Context, id int64, endTime time.Time, endBattery, energyKWh float64) error
    QuerySessions(ctx context.Context, from, to time.Time, limit, offset int) ([]ChargeSession, error)

    // Events
    InsertEvent(ctx context.Context, e Event) error
    QueryEvents(ctx context.Context, from, to time.Time, eventType string, limit, offset int) ([]Event, error)

    // Search — full-text search across events
    Search(ctx context.Context, query string, from, to time.Time, limit int) ([]Event, error)

    // Tesla API usage
    InsertAPIUsage(ctx context.Context, s APIUsageSnapshot) error
    QueryAPIUsage(ctx context.Context, from, to time.Time, limit int) ([]APIUsageSnapshot, error)

    // Maintenance
    Prune(ctx context.Context, olderThan time.Duration) (int64, error)  // returns rows deleted

    Close() error
}
```

**File: `internal/storage/sqlite.go`**

```go
type SQLiteStore struct {
    db     *sql.DB
    logger *slog.Logger
}

func NewSQLiteStore(dbPath string, logger *slog.Logger) (*SQLiteStore, error)
    // 1. Open database: sql.Open("sqlite", dbPath)
    // 2. Set pragmas for performance:
    //    PRAGMA journal_mode=WAL;       -- concurrent reads while writing
    //    PRAGMA synchronous=NORMAL;     -- balanced durability/speed
    //    PRAGMA busy_timeout=5000;      -- 5s retry on lock
    //    PRAGMA cache_size=-64000;      -- 64MB cache
    //    PRAGMA foreign_keys=ON;
    // 3. Return &SQLiteStore{db, logger}

func (s *SQLiteStore) Migrate(ctx context.Context) error
    // CREATE TABLE IF NOT EXISTS readings (
    //     id            INTEGER PRIMARY KEY AUTOINCREMENT,
    //     timestamp     DATETIME NOT NULL,
    //     pv_watts      REAL NOT NULL,
    //     grid_watts    REAL NOT NULL,
    //     load_watts    REAL NOT NULL,
    //     surplus_watts REAL NOT NULL,
    //     charge_amps   INTEGER NOT NULL DEFAULT 0,
    //     battery_pct   REAL NOT NULL DEFAULT 0,
    //     state         TEXT NOT NULL DEFAULT ''
    // );
    // CREATE INDEX IF NOT EXISTS idx_readings_timestamp ON readings(timestamp);
    //
    // CREATE TABLE IF NOT EXISTS charge_sessions (
    //     id            INTEGER PRIMARY KEY AUTOINCREMENT,
    //     start_time    DATETIME NOT NULL,
    //     end_time      DATETIME,
    //     start_battery REAL NOT NULL DEFAULT 0,
    //     end_battery   REAL NOT NULL DEFAULT 0,
    //     energy_kwh    REAL NOT NULL DEFAULT 0,
    //     peak_amps     INTEGER NOT NULL DEFAULT 0,
    //     avg_amps      REAL NOT NULL DEFAULT 0
    // );
    // CREATE INDEX IF NOT EXISTS idx_sessions_start ON charge_sessions(start_time);
    //
    // CREATE TABLE IF NOT EXISTS events (
    //     id        INTEGER PRIMARY KEY AUTOINCREMENT,
    //     timestamp DATETIME NOT NULL,
    //     type      TEXT NOT NULL,
    //     message   TEXT NOT NULL,
    //     details   TEXT NOT NULL DEFAULT ''
    // );
    // CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
    // CREATE INDEX IF NOT EXISTS idx_events_type ON events(type);
    //
    // -- FTS5 virtual table for full-text search across events
    // CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
    //     message, details, content=events, content_rowid=id
    // );
    //
    // -- Triggers to keep FTS in sync
    // CREATE TRIGGER IF NOT EXISTS events_ai AFTER INSERT ON events BEGIN
    //     INSERT INTO events_fts(rowid, message, details) VALUES (new.id, new.message, new.details);
    // END;
    // CREATE TRIGGER IF NOT EXISTS events_ad AFTER DELETE ON events BEGIN
    //     INSERT INTO events_fts(events_fts, rowid, message, details) VALUES ('delete', old.id, old.message, old.details);
    // END;

func (s *SQLiteStore) QueryReadings(ctx context.Context, f ReadingFilter) ([]Reading, error)
    // If f.Interval == "hour":
    //   SELECT strftime('%Y-%m-%d %H:00:00', timestamp) as ts,
    //          AVG(pv_watts), AVG(grid_watts), AVG(load_watts), AVG(surplus_watts),
    //          ROUND(AVG(charge_amps)), MAX(battery_pct), state
    //   FROM readings WHERE timestamp BETWEEN ? AND ?
    //   GROUP BY ts ORDER BY ts DESC LIMIT ? OFFSET ?
    //
    // If f.Interval == "day":
    //   Same with strftime('%Y-%m-%d', timestamp)
    //
    // Default ("minute"):
    //   SELECT * FROM readings WHERE timestamp BETWEEN ? AND ?
    //   ORDER BY timestamp DESC LIMIT ? OFFSET ?

func (s *SQLiteStore) Search(ctx context.Context, query string, from, to time.Time, limit int) ([]Event, error)
    // SELECT e.* FROM events e
    // JOIN events_fts ON events_fts.rowid = e.id
    // WHERE events_fts MATCH ?
    //   AND e.timestamp BETWEEN ? AND ?
    // ORDER BY e.timestamp DESC LIMIT ?
    //
    // Note: sanitize query input — FTS5 MATCH syntax.
    // Escape special chars: strip quotes, *, etc. to prevent FTS injection.

func (s *SQLiteStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error)
    // cutoff = time.Now().Add(-olderThan)
    // DELETE FROM readings WHERE timestamp < ?
    // DELETE FROM charge_sessions WHERE end_time IS NOT NULL AND end_time < ?
    // DELETE FROM events WHERE timestamp < ?
    // -- Rebuild FTS index after delete
    // INSERT INTO events_fts(events_fts) VALUES('rebuild');
    // Return total rows deleted
```

**File: `internal/storage/sqlite_test.go`**

All tests use a temporary in-memory database or `t.TempDir()` file. Use `t.Cleanup()` to close the database after each test. Test naming follows `Test_functionName_scenario` convention.

| Test | What it verifies |
|---|---|
| `Test_Migrate_createsTablesIdempotently` | `Migrate()` succeeds, tables exist, calling twice is idempotent |
| `Test_InsertReading_queryReadings` | Insert 10 readings, query with match filter → returns correct rows |
| `Test_QueryReadings_hourlyAggregation` | Insert 60 minute-readings across 1 hour, query `interval="hour"` → 1 row with averages |
| `Test_QueryReadings_dailyAggregation` | Insert readings across 3 days, query `interval="day"` → 3 rows |
| `Test_QueryReadings_pagination` | Insert 50 rows, query `limit=10, offset=0` → 10 rows; `offset=10` → next 10 |
| `Test_QueryReadings_emptyRange` | Query date range with no data → empty slice (not nil) |
| `Test_StartSession_endSession` | Start session, verify ID returned; end session, verify fields updated |
| `Test_QuerySessions_dateFilter` | Insert sessions across dates, query range → correct subset |
| `Test_InsertEvent_queryEvents` | Insert events, query back → matches |
| `Test_Search_ftsMatch` | Insert events with varied messages, search "surplus" → only matching events |
| `Test_Search_noResults` | Search for nonexistent term → empty slice |
| `Test_Search_specialChars` | Search with `*"()` chars → no error (sanitized), empty or partial results |
| `Test_Prune_deletesOldRecords` | Insert records with timestamps 2 years ago + today; prune 365 days → old deleted, today kept |
| `Test_Prune_returnsCount` | Verify returned count matches actual deletions |
| `Test_ConcurrentWrites` | 10 goroutines inserting simultaneously → no `database is locked` errors (WAL mode) |
| `Test_Close_operationsReturnError` | After `Close()`, operations return error |

---

### Phase 4 — Sungrow WiNet-S Client

**File: `internal/inverter/inverter.go`**

```go
// PowerData holds a point-in-time reading from the solar inverter.
type PowerData struct {
    PVWatts      float64   // Current PV generation (W), always >= 0
    GridWatts    float64   // Grid power (W): positive = importing, negative = exporting
    LoadWatts    float64   // Home consumption (W), always >= 0
    SurplusWatts float64   // Available surplus (W) = max(0, -GridWatts)
    Timestamp    time.Time
}

// InverterReader reads real-time power data from a solar inverter.
type InverterReader interface {
    Connect(ctx context.Context) error
    GetPowerData(ctx context.Context) (PowerData, error)
    Close() error
}
```

**File: `internal/inverter/sungrow.go`**

```go
// SungrowClient communicates with a Sungrow inverter via the WiNet-S dongle.
// Protocol: WebSocket on port 8082 for session token, HTTP GET for register reads.
type SungrowClient struct {
    host       string
    port       int
    token      string          // Session token from WebSocket connect
    wsConn     *websocket.Conn
    httpClient *http.Client
    logger     *slog.Logger
    metrics    *Metrics        // OTel metrics (optional, nil-safe)
    mu         sync.Mutex      // Protects token and wsConn
}

func New(host string, port int, logger *slog.Logger, metrics *Metrics) *SungrowClient

// Connect establishes a WebSocket connection and obtains a session token.
func (c *SungrowClient) Connect(ctx context.Context) error
    // PSEUDO CODE:
    // 1. Dial ws://{host}:{port}/ws/home/overview
    // 2. Send JSON: {"lang":"en_us","token":"","service":"connect"}
    // 3. Read response JSON
    // 4. Extract "token" field → store in c.token
    // 5. If error → return fmt.Errorf("sungrow: connect: %w", err)

// GetPowerData reads Modbus registers via the WiNet-S HTTP API.
func (c *SungrowClient) GetPowerData(ctx context.Context) (PowerData, error)
    // PSEUDO CODE:
    // 1. Read register 5031 (total_active_power, U32, W) → pvWatts
    //    GET http://{host}/device/getParam?dev_id=1&dev_type=0&dev_code=0
    //        &type=3&param_addr=5031&param_num=2&param_type=0
    //        &token={token}&lang=en_us&time123456={unix_ts}
    //
    // 2. Read register 5083 (meter_power, S32, W) → gridWatts
    //    Same pattern, param_addr=5083, param_num=2
    //
    // 3. Read register 5091 (load_power, S32, W) → loadWatts
    //    Same pattern, param_addr=5091, param_num=2
    //
    // 4. Check response result_code:
    //    If result_code == 106 (token expired):
    //        c.Connect(ctx)  // re-establish session
    //        retry the read (once only — avoid infinite loop)
    //
    // 5. Parse register values:
    //    U32: (high_word << 16) | low_word
    //    S32: interpret as int32 after combining words
    //
    // 6. Calculate surplus:
    //    surplusWatts = max(0.0, -gridWatts)
    //
    // 7. Record OTel metrics:
    //    c.metrics.PVPower.Record(ctx, pvWatts)
    //    c.metrics.GridPower.Record(ctx, gridWatts)
    //    c.metrics.LoadPower.Record(ctx, loadWatts)
    //    c.metrics.SurplusPower.Record(ctx, surplusWatts)
    //
    // 8. Return PowerData{pvWatts, gridWatts, loadWatts, surplusWatts, time.Now()}

// readRegister performs a single HTTP GET to read Modbus registers.
func (c *SungrowClient) readRegister(ctx context.Context, addr, count int, signed bool) (float64, error)
    // Build URL, execute GET, parse JSON response, extract param_value
    // If signed: interpret as int32
    // If unsigned: interpret as uint32

func (c *SungrowClient) Close() error
    // Close WebSocket connection
```

**Go best practices applied:**
- **HTTP client discipline** — `SungrowClient` holds `*http.Client` as a dependency; each `readRegister` call builds a fresh `*http.Request` with `http.NewRequestWithContext`; `defer resp.Body.Close()` on every response
- Mutex protects shared state (`token`, `wsConn`) — not the entire struct; critical section kept small
- `fmt.Errorf` with `%w` for error wrapping (allows `errors.Is`/`errors.As` upstream)
- Error messages are lowercase with package prefix: `"sungrow: connect: %w"`, `"sungrow: read register %d: %w"`
- OTel metrics are nil-safe — if `metrics` is nil, skip recording (simplifies testing)
- Context propagation on all public methods for cancellation/tracing
- `InverterReader` interface is defined here but will be **consumed** by the controller package (interfaces defined close to use)

**File: `internal/inverter/sungrow_test.go`**

All tests use `httptest.NewServer` to mock the WiNet-S HTTP API. WebSocket connect is tested with `nhooyr.io/websocket` test helpers. Use `t.Cleanup()` to shut down test servers.

| Test case | Mock behavior | Assertion |
|---|---|---|
| `Test_GetPowerData_normal` | Returns valid register JSON for 5031=5000, 5083=-2000, 5091=3000 | PV=5000, Grid=-2000, Load=3000, Surplus=2000 |
| `Test_GetPowerData_importing` | 5083=+1500 (positive = importing) | Surplus=0 |
| `Test_GetPowerData_zeroPV` | 5031=0, 5083=0, 5091=0 | All zeros |
| `Test_GetPowerData_largeExport` | 5083=-8000 | Surplus=8000 |
| `Test_GetPowerData_tokenExpiry` | First call returns result_code=106, second succeeds | Reconnects, retries, returns valid data |
| `Test_GetPowerData_tokenExpiryReconnectFails` | result_code=106, reconnect also fails | Returns error |
| `Test_GetPowerData_malformedJSON` | Returns `{bad json` | Returns error |
| `Test_GetPowerData_httpError` | Returns 500 | Returns error |
| `Test_Connect_success` | WebSocket echo valid token JSON | `c.token` is set |
| `Test_Connect_webSocketRefused` | No server listening | Returns error with connect context |
| `Test_Connect_invalidResponse` | WebSocket returns non-JSON | Returns error |
| `Test_Close_subsequentCallsFail` | After Connect | No error; subsequent GetPowerData returns error |

---

### Phase 5 — Tesla Fleet API Client

**File: `internal/tesla/tesla.go`**

```go
// Sentinel errors for known vehicle states.
var (
    ErrCarOffline   = errors.New("tesla: vehicle is offline")
    ErrNotPluggedIn = errors.New("tesla: vehicle is not plugged in")
    ErrNotCharging  = errors.New("tesla: vehicle is not charging")
)

// ChargeState holds the vehicle's current charging information.
type ChargeState struct {
    State       string  // "Charging", "Complete", "Disconnected", "Stopped", "NoPower"
    AmpsActual  int     // Current charge rate in amps
    BatteryPct  float64 // State of charge 0-100
    PluggedIn   bool    // Whether charge cable is connected
    IsOnline    bool    // Whether vehicle is awake/online
}

// VehicleController controls EV charging.
type VehicleController interface {
    GetChargeState(ctx context.Context) (ChargeState, error)
    SetChargingAmps(ctx context.Context, amps int) error
    StartCharging(ctx context.Context) error
    StopCharging(ctx context.Context) error
    WakeUp(ctx context.Context) error
    GetAPIUsage() APIUsage
}
```

**File: `internal/tesla/client.go`**

```go
// TeslaClient implements VehicleController using the Tesla Fleet API.
// Commands are signed with the Virtual Key via the vehicle-command SDK.
//
// Per Go HTTP client best practices: the struct holds only configuration
// and long-lived dependencies. No *http.Request or per-request state is
// stored on the struct. Each method builds a fresh request per call.
type TeslaClient struct {
    httpClient   *http.Client        // configured once (timeouts, transport); safe for concurrent use
    baseURL      string              // e.g. "https://fleet-api.prd.na.vn.cloud.tesla.com"
    authURL      string              // "https://fleet-auth.prd.vn.cloud.tesla.com"
    vin          string
    clientID     string
    clientSecret string
    privateKey   *ecdsa.PrivateKey   // Virtual Key for command signing
    logger       *slog.Logger
    metrics      *Metrics

    mu           sync.Mutex          // protects token fields below
    accessToken  string              // current OAuth2 access token
    refreshToken string              // long-lived refresh token (rotated on each refresh)
    tokenExpiry  time.Time
}

func New(cfg config.Config, logger *slog.Logger, metrics *Metrics) (*TeslaClient, error)
    // 1. Load EC private key from cfg.TeslaPrivateKeyPath
    // 2. Map cfg.TeslaRegion to base URL
    // 3. Initialize with refresh token from config
    // 4. Perform initial token refresh to validate credentials

// PSEUDO CODE for token refresh:
func (c *TeslaClient) refreshAccessToken(ctx context.Context) error
    // POST {authURL}/oauth2/v3/token
    // Body: grant_type=refresh_token&client_id={id}&client_secret={secret}&refresh_token={token}
    // Parse response: access_token, refresh_token (new single-use), expires_in
    // c.mu.Lock(); c.accessToken = new; c.refreshToken = newRefresh; c.tokenExpiry = now + expires_in; c.mu.Unlock()
    // Log new refresh_token at INFO level (user needs it for env var update)

// PSEUDO CODE for authenticated request:
func (c *TeslaClient) doRequest(ctx context.Context, method, path string, body any) (*http.Response, error)
    // If time.Now().After(c.tokenExpiry - 60s):
    //     refreshAccessToken(ctx)
    //
    // Build request body (if body != nil):
    //     payload, _ = json.Marshal(body)
    //
    // Build fresh *http.Request per call (never reuse or cache requests):
    //     req, err = http.NewRequestWithContext(ctx, method, c.baseURL+path,
    //                    bytes.NewReader(payload))
    //     req.Header.Set("Content-Type", "application/json")
    //     req.Header.Set("Authorization", "Bearer "+c.accessToken)
    //
    // Set GetBody for redirect/retry body reuse:
    //     req.GetBody = func() (io.ReadCloser, error) {
    //         return io.NopCloser(bytes.NewReader(payload)), nil
    //     }
    //
    // Execute request:
    //     resp, err = c.httpClient.Do(req)
    //     if err != nil: return nil, fmt.Errorf("tesla: %s %s: %w", method, path, err)
    //
    // IMPORTANT: caller must defer resp.Body.Close()
    //
    // If 401 (token expired mid-flight):
    //     resp.Body.Close()
    //     refreshAccessToken(ctx)
    //     // rebuild request (fresh req, not reused) and retry once
    //     req, _ = http.NewRequestWithContext(ctx, method, c.baseURL+path,
    //                 bytes.NewReader(payload))
    //     req.Header.Set("Authorization", "Bearer "+c.accessToken)
    //     req.GetBody = func() (io.ReadCloser, error) {
    //         return io.NopCloser(bytes.NewReader(payload)), nil
    //     }
    //     resp, err = c.httpClient.Do(req)
    //
    // If 408/429/5xx:
    //     resp.Body.Close()
    //     return nil, fmt.Errorf("tesla: %s %s: status %d", method, path, resp.StatusCode)

func (c *TeslaClient) GetChargeState(ctx context.Context) (ChargeState, error)
    // GET /api/1/vehicles/{vin}/vehicle_data?endpoints=charge_state
    // Parse response.charge_state fields
    // Map to ChargeState struct
    // Record OTel metrics: c.metrics.ChargeAmps.Record(ctx, state.AmpsActual)
    //                      c.metrics.BatteryPct.Record(ctx, state.BatteryPct)

func (c *TeslaClient) SetChargingAmps(ctx context.Context, amps int) error
    // Clamp: amps = max(MIN_CHARGE_AMPS, min(MAX_CHARGE_AMPS, amps))
    // POST /api/1/vehicles/{vin}/command/set_charging_amps
    // Body: {"charging_amps": amps}
    // Sign with Virtual Key via vehicle-command SDK
    // Counter: c.metrics.ChargeCommands.Add(ctx, 1, attribute.String("command", "set_amps"))

func (c *TeslaClient) StartCharging(ctx context.Context) error
    // POST /api/1/vehicles/{vin}/command/charge_start
    // Sign with Virtual Key
    // Counter: c.metrics.ChargeCommands.Add(ctx, 1, attribute.String("command", "start"))

func (c *TeslaClient) StopCharging(ctx context.Context) error
    // POST /api/1/vehicles/{vin}/command/charge_stop
    // Sign with Virtual Key
    // Counter: c.metrics.ChargeCommands.Add(ctx, 1, attribute.String("command", "stop"))

func (c *TeslaClient) WakeUp(ctx context.Context) error
    // POST /api/1/vehicles/{vin}/wake_up
    // Poll GetChargeState every 2s up to 30s until IsOnline == true
    // If timeout → return fmt.Errorf("tesla: wake_up timed out after 30s")
```

**Go best practices applied:**
- **HTTP client discipline** \u2014 `TeslaClient` struct holds only config + dependencies; no `*http.Request` cached on the struct; each method builds a fresh request; `req.GetBody` set for retry body reuse; `defer resp.Body.Close()` in every caller
- Sentinel errors (`ErrCarOffline`, etc.) allow callers to use `errors.Is(err, tesla.ErrCarOffline)` for control flow
- Error messages are lowercase with no trailing punctuation (e.g. `"tesla: vehicle is offline"`)
- Mutex only protects token state, not HTTP calls \u2014 critical section is minimal
- Context passed through for cancellation/timeout propagation
- `%w` wrapping for all errors to preserve the error chain
- Accept interfaces, return concrete types \u2014 `VehicleController` interface defined where it is **consumed** (controller package), not here

**File: `internal/tesla/client_test.go`**

Uses `httptest.NewServer` for both Fleet API and auth endpoints. Use `t.Cleanup()` to shut down test servers.

| Test case | Mock behavior | Assertion |
|---|---|---|
| `Test_GetChargeState_charging` | Returns JSON: state=Charging, amps=16, battery=55 | Struct matches |
| `Test_GetChargeState_disconnected` | Returns JSON: state=Disconnected | PluggedIn=false |
| `Test_GetChargeState_vehicleOffline` | Returns 408 | `errors.Is(err, ErrCarOffline)` |
| `Test_SetChargingAmps_valid` | Returns success | Verify POST body has `charging_amps: 16` |
| `Test_SetChargingAmps_clampLow` | Call with amps=2 | POST body has `charging_amps: 5` (MIN) |
| `Test_SetChargingAmps_clampHigh` | Call with amps=50 | POST body has `charging_amps: 32` (MAX) |
| `Test_StartCharging_success` | Returns success | Verify POST to correct endpoint |
| `Test_StopCharging_success` | Returns success | Verify POST to correct endpoint |
| `Test_WakeUp_immediatelyOnline` | First vehicle_data returns online | No polling loop |
| `Test_WakeUp_awakensAfterRetry` | First 2 calls: offline, third: online | Returns nil after 3 polls |
| `Test_WakeUp_timeout` | Always returns offline | Returns timeout error |
| `Test_tokenRefresh_onExpiry` | First GET returns 401, refresh succeeds, retry succeeds | Final GET returns data |
| `Test_tokenRefresh_failure` | Refresh endpoint returns 400 | Returns auth error |
| `Test_doRequest_serverError` | Returns 500 | Returns error, no retry |

---

### Phase 6 — Control Loop & State Machine

**File: `internal/controller/controller.go`**

This is the core logic. The controller runs a background goroutine that periodically reads solar data, calculates surplus, and adjusts EV charging. It also accumulates tick samples and flushes 1-minute averages to storage.

**Go best practices applied (per `.github/copilot-instructions.md`):**
- **Concurrency**: The controller owns a single goroutine (`Run`); caller controls lifecycle via `context.Context`. All goroutine exit paths are defined (context cancellation + graceful flush).
- **Sync**: `sync.RWMutex` protects snapshot/state; readers (`StateSnapshot`) use `RLock`, writers (`transitionTo`, `Tick`) use `Lock`. Critical sections are minimal.
- **WaitGroup**: Go 1.25, so use `WaitGroup.Go` method
- **Interfaces**: `InverterReader`, `VehicleController`, and `Store` are defined close to where they are consumed (in this package or their respective packages), not in a shared `types` package
- **Error handling**: errors are wrapped with `%w` and lowercase messages; errors are logged **or** returned, never both
- **Zero values**: `Controller` zero state is idle/auto, which is a safe default

```go
// State represents the controller's current operating state.
type State string

const (
    StateIdle              State = "idle"               // Car not home or not plugged in
    StateMonitoring        State = "monitoring"          // Plugged in, watching surplus
    StateCharging          State = "charging"            // Actively charging at calculated amps
    StateStoppedLowSurplus State = "stopped_low_surplus" // Stopped due to insufficient surplus
    StateWakePending       State = "wake_pending"        // Surplus detected, waking car
    StateError             State = "error"               // Unrecoverable error
)

// Mode represents the operating mode.
type Mode string

const (
    ModeAuto   Mode = "auto"
    ModeManual Mode = "manual"
)

// StateSnapshot is a point-in-time snapshot for the web UI (thread-safe copy).
type StateSnapshot struct {
    State              State
    Mode               Mode
    PVWatts            float64
    GridWatts          float64
    LoadWatts          float64
    SurplusWatts       float64
    TargetAmps         int
    ActualAmps         int
    BatteryPct         float64
    CarPluggedIn       bool
    CarOnline          bool
    ChargingState      string
    ConsecutiveLow     int
    ConsecutiveSurplus int
    LastUpdate         time.Time
    LastError          string
}

type Controller struct {
    inverter     InverterReader
    vehicle      VehicleController
    store        Store              // SQLite store (nil-safe for tests without storage)
    cfg          config.Config
    logger       *slog.Logger
    metrics      *Metrics

    // Protected by mu
    mu                 sync.RWMutex
    state              State
    mode               Mode
    snapshot           StateSnapshot
    consecutiveLow     int    // ticks below MIN_CHARGE_AMPS
    consecutiveSurplus int    // ticks with viable surplus (for wake)
    lastChargeAmps     int    // last amps sent to vehicle

    // Cost-aware Tesla API polling
    lastTeslaPoll      time.Time  // when Tesla API was last called
    cachedChargeState  ChargeState
    hasCachedState     bool

    // 1-minute averaging for DB persistence
    accumulator        []accSample
    currentMinute      time.Time
    activeSessionID    int64

    // SSE notification
    onUpdate func(StateSnapshot)
}

type accSample struct {
    pvWatts, gridWatts, loadWatts, surplusWatts float64
    chargeAmps                                   int
    batteryPct                                   float64
    state                                        State
}

func New(inverter InverterReader, vehicle VehicleController, store Store,
         cfg config.Config, logger *slog.Logger, metrics *Metrics) *Controller
```

#### Critical Control Loop — `Tick(ctx)`

```
func (c *Controller) Tick(ctx context.Context)
    // --- START OTel span + timing ---
    // ctx, span = tracer.Start(ctx, "controller.Tick")
    // defer span.End()
    // start = time.Now()
    // defer func() { c.metrics.PollDuration.Record(ctx, time.Since(start).Seconds()) }()

    // --- STEP 1: Read solar data ---
    // power, err = c.inverter.GetPowerData(ctx)
    // if err != nil:
    //     c.logger.ErrorContext(ctx, "inverter read failed", "error", err)
    //     c.transitionTo(ctx, StateError, err.Error())
    //     return

    // --- STEP 2: Read vehicle state (cost-aware) ---
    // Cost-aware polling: only call Tesla API when shouldPollTesla() returns true.
    // Between API calls, use cached charge state. The inverter (local/free) is
    // always polled, but Tesla Fleet API calls ($0.002 each) are gated:
    //   - No cached state → poll immediately
    //   - Charging → poll every TeslaChargingPollInterval (default 60s)
    //   - Idle with surplus ≥ min amps → poll every TeslaIdlePollInterval (default 300s)
    //   - Idle without surplus → skip (no reason to call Tesla)
    //   - Wake pending → skip (wake command handles its own polling)
    //
    // if c.shouldPollTesla(availableAmps):
    //     chargeState, err = c.vehicle.GetChargeState(ctx)
    //     if errors.Is(err, tesla.ErrCarOffline):
    //         chargeState = ChargeState{IsOnline: false}
    //     else if err != nil:
    //         c.logger.ErrorContext(ctx, "tesla read failed", "error", err)
    //         c.transitionTo(ctx, StateError, err.Error())
    //         return
    //     c.setCachedChargeState(chargeState)
    // else:
    //     chargeState = c.getCachedChargeState()

    // --- STEP 3: Check preconditions ---
    // if !chargeState.PluggedIn && chargeState.IsOnline:
    //     c.transitionTo(ctx, StateIdle, "car not plugged in")
    //     c.resetCounters()
    //     return

    // --- STEP 4: If in manual mode, skip auto-control ---
    // if c.mode == ModeManual:
    //     c.updateSnapshot(power, chargeState, 0)
    //     c.accumulateSample(power, chargeState, 0, ctx)
    //     return

    // --- STEP 5: Calculate available amps ---
    // availableAmps = c.calculateAvailableAmps(power.SurplusWatts, chargeState)

    // --- STEP 6: State machine decision ---
    // if availableAmps >= MIN_CHARGE_AMPS:
    //     c.consecutiveLow = 0
    //     c.consecutiveSurplus++
    //
    //     if !chargeState.IsOnline:
    //         // Car is asleep
    //         if c.consecutiveSurplus >= WAKE_THRESHOLD_POLLS:
    //             c.transitionTo(ctx, StateWakePending, "waking car")
    //             err = c.vehicle.WakeUp(ctx)
    //             if err != nil:
    //                 c.logger.WarnContext(ctx, "wake failed", "error", err)
    //                 c.transitionTo(ctx, StateError, err.Error())
    //             // Next tick will find car online
    //         else:
    //             c.transitionTo(ctx, StateMonitoring, "surplus detected, waiting to wake")
    //         return
    //
    //     if chargeState.State != "Charging":
    //         // Car is online but not charging → start it
    //         c.vehicle.SetChargingAmps(ctx, availableAmps)
    //         err = c.vehicle.StartCharging(ctx)
    //         if err != nil:
    //             c.logger.WarnContext(ctx, "start charging failed", "error", err)
    //         else:
    //             c.transitionTo(ctx, StateCharging, fmt.Sprintf("started at %dA", availableAmps))
    //             c.lastChargeAmps = availableAmps
    //         return
    //
    //     // Car IS charging — adjust amps if delta >= AmpsChangeThreshold
    //     if abs(availableAmps - c.lastChargeAmps) >= c.cfg.AmpsChangeThreshold:
    //         err = c.vehicle.SetChargingAmps(ctx, availableAmps)
    //         if err != nil:
    //             c.logger.WarnContext(ctx, "set amps failed", "error", err)
    //         else:
    //             c.lastChargeAmps = availableAmps
    //             c.logger.InfoContext(ctx, "adjusted amps", "amps", availableAmps)
    //
    // else:
    //     // Insufficient surplus
    //     c.consecutiveSurplus = 0
    //     c.consecutiveLow++
    //
    //     if chargeState.State == "Charging" && c.consecutiveLow >= DEADBAND_POLLS:
    //         err = c.vehicle.StopCharging(ctx)
    //         if err != nil:
    //             c.logger.WarnContext(ctx, "stop charging failed", "error", err)
    //         else:
    //             c.transitionTo(ctx, StateStoppedLowSurplus, "insufficient surplus")
    //             c.lastChargeAmps = 0
    //     else if chargeState.State == "Charging":
    //         c.transitionTo(ctx, StateCharging, fmt.Sprintf("low surplus tick %d/%d",
    //                        c.consecutiveLow, DEADBAND_POLLS))
    //     else:
    //         c.transitionTo(ctx, StateMonitoring, "waiting for surplus")

    // --- STEP 7: Update snapshot + accumulate for DB ---
    // c.updateSnapshot(power, chargeState, availableAmps)
    // c.accumulateSample(power, chargeState, availableAmps, ctx)

    // --- STEP 8: Notify SSE subscribers ---
    // if c.onUpdate != nil:
    //     c.onUpdate(c.snapshot)
```

#### Critical Control Loop — `calculateAvailableAmps`

```
func (c *Controller) calculateAvailableAmps(surplusWatts float64, cs ChargeState) int
    // The grid meter reads the NET power flow at the meter point.
    // When the car is charging at X amps, the meter sees X*240W LESS export
    // than the "true" surplus. We must add back the car's consumption to find
    // what the total available solar surplus really is.
    //
    // Example:
    //   PV = 6000W, Home = 1000W, Car charging at 10A (2400W)
    //   Meter reads: 6000 - 1000 - 2400 = 2600W export
    //   surplusWatts from meter = 2600W → surplusAmps = floor(2600/240) = 10
    //   True available = 10 + 10 (car's current draw) = 20A
    //   But car is already at 10A and we have 10A surplus → set to 20A
    //
    // surplusAmps = int(math.Floor(surplusWatts / float64(c.cfg.LineVoltage)))
    // if cs.State == "Charging":
    //     surplusAmps = surplusAmps + cs.AmpsActual
    // return clamp(surplusAmps, 0, c.cfg.MaxChargeAmps)
```

#### Critical Control Loop — `Run(ctx)`

```
func (c *Controller) Run(ctx context.Context)
    // ticker = time.NewTicker(c.cfg.PollInterval)
    // defer ticker.Stop()
    //
    // for {
    //     select {
    //     case <-ctx.Done():
    //         c.logger.InfoContext(ctx, "controller stopped")
    //         // If car is charging, stop it gracefully
    //         if c.state == StateCharging:
    //             c.vehicle.StopCharging(context.Background())
    //         // Flush any remaining accumulated samples
    //         c.flushMinuteAverage(ctx)
    //         return
    //     case <-ticker.C:
    //         c.Tick(ctx)
    //     }
    // }
```

#### 1-Minute Averaging for DB Persistence

```
func (c *Controller) accumulateSample(power PowerData, cs ChargeState, amps int, ctx context.Context)
    // thisMinute = time.Now().Truncate(time.Minute)
    // if c.currentMinute.IsZero():
    //     c.currentMinute = thisMinute
    //
    // if thisMinute != c.currentMinute:
    //     // Minute boundary crossed — flush accumulated samples
    //     c.flushMinuteAverage(ctx)
    //     c.currentMinute = thisMinute
    //     c.accumulator = c.accumulator[:0]
    //
    // c.accumulator = append(c.accumulator, accSample{
    //     pvWatts: power.PVWatts, gridWatts: power.GridWatts,
    //     loadWatts: power.LoadWatts, surplusWatts: power.SurplusWatts,
    //     chargeAmps: amps, batteryPct: cs.BatteryPct, state: c.state,
    // })

func (c *Controller) flushMinuteAverage(ctx context.Context)
    // if c.store == nil || len(c.accumulator) == 0:
    //     return
    //
    // n = float64(len(c.accumulator))
    // avg := Reading{
    //     Timestamp:    c.currentMinute,
    //     PVWatts:      sum(s.pvWatts) / n,
    //     GridWatts:    sum(s.gridWatts) / n,
    //     LoadWatts:    sum(s.loadWatts) / n,
    //     SurplusWatts: sum(s.surplusWatts) / n,
    //     ChargeAmps:   last(s.chargeAmps),
    //     BatteryPct:   last(s.batteryPct),
    //     State:        string(last(s.state)),
    // }
    //
    // if err := c.store.InsertReading(ctx, avg); err != nil:
    //     c.logger.ErrorContext(ctx, "failed to persist reading", "error", err)
```

#### Helper: `transitionTo`

```
func (c *Controller) transitionTo(ctx context.Context, newState State, reason string)
    // c.mu.Lock()
    // defer c.mu.Unlock()
    // if c.state == newState:
    //     return  // No-op transition
    // oldState = c.state
    // c.state = newState
    // c.logger.InfoContext(ctx, "state transition", "from", oldState, "to", newState, "reason", reason)
    // c.metrics.StateChanges.Add(ctx, 1,
    //     attribute.String("from", string(oldState)),
    //     attribute.String("to", string(newState)))
    //
    // Persist event to storage:
    // if c.store != nil:
    //     c.store.InsertEvent(ctx, Event{
    //         Timestamp: time.Now(),
    //         Type:      "state_change",
    //         Message:   fmt.Sprintf("%s → %s", oldState, newState),
    //         Details:   fmt.Sprintf(`{"reason":%q}`, reason),
    //     })
    //
    // Track charge sessions:
    // if newState == StateCharging && oldState != StateCharging:
    //     id, _ = c.store.StartSession(ctx, ChargeSession{
    //         StartTime: time.Now(), StartBattery: c.snapshot.BatteryPct})
    //     c.activeSessionID = id
    //
    // if oldState == StateCharging && newState != StateCharging && c.activeSessionID != 0:
    //     c.store.EndSession(ctx, c.activeSessionID, time.Now(),
    //         c.snapshot.BatteryPct, c.estimateSessionEnergy())
    //     c.activeSessionID = 0
```

#### Manual overrides

```
func (c *Controller) SetMode(mode Mode)
func (c *Controller) ManualSetAmps(ctx context.Context, amps int) error
    // Only works in ModeManual
    // Calls c.vehicle.SetChargingAmps(ctx, amps) directly
func (c *Controller) ManualStart(ctx context.Context) error
func (c *Controller) ManualStop(ctx context.Context) error
func (c *Controller) StateSnapshot() StateSnapshot
    // c.mu.RLock()
    // defer c.mu.RUnlock()
    // return c.snapshot  (value copy — safe)
```

**File: `internal/controller/controller_test.go`**

Uses mock implementations of `InverterReader`, `VehicleController`, and `Store`.

**Testing conventions (per `.github/copilot-instructions.md`):**
- Test naming: `Test_functionName_scenario` (e.g. `Test_Tick_highSurplusStartsCharging`)
- All mock setup helpers marked with `t.Helper()` so failures report at the caller's line
- Use `t.Cleanup()` for resource teardown instead of manual defers in setup functions
- Table-driven tests with `t.Run` subtests for clear output
- Run all tests with `-race` flag to detect data races
- Use `_test` package suffix for black-box testing where appropriate

```go
// mockInverter implements InverterReader with configurable responses.
// Used in controller tests — defined close to where it's used (not exported).
type mockInverter struct {
    power PowerData
    err   error
}
func (m *mockInverter) Connect(ctx context.Context) error                    { return nil }
func (m *mockInverter) GetPowerData(ctx context.Context) (PowerData, error)  { return m.power, m.err }
func (m *mockInverter) Close() error                                         { return nil }

// mockVehicle implements VehicleController, recording all calls.
type mockVehicle struct {
    mu           sync.Mutex
    chargeState  ChargeState
    stateErr     error
    calls        []string       // e.g. ["SetChargingAmps:16", "StartCharging", "StopCharging"]
    setAmpsErr   error
    startErr     error
    stopErr      error
    wakeErr      error
}

// mockStore implements Store, recording all inserts.
type mockStore struct {
    mu       sync.Mutex
    readings []Reading
    events   []Event
    sessions []ChargeSession
}
```

**State machine transition tests (table-driven):**

All subtests run via `t.Run` with descriptive names following `Test_Tick_scenario` convention:

| Test name | Initial state | Mock inverter (surplusW) | Mock vehicle state | Expected transitions/calls |
|---|---|---|---|---|
| `Test_Tick_idleCarNotPluggedHighSurplus` | idle | 5000 | online, not plugged in | stays idle, no commands |
| `Test_Tick_idleCarPluggedHighSurplus` | idle | 2400 | online, plugged in, stopped | → charging, calls: SetChargingAmps(10), StartCharging |
| `Test_Tick_idleCarAsleepHighSurplusBelowWake` | idle | 2400 | offline | → monitoring (consecutiveSurplus=1, below threshold) |
| `Test_Tick_idleCarAsleepSurplusSustained` | idle | 2400 | offline, run 6 ticks | → wake_pending, calls: WakeUp |
| `Test_Tick_chargingSurplusIncreases` | charging@10A | 3600 | charging@10A | calls: SetChargingAmps(25) (surplus 15A + car 10A) |
| `Test_Tick_chargingSurplusDecreases` | charging@10A | 720 | charging@10A | calls: SetChargingAmps(13) (surplus 3A + car 10A) |
| `Test_Tick_chargingSurplusDropsOneTickDeadband` | charging@10A | 0 | charging@10A | no StopCharging yet (consecutiveLow=1) |
| `Test_Tick_chargingSurplusDropsDeadbandExpires` | charging, 3 consecutive 0W | charging@10A | calls: StopCharging → stopped_low_surplus |
| `Test_Tick_stoppedLowSurplusRecovers` | stopped_low | 2400 | online, plugged in, stopped | → charging, calls: SetChargingAmps(10), StartCharging |
| `Test_Tick_chargingCarDisconnected` | charging | 5000 | online, not plugged in | → idle |
| `Test_Tick_errorInverterRecovers` | error (inverter was failing) | 2400 (now working) | online, plugged in | → charging |
| `Test_Tick_manualNoAutoControl` | any, manual mode | 5000 | charging@10A | no SetChargingAmps/Start/Stop calls |

**Surplus amps calculation tests (table-driven):**

Tests follow `Test_calculateAvailableAmps` with `t.Run` subtests:

| Surplus (W) | Car charging | Car amps | LINE_VOLTAGE | Expected available amps |
|---|---|---|---|---|
| 2400 | no | 0 | 240 | 10 |
| 2400 | yes | 10 | 240 | 20 |
| 1199 | no | 0 | 240 | 4 → below MIN(5), should stop |
| 1200 | no | 0 | 240 | 5 → exactly MIN, charge at 5 |
| 7680 | no | 0 | 240 | 32 → at MAX |
| 9600 | no | 0 | 240 | 32 → clamped to MAX |
| 0 | no | 0 | 240 | 0 → stop |
| 0 | yes | 10 | 240 | 10 → keep current (car IS the surplus consumer) |
| -500 | yes | 10 | 240 | 8 → reduce (importing 500W = ~2A) |

**Storage integration tests:**

| Test | What it verifies |
|---|---|
| `Test_Tick_flushesMinuteAverage` | Run 7 ticks (>1 fake minute), verify `InsertReading` called on mock store with averaged values |
| `Test_Tick_noFlushWithinSameMinute` | Run 5 ticks within same minute → `InsertReading` not called |
| `Test_transitionTo_persistsEvent` | Trigger state change → verify `InsertEvent` called on mock store |
| `Test_transitionTo_startsChargeSession` | Transition to `StateCharging` → `StartSession` called |
| `Test_transitionTo_endsChargeSession` | Transition from `StateCharging` → `EndSession` called with correct ID |
| `Test_Tick_nilStoreNoPanic` | Controller with `store=nil` → no panics, no errors (nil-safe) |

**Concurrency test:**
- `Test_Tick_concurrentSnapshotAccess` — Run `Tick` in a goroutine while calling `StateSnapshot()` from another — verify no race (run with `-race` flag)

---

### Phase 7 — Web UI & SSE

**File: `internal/web/server.go`**

```go
func NewServer(ctrl *controller.Controller, store storage.Store, logger *slog.Logger) http.Handler
    // mux = http.NewServeMux()
    // mux.HandleFunc("GET /", handleIndex)
    // mux.HandleFunc("GET /api/state", handleState(ctrl))
    // mux.HandleFunc("GET /events", handleSSE(hub))
    // mux.HandleFunc("POST /api/control", handleControl(ctrl))
    // mux.HandleFunc("POST /api/mode", handleMode(ctrl))
    // mux.HandleFunc("GET /api/history", handleHistory(store))
    // mux.HandleFunc("GET /api/sessions", handleSessions(store))
    // mux.HandleFunc("GET /api/events", handleEvents(store))
    // mux.HandleFunc("GET /api/search", handleSearch(store))
    // mux.HandleFunc("GET /api/usage", handleAPIUsage(ctrl))
    // mux.HandleFunc("GET /api/usage/history", handleAPIUsageHistory(store))
    // mux.HandleFunc("GET /auth/tesla", handleTeslaAuth)
    // mux.HandleFunc("GET /auth/tesla/callback", handleTeslaCallback)
    // mux.HandleFunc("GET /healthz", handleHealthz)
    //
    // Wrap with OTel HTTP middleware:
    // return otelhttp.NewHandler(mux, "solar-ev-charger")
```

**File: `internal/web/sse.go`**

```go
// Hub manages SSE client connections and broadcasts state updates.
type Hub struct {
    mu          sync.RWMutex
    clients     map[chan StateSnapshot]struct{}
    logger      *slog.Logger
}

func NewHub(logger *slog.Logger) *Hub

func (h *Hub) Subscribe() chan StateSnapshot
    // Create buffered channel (cap=10 to avoid blocking on slow clients)
    // h.mu.Lock(); h.clients[ch] = struct{}{}; h.mu.Unlock()
    // return ch

func (h *Hub) Unsubscribe(ch chan StateSnapshot)
    // h.mu.Lock(); delete(h.clients, ch); close(ch); h.mu.Unlock()

func (h *Hub) Broadcast(snap StateSnapshot)
    // h.mu.RLock(); defer h.mu.RUnlock()
    // for ch := range h.clients:
    //     select {
    //     case ch <- snap:     // deliver
    //     default:             // slow client — drop event (don't block)
    //     }

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request)
    // Set headers: Content-Type: text/event-stream, Cache-Control: no-cache, Connection: keep-alive
    // flusher, ok = w.(http.Flusher)
    // ch = h.Subscribe()
    // defer h.Unsubscribe(ch)
    //
    // for {
    //     select {
    //     case <-r.Context().Done():
    //         return
    //     case snap := <-ch:
    //         data, _ = json.Marshal(snap)
    //         fmt.Fprintf(w, "data: %s\n\n", data)
    //         flusher.Flush()
    //     }
    // }
```

**Web API Endpoints:**

| Method | Path | Query params | Response |
|---|---|---|---|
| `GET` | `/` | — | HTML dashboard |
| `GET` | `/api/state` | — | JSON `StateSnapshot` |
| `GET` | `/events` | — | SSE stream |
| `POST` | `/api/control` | — | `{"action":"start"|"stop"|"setAmps","amps":N}` |
| `POST` | `/api/mode` | — | `{"mode":"auto"|"manual"}` |
| `GET` | `/api/history` | `from`, `to` (RFC3339), `interval` (minute/hour/day), `limit`, `offset` | `[]Reading` JSON |
| `GET` | `/api/sessions` | `from`, `to`, `limit`, `offset` | `[]ChargeSession` JSON |
| `GET` | `/api/events` | `from`, `to`, `type`, `limit`, `offset` | `[]Event` JSON |
| `GET` | `/api/search` | `q`, `from`, `to`, `limit` | `[]Event` JSON (FTS5) |
| `GET` | `/api/usage` | — | Current month Tesla API usage counters + estimated cost |
| `GET` | `/api/usage/history` | `from`, `to`, `limit` | `[]APIUsageSnapshot` JSON (historical) |
| `GET` | `/auth/tesla` | — | OAuth2 redirect |
| `GET` | `/auth/tesla/callback` | `code` | Token exchange |
| `GET` | `/healthz` | — | `{"status":"ok"}` |

**File: `internal/web/handlers_test.go`**

All tests use `httptest.NewRecorder` and `httptest.NewRequest`. Mock controller/store injected; helpers marked with `t.Helper()`.

| Test | Method/Path | Request body | Expected |
|---|---|---|---|
| `Test_handleState_returnsSnapshot` | GET /api/state | — | 200, valid JSON matching StateSnapshot fields |
| `Test_handleControl_setAmps` | POST /api/control | `{"action":"setAmps","amps":16}` | 200, mock ManualSetAmps(16) called |
| `Test_handleControl_start` | POST /api/control | `{"action":"start"}` | 200, mock ManualStart() called |
| `Test_handleControl_stop` | POST /api/control | `{"action":"stop"}` | 200, mock ManualStop() called |
| `Test_handleControl_invalidAction` | POST /api/control | `{"action":"reboot"}` | 400 |
| `Test_handleControl_badJSON` | POST /api/control | `not json` | 400 |
| `Test_handleControl_missingAmps` | POST /api/control | `{"action":"setAmps"}` | 400 |
| `Test_handleMode_auto` | POST /api/mode | `{"mode":"auto"}` | 200, mock SetMode(Auto) called |
| `Test_handleMode_manual` | POST /api/mode | `{"mode":"manual"}` | 200 |
| `Test_handleMode_invalid` | POST /api/mode | `{"mode":"turbo"}` | 400 |
| `Test_handleHealthz` | GET /healthz | — | 200, `{"status":"ok"}` |
| `Test_handleHistory_validRange` | GET /api/history?from=...&to=... | — | 200, JSON array |
| `Test_handleHistory_defaultRange` | GET /api/history (no params) | — | 200, defaults to last 24h |
| `Test_handleHistory_invalidInterval` | interval=weekly | — | 400 |
| `Test_handleHistory_limitCapped` | limit=5000 | — | capped to 1000 |
| `Test_handleSessions` | GET /api/sessions | — | 200, JSON array |
| `Test_handleSearch_valid` | GET /api/search?q=surplus | — | 200, results |
| `Test_handleSearch_missingQ` | GET /api/search | — | 400 |
| `Test_handleSearch_sanitized` | GET /api/search?q="*NEAR | — | no FTS syntax error |
| `Test_handleEvents_typeFilter` | GET /api/events?type=state_change | — | only state_change events |

**File: `internal/web/sse_test.go`**

| Test | What it verifies |
|---|---|
| `Test_Hub_subscribeBroadcastReceive` | Subscribe → Broadcast(snap) → ch receives snap |
| `Test_Hub_multipleSubscribers` | 3 subscribers → Broadcast → all 3 receive |
| `Test_Hub_unsubscribe` | Subscribe → Unsubscribe → Broadcast → ch is closed, no panic |
| `Test_Hub_slowClientDropsEvent` | Subscribe with full channel → Broadcast → no deadlock, warning logged |
| `Test_Hub_clientDisconnect` | Cancel request context → goroutine exits cleanly |
| `Test_Hub_noGoroutineLeak` | Subscribe 10 clients, Unsubscribe all → runtime.NumGoroutine() stable |

---

### Phase 8 — Web UI Template

**File: `internal/web/templates/index.html`**

Single-page dashboard using Tailwind CSS (CDN), htmx, and native EventSource:

**Real-time Panel:**
- PV Production (W), Grid Import/Export (W), Home Load (W), Surplus (W)
- Charging: State, Current Amps, Battery %, Controller Mode (Auto/Manual)
- Auto/Manual toggle, Start/Stop/Set Amps controls (manual mode)
- All values update in real-time via SSE `EventSource("/events")`

**History Panel:**
- Date range picker (from/to inputs)
- Interval selector (Minute / Hour / Day radio buttons)
- Table loaded via `hx-get="/api/history?..."`: Time, PV, Grid, Load, Surplus, Amps, Battery, State
- Pagination (Prev/Next buttons via `hx-get` with offset)

**Charge Sessions Panel:**
- Table: Start, End, Duration, Start→End %, Energy (kWh), Peak Amps, Avg Amps
- Loaded via `hx-get="/api/sessions"`

**Search Panel:**
- Search input: `hx-get="/api/search?q=..."` with `hx-trigger="keyup changed delay:300ms"`
- Results: filterable event log table

---

### Phase 9 — Entry Point

**File: `cmd/server/main.go`**

```
func main()
    // 1. cfg, err = config.Load() → fail fast on error
    // 2. shutdown, err = observability.SetupOTelSDK(ctx, "solar-ev-charger", "0.1.0")
    //    defer shutdown(ctx)
    // 3. logger = observability.NewLogger("solar-ev-charger", cfg.LogLevel)
    // 4. metrics, err = observability.NewMetrics()
    // 5. store, err = storage.NewSQLiteStore(cfg.DBPath, logger)
    //    store.Migrate(ctx)
    //    defer store.Close()
    // 6. inverter = inverter.New(cfg.SungrowHost, cfg.SungrowPort, logger, metrics)
    //    inverter.Connect(ctx)  // non-fatal if fails
    // 7. vehicle, err = tesla.New(cfg, logger, metrics)  // fatal on error
    // 8. ctrl = controller.New(inverter, vehicle, store, cfg, logger, metrics)
    // 9. hub = web.NewHub(logger)
    //    ctrl.OnUpdate = hub.Broadcast
    // 10. handler = web.NewServer(ctrl, store, logger)
    // 11. go ctrl.Run(ctx)
    // 12. Start daily prune goroutine:
    //     go func() {
    //         ticker = time.NewTicker(24 * time.Hour)
    //         // prune once at startup, then daily
    //         store.Prune(ctx, time.Duration(cfg.DBRetentionDays) * 24 * time.Hour)
    //         for { select { case <-ctx.Done(): return; case <-ticker.C: store.Prune(...) } }
    //     }()
    // 13. srv = &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: handler}
    // 14. Graceful shutdown on SIGINT/SIGTERM
    //     signal.Notify(sigCh, SIGINT, SIGTERM)
    //     go srv.ListenAndServe()
    //     <-sigCh → cancel() → srv.Shutdown() → done
```

---

### Phase 10 — Docker Packaging

**Dockerfile (multi-stage):**

```dockerfile
# Stage 1: Build
FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /solar-ev-charger ./cmd/server

# Stage 2: Runtime
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /solar-ev-charger /solar-ev-charger
COPY --from=builder /src/internal/web/templates /templates
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/solar-ev-charger"]
```

**docker-compose.yml:**

```yaml
services:
  solar-ev-charger:
    build: .
    ports:
      - "8080:8080"
    env_file: .env
    volumes:
      - ./secrets:/secrets:ro
      - solar-data:/data
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "--spider", "-q", "http://localhost:8080/healthz"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s

volumes:
  solar-data:
```

**.env.example:**

```bash
# Required
SUNGROW_HOST=192.168.1.100
TESLA_CLIENT_ID=your-client-id
TESLA_CLIENT_SECRET=your-client-secret
TESLA_REFRESH_TOKEN=your-refresh-token
TESLA_VIN=5YJ3E1EA0RF000000

# Optional (defaults shown)
SUNGROW_PORT=8082
TESLA_PRIVATE_KEY_PATH=/secrets/fleet-key.pem
TESLA_REGION=na
POLL_INTERVAL_SECONDS=10
MIN_CHARGE_AMPS=5
MAX_CHARGE_AMPS=32
LINE_VOLTAGE=240
DEADBAND_POLLS=3
WAKE_THRESHOLD_POLLS=6
HTTP_PORT=8080
LOG_LEVEL=info
DB_PATH=/data/solar-ev-charger.db
DB_RETENTION_DAYS=365

# OpenTelemetry (optional — omit for stdout)
# OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318
# OTEL_SERVICE_NAME=solar-ev-charger
```

---

## 6. State Machine Diagram

```
                    ┌──────────────────────────────────────────┐
                    │                                          │
                    ▼                                          │
               ┌─────────┐   car plugged in + surplus    ┌────┴────┐
          ┌───►│  IDLE    │──────────────────────────────►│MONITORING│
          │    └─────────┘                               └────┬────┘
          │         ▲                                         │
          │         │ car unplugged                            │ surplus sustained
          │         │                                         │ >= WAKE_THRESHOLD
          │    ┌────┴─────┐                              ┌────▼────┐
          │    │ CHARGING  │◄────── car woke up ─────────│  WAKE   │
          │    │           │       + start charge         │ PENDING │
          │    └────┬──┬───┘                              └─────────┘
          │         │  │
          │         │  │ surplus changes
          │         │  └──► SetChargingAmps(newAmps) ──► stays CHARGING
          │         │
          │         │ low surplus × DEADBAND_POLLS
          │         ▼
          │    ┌────────────┐
          │    │  STOPPED   │
          │    │ LOW SURPLUS│───── surplus recovers ──► CHARGING
          │    └────────────┘
          │
          │    ┌─────────┐
          └────│  ERROR   │◄──── inverter/tesla failure from any state
               └─────────┘───── auto-recovers on next successful tick
```

---

## 7. Verification Checklist

| # | Command / Action | Expected Result |
|---|---|---|
| 1 | `go test ./... -cover -race` | All tests pass, ≥85% coverage, no race conditions |
| 2 | `go test ./internal/controller/ -cover` | ≥90% coverage |
| 3 | `go test ./internal/storage/ -cover -race` | ≥90% coverage, no races |
| 4 | `go vet ./...` | No issues |
| 5 | `golangci-lint run ./...` | No lint issues (golint is deprecated; use golangci-lint) |
| 6 | `goimports -l .` | No files need formatting |
| 7 | `go build ./...` | Compiles cleanly |
| 6 | `docker build -t solar-ev-charger .` | Tests run in build stage, image builds |
| 7 | `docker compose up` with valid `.env` | Starts, logs JSON to stdout, healthcheck passes |
| 8 | Set `SUNGROW_HOST=<real IP>` | Inverter readings appear in stdout logs within 15s |
| 9 | Open `http://localhost:8080` | Dashboard shows live PV/grid values via SSE |
| 10 | Set `OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318` | Traces/metrics/logs reach collector |
| 11 | With car plugged in + surplus | `set_charging_amps` command logged, car charges |
| 12 | Kill surplus (cover panels) | After DEADBAND_POLLS ticks, `charge_stop` sent |
| 13 | `GET /healthz` | Returns `HTTP 200 {"status":"ok"}` |
| 14 | `POST /api/mode {"mode":"manual"}` | Controller stops auto-adjusting |
| 15 | `POST /api/control {"action":"setAmps","amps":10}` in manual | Car set to 10A |
| 16 | Run app with `DB_PATH=./test.db`, wait 2 min | `readings` table has ~2 rows (1-min averages) |
| 17 | `GET /api/history?from=...&to=...&interval=hour` | Returns hourly-aggregated JSON |
| 18 | `GET /api/search?q=charging` | Returns events matching "charging" |
| 19 | Stop/restart container | Data persists in `solar-data` volume |
| 20 | After 1+ year retention, verify prune log message at startup | Old records deleted |

---

## 8. Decisions & Scope

**In scope:**
- Automatic surplus-only EV charging via simple amps = floor(surplus/voltage) algorithm
- Deadband to prevent rapid start/stop on passing clouds
- Wake-from-sleep logic with sustained surplus threshold
- Manual override mode via web UI
- OAuth2 first-time auth flow via web UI
- OpenTelemetry traces, metrics, and logs with stdout default + OTLP option
- SQLite persistence with 1-minute averaged readings, charge sessions, events
- Full-text search (FTS5) across events via web UI
- Historical data querying with minute/hour/day aggregation
- Auto-pruning of records older than 1 year (configurable)
- Comprehensive unit tests with table-driven patterns, `Test_functionName_scenario` naming, `t.Helper()`/`t.Cleanup()`, and hand-written mocks
- All Go code adheres to project coding standards in `.github/copilot-instructions.md`

**Explicitly out of scope (v1):**
- Fleet Telemetry WebSocket streaming (requires public TLS endpoint)
- Multi-phase charging (three-phase meter support)
- Battery storage integration (SH hybrid inverter registers)
- OAuth2 token persistence to file (displayed in UI; user sets as env var)
- Prometheus `/metrics` endpoint (use OTEL_METRICS_EXPORTER=prometheus if needed)
- OTel Collector sidecar in docker-compose (app is ready; just set env var)
- Mobile app / push notifications
- Graphical charts (v1 uses tables; chart.js or similar can be added in v2)

---

## 9. Tesla Developer Registration Guide (Pre-requisite)

This is a one-time manual setup before the application can be used:

1. Register at `developer.tesla.com`, create an application
2. Generate EC key pair: `openssl ecparam -name prime256v1 -genkey -noout -out fleet-key.pem`
3. Extract public key: `openssl ec -in fleet-key.pem -pubout -out public-key.pem`
4. Host `public-key.pem` at `https://<your-domain>/.well-known/appspecific/com.tesla.3p.public-key.pem`
5. Register the app endpoint with Tesla (POST to `/api/1/partner_accounts`)
6. Pair Virtual Key: visit `https://tesla.com/_ak/<your-domain>` and approve in the Tesla app
7. Run the application and visit `http://localhost:8080/auth/tesla` to complete OAuth2 flow
8. Copy the displayed `refresh_token` into your `.env` file as `TESLA_REFRESH_TOKEN`

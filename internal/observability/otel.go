package observability

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// SetupOTelSDK initializes OpenTelemetry providers for traces, metrics, and logs.
// Returns a shutdown function that flushes and closes all providers.
// Exporter selection is driven by OTEL_*_EXPORTER env vars via autoexport.
func SetupOTelSDK(ctx context.Context, serviceName, serviceVersion string) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error
	shutdown = func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdownFuncs {
			if e := fn(ctx); e != nil {
				errs = append(errs, e)
			}
		}
		return errors.Join(errs...)
	}

	res := sdkresource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Traces.
	spanExporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return shutdown, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(spanExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)

	// Metrics.
	metricReader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return shutdown, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(metricReader),
	)
	otel.SetMeterProvider(mp)
	shutdownFuncs = append(shutdownFuncs, mp.Shutdown)

	// Logs.
	logExporter, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return shutdown, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
	)
	shutdownFuncs = append(shutdownFuncs, lp.Shutdown)

	return shutdown, nil
}

// multiHandler fans out slog records to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, r.Level) {
			if err := handler.Handle(ctx, r); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}

// NewLogger creates a *slog.Logger that writes JSON to stdout and bridges to OTel logs.
func NewLogger(name string, level slog.Level) *slog.Logger {
	stdoutHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
	})
	otelHandler := otelslog.NewHandler(name)
	mh := &multiHandler{handlers: []slog.Handler{stdoutHandler, otelHandler}}
	return slog.New(mh)
}

// Metrics holds registered OTel metric instruments.
type Metrics struct {
	PVPower        metric.Float64Gauge
	GridPower      metric.Float64Gauge
	SurplusPower   metric.Float64Gauge
	LoadPower      metric.Float64Gauge
	ChargeAmps     metric.Int64Gauge
	BatteryPct     metric.Float64Gauge
	ChargeCommands metric.Int64Counter
	StateChanges   metric.Int64Counter
	PollDuration   metric.Float64Histogram
}

// NewMetrics registers all custom OTel metric instruments.
func NewMetrics() (*Metrics, error) {
	meter := otel.Meter("solar-ev-charger")

	pvPower, err := meter.Float64Gauge("solar.pv_power",
		metric.WithDescription("Current PV generation in watts"),
		metric.WithUnit("W"))
	if err != nil {
		return nil, err
	}

	gridPower, err := meter.Float64Gauge("solar.grid_power",
		metric.WithDescription("Grid power in watts (positive=import, negative=export)"),
		metric.WithUnit("W"))
	if err != nil {
		return nil, err
	}

	surplusPower, err := meter.Float64Gauge("solar.surplus_power",
		metric.WithDescription("Available surplus power in watts"),
		metric.WithUnit("W"))
	if err != nil {
		return nil, err
	}

	loadPower, err := meter.Float64Gauge("solar.load_power",
		metric.WithDescription("Home load power in watts"),
		metric.WithUnit("W"))
	if err != nil {
		return nil, err
	}

	chargeAmps, err := meter.Int64Gauge("ev.charge_amps",
		metric.WithDescription("Current EV charge rate in amps"),
		metric.WithUnit("A"))
	if err != nil {
		return nil, err
	}

	batteryPct, err := meter.Float64Gauge("ev.battery_pct",
		metric.WithDescription("EV battery state of charge"),
		metric.WithUnit("%"))
	if err != nil {
		return nil, err
	}

	chargeCommands, err := meter.Int64Counter("ev.charge_commands",
		metric.WithDescription("Number of charge commands sent"),
		metric.WithUnit("{command}"))
	if err != nil {
		return nil, err
	}

	stateChanges, err := meter.Int64Counter("controller.state_changes",
		metric.WithDescription("Number of state machine transitions"),
		metric.WithUnit("{transition}"))
	if err != nil {
		return nil, err
	}

	pollDuration, err := meter.Float64Histogram("controller.poll_duration",
		metric.WithDescription("Duration of each control loop tick"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}

	return &Metrics{
		PVPower:        pvPower,
		GridPower:      gridPower,
		SurplusPower:   surplusPower,
		LoadPower:      loadPower,
		ChargeAmps:     chargeAmps,
		BatteryPct:     batteryPct,
		ChargeCommands: chargeCommands,
		StateChanges:   stateChanges,
		PollDuration:   pollDuration,
	}, nil
}

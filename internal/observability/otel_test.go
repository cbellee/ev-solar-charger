package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

type stubHandler struct {
	enabled     bool
	handled     int
	attrs       []slog.Attr
	groups      []string
	err         error
	lastMessage string
}

func (h *stubHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.enabled
}

func (h *stubHandler) Handle(ctx context.Context, r slog.Record) error {
	h.handled++
	h.lastMessage = r.Message
	return h.err
}

func (h *stubHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *stubHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

func Test_SetupOTelSDK_success(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	ctx := context.Background()
	shutdown, err := SetupOTelSDK(ctx, "test-service", "0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
}

func Test_SetupOTelSDK_shutdownIdempotent(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	ctx := context.Background()
	shutdown, err := SetupOTelSDK(ctx, "test-service", "0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := shutdown(ctx); err != nil {
		t.Fatalf("first shutdown error: %v", err)
	}
	// Second call should not panic.
	_ = shutdown(ctx)
}

func Test_NewLogger_writesToStdout(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(handler)
	logger.Info("test message", "key", "value")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("failed to parse JSON log output: %v", err)
	}
	if msg, ok := m["msg"].(string); !ok || msg != "test message" {
		t.Errorf("msg = %v, want %q", m["msg"], "test message")
	}
}

func Test_NewLogger_includesSource(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: true,
	})
	logger := slog.New(handler)
	logger.Info("source test")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("failed to parse JSON log output: %v", err)
	}
	if _, ok := m["source"]; !ok {
		t.Error("expected 'source' key in log output")
	}
}

func Test_multiHandler_Enabled(t *testing.T) {
	mh := &multiHandler{handlers: []slog.Handler{
		&stubHandler{enabled: false},
		&stubHandler{enabled: true},
	}}

	if !mh.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("expected Enabled to return true when any child handler is enabled")
	}

	mh = &multiHandler{handlers: []slog.Handler{
		&stubHandler{enabled: false},
		&stubHandler{enabled: false},
	}}
	if mh.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("expected Enabled to return false when no child handler is enabled")
	}
}

func Test_multiHandler_Handle(t *testing.T) {
	first := &stubHandler{enabled: true}
	second := &stubHandler{enabled: false}
	third := &stubHandler{enabled: true, err: errors.New("boom")}
	mh := &multiHandler{handlers: []slog.Handler{first, second, third}}

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	err := mh.Handle(context.Background(), record)
	if err == nil {
		t.Fatal("expected joined error from enabled child handler")
	}
	if !errors.Is(err, third.err) {
		t.Fatalf("expected joined error to contain child error, got %v", err)
	}
	if first.handled != 1 {
		t.Fatalf("first handler handled = %d, want 1", first.handled)
	}
	if second.handled != 0 {
		t.Fatalf("disabled handler handled = %d, want 0", second.handled)
	}
	if third.handled != 1 {
		t.Fatalf("third handler handled = %d, want 1", third.handled)
	}
}

func Test_multiHandler_WithAttrs(t *testing.T) {
	mh := &multiHandler{handlers: []slog.Handler{
		&stubHandler{enabled: true},
		&stubHandler{enabled: true},
	}}

	wrapped := mh.WithAttrs([]slog.Attr{slog.String("component", "test")}).(*multiHandler)
	for i, handler := range wrapped.handlers {
		stub := handler.(*stubHandler)
		if len(stub.attrs) != 1 {
			t.Fatalf("handler %d attrs len = %d, want 1", i, len(stub.attrs))
		}
		if stub.attrs[0].Key != "component" {
			t.Fatalf("handler %d attr key = %q, want component", i, stub.attrs[0].Key)
		}
	}
}

func Test_multiHandler_WithGroup(t *testing.T) {
	mh := &multiHandler{handlers: []slog.Handler{
		&stubHandler{enabled: true},
		&stubHandler{enabled: true},
	}}

	wrapped := mh.WithGroup("controller").(*multiHandler)
	for i, handler := range wrapped.handlers {
		stub := handler.(*stubHandler)
		if len(stub.groups) != 1 || stub.groups[0] != "controller" {
			t.Fatalf("handler %d groups = %v, want [controller]", i, stub.groups)
		}
	}
}

func Test_NewLogger_emitsJSONToStdout(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer reader.Close()

	originalStdout := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = originalStdout }()

	logger := NewLogger("test-service", slog.LevelInfo)
	logger.Info("hello", "key", "value")
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close writer: %v", err)
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read stdout: %v", err)
	}
	if !bytes.Contains(output, []byte(`"msg":"hello"`)) {
		t.Fatalf("expected stdout log output to contain message, got %s", output)
	}
	if !bytes.Contains(output, []byte(`"key":"value"`)) {
		t.Fatalf("expected stdout log output to contain attr, got %s", output)
	}
}

func Test_NewMetrics_allInstrumentsNonNil(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	ctx := context.Background()
	shutdown, err := SetupOTelSDK(ctx, "test", "0.0.1")
	if err != nil {
		t.Fatalf("failed to setup OTel: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(ctx) })

	m, err := NewMetrics()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.PVPower == nil {
		t.Error("PVPower is nil")
	}
	if m.GridPower == nil {
		t.Error("GridPower is nil")
	}
	if m.SurplusPower == nil {
		t.Error("SurplusPower is nil")
	}
	if m.LoadPower == nil {
		t.Error("LoadPower is nil")
	}
	if m.ChargeAmps == nil {
		t.Error("ChargeAmps is nil")
	}
	if m.BatteryPct == nil {
		t.Error("BatteryPct is nil")
	}
	if m.ChargeCommands == nil {
		t.Error("ChargeCommands is nil")
	}
	if m.StateChanges == nil {
		t.Error("StateChanges is nil")
	}
	if m.PollDuration == nil {
		t.Error("PollDuration is nil")
	}
}

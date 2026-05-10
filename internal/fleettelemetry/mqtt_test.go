package fleettelemetry

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type fakeUpdater struct {
	cached tesla.ChargeState
	at     time.Time
	count  int
}

func (f *fakeUpdater) GetCachedChargeState() tesla.ChargeState {
	return f.cached
}

func (f *fakeUpdater) UpdateTelemetryChargeState(cs tesla.ChargeState, observedAt time.Time) {
	f.cached = cs
	f.at = observedAt
	f.count++
}

type fakeMessage struct {
	topic   string
	payload []byte
}

func (m fakeMessage) Duplicate() bool   { return false }
func (m fakeMessage) Qos() byte         { return 0 }
func (m fakeMessage) Retained() bool    { return false }
func (m fakeMessage) Topic() string     { return m.topic }
func (m fakeMessage) MessageID() uint16 { return 0 }
func (m fakeMessage) Payload() []byte   { return m.payload }
func (m fakeMessage) Ack()              {}

func testBridge(t *testing.T) (*mqttBridge, *fakeUpdater) {
	t.Helper()
	updater := &fakeUpdater{}
	return &mqttBridge{
		ctrl:      updater,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		vin:       "VIN123",
		topicBase: "telemetry",
	}, updater
}

func TestStartMQTTBridge_disabledWithoutBroker(t *testing.T) {
	if err := StartMQTTBridge(context.Background(), &fakeUpdater{}, slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMQTTBridge_handleConnectivity(t *testing.T) {
	bridge, updater := testBridge(t)
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/connectivity", payload: []byte(`{"Status":"CONNECTED","CreatedAt":"2026-05-11T10:00:00Z"}`)})

	if updater.count != 1 {
		t.Fatalf("update count = %d, want 1", updater.count)
	}
	if !updater.cached.IsOnline {
		t.Fatal("IsOnline = false, want true")
	}
	if updater.at.Format(time.RFC3339) != "2026-05-11T10:00:00Z" {
		t.Fatalf("observedAt = %s, want %s", updater.at.Format(time.RFC3339), "2026-05-11T10:00:00Z")
	}
}

func TestMQTTBridge_handleMetricFields(t *testing.T) {
	bridge, updater := testBridge(t)
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/v/Soc", payload: []byte(`61.2`)})
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/v/ChargeAmps", payload: []byte(`15.6`)})
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/v/ChargeLimitSoc", payload: []byte(`80`)})
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/v/TimeToFullCharge", payload: []byte(`1.5`)})
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/v/DetailedChargeState", payload: []byte(`"DetailedChargeStateCharging"`)})
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/v/ChargePortLatch", payload: []byte(`"ChargePortLatchEngaged"`)})

	if updater.cached.BatteryPct != 61.2 {
		t.Fatalf("BatteryPct = %v, want 61.2", updater.cached.BatteryPct)
	}
	if updater.cached.AmpsActual != 16 {
		t.Fatalf("AmpsActual = %d, want 16", updater.cached.AmpsActual)
	}
	if updater.cached.ChargeLimit != 80 {
		t.Fatalf("ChargeLimit = %d, want 80", updater.cached.ChargeLimit)
	}
	if updater.cached.TimeToLimitHours != 1.5 {
		t.Fatalf("TimeToLimitHours = %v, want 1.5", updater.cached.TimeToLimitHours)
	}
	if updater.cached.State != "Charging" {
		t.Fatalf("State = %q, want %q", updater.cached.State, "Charging")
	}
	if !updater.cached.PluggedIn {
		t.Fatal("PluggedIn = false, want true")
	}
}

func TestMQTTBridge_handleDisconnectedStateSetsPluggedOut(t *testing.T) {
	bridge, updater := testBridge(t)
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/v/ChargeState", payload: []byte(`"ChargeStateDisconnected"`)})

	if updater.cached.State != "Disconnected" {
		t.Fatalf("State = %q, want %q", updater.cached.State, "Disconnected")
	}
	if updater.cached.PluggedIn {
		t.Fatal("PluggedIn = true, want false")
	}
}

func TestMQTTBridge_ignoresUnknownTopics(t *testing.T) {
	bridge, updater := testBridge(t)
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/v/VehicleName", payload: []byte(`"charger"`)})
	bridge.handleMessage(nil, fakeMessage{topic: "telemetry/VIN123/errors/foo", payload: []byte(`{"Body":"x"}`)})

	if updater.count != 0 {
		t.Fatalf("update count = %d, want 0", updater.count)
	}
}

func TestMQTTFieldFromTopic(t *testing.T) {
	field, ok := mqttFieldFromTopic("telemetry/VIN123/v/BatteryLevel")
	if !ok {
		t.Fatal("expected topic to parse")
	}
	if field != "BatteryLevel" {
		t.Fatalf("field = %q, want %q", field, "BatteryLevel")
	}
}

func TestNormalizeMQTTBroker(t *testing.T) {
	if got := normalizeMQTTBroker("broker:1883"); got != "tcp://broker:1883" {
		t.Fatalf("normalizeMQTTBroker = %q, want %q", got, "tcp://broker:1883")
	}
	if got := normalizeMQTTBroker("ssl://broker:8883"); got != "ssl://broker:8883" {
		t.Fatalf("normalizeMQTTBroker = %q, want %q", got, "ssl://broker:8883")
	}
}

var _ mqtt.Message = fakeMessage{}

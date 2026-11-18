package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func TestConsumerChargingHints(t *testing.T) {
	tests := []struct {
		name      string
		topic     string
		payload   string
		retained  bool
		wantErr   bool
		wantCount int
	}{
		{name: "charge state", topic: "tesla/VIN/v/DetailedChargeState", payload: `"DetailedChargeStateDisconnected"`, wantCount: 1},
		{name: "latch", topic: "tesla/VIN/v/ChargePortLatch", payload: `"ChargePortLatchEngaged"`, wantCount: 1},
		{name: "other VIN", topic: "tesla/OTHER/v/ChargePortLatch", payload: `"Engaged"`, wantErr: true},
		{name: "other field", topic: "tesla/VIN/v/BatteryLevel", payload: `70`, wantErr: true},
		{name: "retained state", topic: "tesla/VIN/v/ChargePortLatch", payload: `"Engaged"`, retained: true},
		{name: "null", topic: "tesla/VIN/v/ChargePortLatch", payload: `null`, wantErr: true},
		{name: "object", topic: "tesla/VIN/v/ChargePortLatch", payload: `{}`, wantErr: true},
		{name: "invalid JSON", topic: "tesla/VIN/v/ChargePortLatch", payload: `"`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := 0
			c := NewConsumer("", "VIN", func() { count++ }, nil)
			for range 2 {
				err := c.handleMessage(tt.topic, []byte(tt.payload), tt.retained, time.Now())
				if (err != nil) != tt.wantErr {
					t.Fatalf("handleMessage error = %v, wantErr %v", err, tt.wantErr)
				}
			}
			if count != tt.wantCount {
				t.Fatalf("notifications = %d, want %d (duplicate must be coalesced)", count, tt.wantCount)
			}
		})
	}
}

func TestConsumerConnectivityFreshness(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		status    string
		age       time.Duration
		wantErr   bool
		wantCount int
	}{
		{name: "connected", status: "CONNECTED", wantCount: 1},
		{name: "disconnected", status: "DISCONNECTED"},
		{name: "stale", status: "CONNECTED", age: 6 * time.Minute, wantErr: true},
		{name: "future", status: "CONNECTED", age: -2 * time.Minute, wantErr: true},
		{name: "unknown", status: "UNKNOWN", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := 0
			c := NewConsumer("", "VIN", func() { count++ }, nil)
			payload := fmt.Sprintf(`{"Status":%q,"CreatedAt":%q}`, tt.status, now.Add(-tt.age).Format(time.RFC3339))
			for range 2 {
				err := c.handleMessage("tesla/VIN/connectivity", []byte(payload), false, now)
				if (err != nil) != tt.wantErr {
					t.Fatalf("handleMessage error = %v, wantErr %v", err, tt.wantErr)
				}
			}
			if count != tt.wantCount {
				t.Fatalf("notifications = %d, want %d", count, tt.wantCount)
			}
		})
	}
}

func TestConsumerMQTTIntegration(t *testing.T) {
	broker := os.Getenv("TELEMETRY_TEST_BROKER")
	if broker == "" {
		t.Skip("set TELEMETRY_TEST_BROKER to run against an isolated MQTT broker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hints := make(chan struct{}, 10)
	vin := fmt.Sprintf("TEST%d", time.Now().UnixNano())
	c := NewConsumer(broker, vin, func() { hints <- struct{}{} }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			t.Error("MQTT consumer did not stop after cancellation")
		}
	}()
	// Subscription completion requests a reconciliation read.
	select {
	case <-hints:
	case <-ctx.Done():
		t.Fatal("consumer did not subscribe")
	}
	publisher := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(broker).SetConnectTimeout(3 * time.Second))
	defer publisher.Disconnect(0)
	if err := waitToken(ctx, publisher.Connect()); err != nil {
		t.Fatal(err)
	}
	if err := waitToken(ctx, publisher.Publish("tesla/"+vin+"/v/ChargePortLatch", 1, false, `"ChargePortLatchEngaged"`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hints:
	case <-ctx.Done():
		t.Fatal("real MQTT message did not request a refresh")
	}
}

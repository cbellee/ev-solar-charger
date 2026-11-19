// Package telemetry consumes Fleet Telemetry events as hints for REST-confirmed
// vehicle state. MQTT field payloads do not include vehicle timestamps.
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Consumer listens only to charging and connectivity topics for one VIN.
// The notification callback must not block or perform network I/O.
type Consumer struct {
	brokerURL   string
	prefix      string
	notify      func()
	logger      *slog.Logger
	mu          sync.Mutex
	values      map[string]string
	connectedAt time.Time
}

// NewConsumer creates a consumer for a validated broker URL and VIN.
func NewConsumer(brokerURL, vin string, notify func(), logger *slog.Logger) *Consumer {
	return &Consumer{
		brokerURL: brokerURL,
		prefix:    "tesla/" + vin + "/",
		notify:    notify,
		logger:    logger,
		values:    make(map[string]string),
	}
}

// Run reconnects until ctx is cancelled. Loss of MQTT never disables the
// controller's normal polling. All connection failures are logged.
func (c *Consumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.consume(ctx); err != nil && ctx.Err() == nil {
			c.logger.WarnContext(ctx, "telemetry MQTT unavailable; normal polling remains active", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *Consumer) consume(ctx context.Context) error {
	lost := make(chan error, 1)
	opts := mqtt.NewClientOptions().
		AddBroker(c.brokerURL).
		SetCleanSession(true).
		SetAutoReconnect(false).
		SetConnectRetry(false).
		SetConnectTimeout(5 * time.Second).
		SetWriteTimeout(5 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			select {
			case lost <- err:
			default:
			}
		})
	client := mqtt.NewClient(opts)
	defer client.Disconnect(0)
	if err := waitToken(ctx, client.Connect()); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	topics := map[string]byte{
		c.prefix + "v/DetailedChargeState": 1,
		c.prefix + "v/ChargePortLatch":     1,
		c.prefix + "connectivity":          1,
	}
	token := client.SubscribeMultiple(topics, func(_ mqtt.Client, message mqtt.Message) {
		if err := c.handleMessage(message.Topic(), message.Payload(), message.Retained(), time.Now()); err != nil {
			c.logger.WarnContext(ctx, "invalid telemetry hint", "error", err)
		}
	})
	if err := waitToken(ctx, token); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	c.logger.InfoContext(ctx, "telemetry MQTT subscribed; events request REST-confirmed state")
	// A clean session may have missed changes while disconnected.
	c.notify()
	select {
	case <-ctx.Done():
		return nil
	case err := <-lost:
		return fmt.Errorf("connection lost: %w", err)
	}
}

func waitToken(ctx context.Context, token mqtt.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token.Done():
		return token.Error()
	}
}

func (c *Consumer) handleMessage(topic string, payload []byte, retained bool, now time.Time) error {
	if !strings.HasPrefix(topic, c.prefix) {
		return fmt.Errorf("unexpected vehicle topic")
	}
	// Retained state may predate this process or the last vehicle session.
	if retained {
		return nil
	}
	if len(payload) > 4096 {
		return fmt.Errorf("payload exceeds 4096 bytes")
	}
	suffix := strings.TrimPrefix(topic, c.prefix)
	switch suffix {
	case "v/DetailedChargeState", "v/ChargePortLatch":
		var value string
		if err := json.Unmarshal(payload, &value); err != nil || value == "" || len(value) > 256 {
			return fmt.Errorf("expected a nonempty charging-state string")
		}
		c.mu.Lock()
		changed := c.values[suffix] != value
		c.values[suffix] = value
		c.mu.Unlock()
		if changed {
			c.notify()
		}
		return nil
	case "connectivity":
		var event struct {
			Status    string
			CreatedAt time.Time
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("invalid connectivity event: %w", err)
		}
		if event.Status != "CONNECTED" && event.Status != "DISCONNECTED" {
			return fmt.Errorf("unknown connectivity status")
		}
		if event.CreatedAt.IsZero() || now.Sub(event.CreatedAt) > 5*time.Minute || event.CreatedAt.After(now.Add(time.Minute)) {
			return fmt.Errorf("connectivity timestamp is missing or outside the freshness window")
		}
		c.mu.Lock()
		newer := event.CreatedAt.After(c.connectedAt)
		if newer {
			c.connectedAt = event.CreatedAt
		}
		c.mu.Unlock()
		if newer && event.Status == "CONNECTED" {
			c.notify()
		}
		return nil
	default:
		return fmt.Errorf("unexpected telemetry field")
	}
}

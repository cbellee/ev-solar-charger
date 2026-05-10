package fleettelemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type chargeStateUpdater interface {
	GetCachedChargeState() tesla.ChargeState
	UpdateTelemetryChargeState(tesla.ChargeState, time.Time)
}

type mqttBridge struct {
	ctrl      chargeStateUpdater
	logger    *slog.Logger
	vin       string
	topicBase string
	client    mqtt.Client
}

type connectivityMessage struct {
	Status    string `json:"Status"`
	CreatedAt string `json:"CreatedAt"`
}

// StartMQTTBridge starts an optional Fleet Telemetry MQTT subscriber.
// When configured, it listens for a small subset of MQTT field topics and
// refreshes the controller cache from stream data instead of paid polling.
func StartMQTTBridge(ctx context.Context, ctrl chargeStateUpdater, logger *slog.Logger, cfg config.Config) error {
	if strings.TrimSpace(cfg.FleetTelemetryMQTTBroker) == "" {
		return nil
	}

	bridge := &mqttBridge{
		ctrl:      ctrl,
		logger:    logger,
		vin:       cfg.TeslaVIN,
		topicBase: strings.Trim(strings.TrimSpace(cfg.FleetTelemetryMQTTTopicBase), "/"),
	}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(normalizeMQTTBroker(cfg.FleetTelemetryMQTTBroker))
	opts.SetClientID(mqttClientID(cfg))
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(30 * time.Second)
	opts.SetOrderMatters(false)
	opts.SetOnConnectHandler(bridge.onConnect)
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		bridge.logger.Warn("fleet telemetry mqtt connection lost", "error", err)
	})
	if cfg.FleetTelemetryMQTTUsername != "" {
		opts.SetUsername(cfg.FleetTelemetryMQTTUsername)
		opts.SetPassword(cfg.FleetTelemetryMQTTPassword)
	}

	client := mqtt.NewClient(opts)
	bridge.client = client
	token := client.Connect()
	if token.WaitTimeout(5*time.Second) && token.Error() != nil {
		bridge.logger.Warn("fleet telemetry mqtt initial connect failed; client will keep retrying", "error", token.Error())
	}

	go func() {
		<-ctx.Done()
		if bridge.client.IsConnected() {
			bridge.client.Disconnect(250)
		}
	}()

	bridge.logger.Info("fleet telemetry mqtt bridge enabled",
		"broker", cfg.FleetTelemetryMQTTBroker,
		"topic_base", bridge.topicBase,
		"vin", bridge.vin)
	return nil
}

func normalizeMQTTBroker(broker string) string {
	broker = strings.TrimSpace(broker)
	if broker == "" {
		return broker
	}
	if strings.Contains(broker, "://") {
		return broker
	}
	return "tcp://" + broker
}

func mqttClientID(cfg config.Config) string {
	if strings.TrimSpace(cfg.FleetTelemetryMQTTClientID) != "" {
		return cfg.FleetTelemetryMQTTClientID
	}
	vinSuffix := cfg.TeslaVIN
	if len(vinSuffix) > 6 {
		vinSuffix = vinSuffix[len(vinSuffix)-6:]
	}
	if vinSuffix == "" {
		return "solar-ev-charger"
	}
	return fmt.Sprintf("solar-ev-charger-%s", strings.ToLower(vinSuffix))
}

func (b *mqttBridge) onConnect(client mqtt.Client) {
	subscriptions := map[string]byte{
		fmt.Sprintf("%s/%s/connectivity", b.topicBase, b.vin): 0,
		fmt.Sprintf("%s/%s/v/+", b.topicBase, b.vin):          0,
	}
	token := client.SubscribeMultiple(subscriptions, b.handleMessage)
	if token.Wait() && token.Error() != nil {
		b.logger.Warn("fleet telemetry mqtt subscribe failed", "error", token.Error())
		return
	}
	b.logger.Info("fleet telemetry mqtt subscribed", "vin", b.vin)
}

func (b *mqttBridge) handleMessage(_ mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	if strings.HasSuffix(topic, "/connectivity") {
		b.handleConnectivity(msg.Payload())
		return
	}

	field, ok := mqttFieldFromTopic(topic)
	if !ok {
		return
	}
	b.handleField(field, msg.Payload())
}

func mqttFieldFromTopic(topic string) (string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) < 2 {
		return "", false
	}
	field := parts[len(parts)-1]
	if field == "v" || field == "connectivity" || field == "" {
		return "", false
	}
	return field, true
}

func (b *mqttBridge) handleConnectivity(payload []byte) {
	var msg connectivityMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		b.logger.Warn("fleet telemetry mqtt connectivity decode failed", "error", err)
		return
	}

	cs := b.ctrl.GetCachedChargeState()
	switch strings.ToUpper(strings.TrimSpace(msg.Status)) {
	case "CONNECTED":
		cs.IsOnline = true
	case "DISCONNECTED":
		cs.IsOnline = false
	default:
		return
	}

	observedAt := time.Now()
	if parsed, err := time.Parse(time.RFC3339, msg.CreatedAt); err == nil {
		observedAt = parsed
	}
	b.ctrl.UpdateTelemetryChargeState(cs, observedAt)
}

func (b *mqttBridge) handleField(field string, payload []byte) {
	cs := b.ctrl.GetCachedChargeState()
	updated := false

	switch field {
	case "BatteryLevel", "Soc":
		value, ok := decodeMQTTNumber(payload)
		if !ok {
			return
		}
		cs.BatteryPct = value
		updated = true
	case "ChargeAmps":
		value, ok := decodeMQTTNumber(payload)
		if !ok {
			return
		}
		cs.AmpsActual = int(math.Round(value))
		updated = true
	case "TimeToFullCharge", "EstimatedHoursToChargeTermination":
		value, ok := decodeMQTTNumber(payload)
		if !ok {
			return
		}
		cs.TimeToLimitHours = value
		updated = true
	case "ChargeLimitSoc":
		value, ok := decodeMQTTNumber(payload)
		if !ok {
			return
		}
		cs.ChargeLimit = int(math.Round(value))
		updated = true
	case "ChargePortLatch":
		value, ok := decodeMQTTString(payload)
		if !ok {
			return
		}
		pluggedIn, ok := normalizeChargePortLatch(value)
		if !ok {
			return
		}
		cs.PluggedIn = pluggedIn
		updated = true
	case "DetailedChargeState", "ChargeState":
		value, ok := decodeMQTTString(payload)
		if !ok {
			return
		}
		state, ok := normalizeChargeState(value)
		if !ok {
			return
		}
		cs.State = state
		if pluggedIn, ok := pluggedInFromChargeState(state); ok {
			cs.PluggedIn = pluggedIn
		}
		updated = true
	default:
		return
	}

	if updated {
		b.ctrl.UpdateTelemetryChargeState(cs, time.Now())
	}
}

func decodeMQTTNumber(payload []byte) (float64, bool) {
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return 0, false
	}
	switch value := decoded.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case nil:
		return 0, false
	default:
		return 0, false
	}
}

func decodeMQTTString(payload []byte) (string, bool) {
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", false
	}
	value, ok := decoded.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func normalizeChargeState(raw string) (string, bool) {
	state := strings.TrimSpace(raw)
	state = strings.TrimPrefix(state, "DetailedChargeState")
	state = strings.TrimPrefix(state, "ChargeState")
	state = strings.TrimSpace(state)
	if state == "Unknown" || state == "" {
		return "", false
	}
	switch state {
	case "Charging", "Stopped", "Disconnected", "Complete", "Starting", "NoPower":
		return state, true
	default:
		return "", false
	}
}

func normalizeChargePortLatch(raw string) (bool, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), "ChargePortLatch")
	switch value {
	case "Engaged", "Blocking":
		return true, true
	case "Disengaged":
		return false, true
	default:
		return false, false
	}
}

func pluggedInFromChargeState(state string) (bool, bool) {
	switch state {
	case "Disconnected":
		return false, true
	case "Charging", "Stopped", "Complete", "Starting", "NoPower":
		return true, true
	default:
		return false, false
	}
}

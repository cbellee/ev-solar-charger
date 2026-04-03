package inverter

import (
	"context"
	"time"
)

// PowerData holds a point-in-time reading from the solar inverter.
type PowerData struct {
	PVWatts      float64
	GridWatts    float64
	LoadWatts    float64
	SurplusWatts float64
	Timestamp    time.Time
}

// InverterReader reads real-time power data from a solar inverter.
type InverterReader interface {
	Connect(ctx context.Context) error
	GetPowerData(ctx context.Context) (PowerData, error)
	Close() error
}

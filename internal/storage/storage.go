package storage

import (
	"context"
	"time"
)

// Reading is a 1-minute averaged data point persisted to the database.
type Reading struct {
	ID           int64
	Timestamp    time.Time
	PVWatts      float64
	GridWatts    float64
	LoadWatts    float64
	SurplusWatts float64
	ChargeAmps   int
	BatteryPct   float64
	State        string
}

// ChargeSession records a contiguous charging period.
type ChargeSession struct {
	ID           int64
	StartTime    time.Time
	EndTime      time.Time
	StartBattery float64
	EndBattery   float64
	EnergyKWh    float64
	PeakAmps     int
	AvgAmps      float64
}

// Event records a state machine transition or notable event.
type Event struct {
	ID        int64
	Timestamp time.Time
	Type      string
	Message   string
	Details   string
}

// ReadingFilter for querying historical readings.
type ReadingFilter struct {
	From     time.Time
	To       time.Time
	Interval string
	Limit    int
	Offset   int
}

// Store persists and queries historical solar/charging data.
type Store interface {
	Migrate(ctx context.Context) error
	InsertReading(ctx context.Context, r Reading) error
	QueryReadings(ctx context.Context, f ReadingFilter) ([]Reading, error)
	StartSession(ctx context.Context, s ChargeSession) (int64, error)
	EndSession(ctx context.Context, id int64, endTime time.Time, endBattery, energyKWh float64, peakAmps int, avgAmps float64) error
	QuerySessions(ctx context.Context, from, to time.Time, limit, offset int) ([]ChargeSession, error)
	InsertEvent(ctx context.Context, e Event) error
	QueryEvents(ctx context.Context, from, to time.Time, eventType string, limit, offset int) ([]Event, error)
	Search(ctx context.Context, query string, from, to time.Time, limit int) ([]Event, error)
	Prune(ctx context.Context, olderThan time.Duration) (int64, error)
	Close() error
}

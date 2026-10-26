package controller

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/storage"
)

func newUsageStore(t *testing.T) *storage.SQLiteStore {
	t.Helper()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

type usageRow struct {
	at                  time.Time
	data, command, wake int64
}

func insertUsage(t *testing.T, store storage.Store, rows ...usageRow) {
	t.Helper()
	for _, r := range rows {
		if err := store.InsertAPIUsage(context.Background(), storage.APIUsageSnapshot{
			Timestamp: r.at, DataCalls: r.data, CommandCalls: r.command, WakeCalls: r.wake,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func utc(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC)
}

func TestLoadAPIUsage(t *testing.T) {
	baseline := &config.UsageBaseline{At: utc(26, 3, 27), DataCalls: 1449, CommandCalls: 1550, WakeCalls: 19}
	tests := []struct {
		name     string
		rows     []usageRow
		baseline *config.UsageBaseline
		now      time.Time
		wantOK   bool
		want     usageRow
	}{
		{name: "no snapshots", now: utc(26, 4, 0)},
		{
			name: "max of this UTC month only",
			rows: []usageRow{
				{at: time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC), data: 9000, command: 9000, wake: 90}, // 1 Oct 07:00 AEST
				{at: utc(10, 0, 0), data: 100, command: 200, wake: 1},
				{at: utc(20, 0, 0), data: 300, command: 400, wake: 2},
			},
			now:    utc(26, 4, 0),
			wantOK: true,
			want:   usageRow{data: 300, command: 400, wake: 2},
		},
		{
			name: "baseline replaces inflated counts and keeps calls since",
			rows: []usageRow{
				{at: utc(1, 0, 5), data: 2500, command: 2000, wake: 30}, // carried over from September
				{at: utc(26, 3, 0), data: 2100, command: 1650, wake: 2},
				{at: utc(26, 3, 50), data: 2110, command: 1655, wake: 2},
			},
			baseline: baseline,
			now:      utc(26, 4, 0),
			wantOK:   true,
			want:     usageRow{data: 1459, command: 1555, wake: 19},
		},
		{
			name:     "baseline with no snapshots",
			baseline: baseline,
			now:      utc(26, 4, 0),
			wantOK:   true,
			want:     usageRow{data: 1449, command: 1550, wake: 19},
		},
		{
			name:     "baseline in the future is ignored",
			rows:     []usageRow{{at: utc(26, 3, 0), data: 10, command: 10}},
			baseline: baseline,
			now:      utc(26, 3, 0),
			wantOK:   true,
			want:     usageRow{data: 10, command: 10},
		},
		{
			name:     "baseline from last month is ignored",
			rows:     []usageRow{{at: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), data: 10, command: 10}},
			baseline: baseline,
			now:      time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC),
			wantOK:   true,
			want:     usageRow{data: 10, command: 10},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newUsageStore(t)
			insertUsage(t, store, tt.rows...)
			got, ok, err := LoadAPIUsage(context.Background(), store, tt.baseline, tt.now)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.DataCalls != tt.want.data || got.CommandCalls != tt.want.command || got.WakeCalls != tt.want.wake {
				t.Fatalf("usage = %d/%d/%d, want %d/%d/%d", got.DataCalls, got.CommandCalls, got.WakeCalls, tt.want.data, tt.want.command, tt.want.wake)
			}
			if want := time.Date(tt.now.Year(), tt.now.Month(), 1, 0, 0, 0, 0, time.UTC); !got.MonthStarted.Equal(want) {
				t.Fatalf("MonthStarted = %v, want %v", got.MonthStarted, want)
			}
		})
	}
}

func TestLoadAPIUsageAppliesBaselineOnce(t *testing.T) {
	ctx := context.Background()
	store := newUsageStore(t)
	baseline := &config.UsageBaseline{At: utc(26, 3, 27), DataCalls: 1449, CommandCalls: 1550, WakeCalls: 19}
	insertUsage(t, store,
		usageRow{at: utc(1, 0, 5), data: 2500, command: 2000, wake: 30},
		usageRow{at: utc(26, 3, 0), data: 2100, command: 1650, wake: 2},
	)

	if _, _, err := LoadAPIUsage(ctx, store, baseline, utc(26, 4, 0)); err != nil {
		t.Fatal(err)
	}
	// The corrected counters keep growing after the restart.
	insertUsage(t, store, usageRow{at: utc(27, 0, 0), data: 1500, command: 1600, wake: 20})

	got, ok, err := LoadAPIUsage(ctx, store, baseline, utc(27, 1, 0))
	if err != nil || !ok {
		t.Fatalf("second load: ok=%v err=%v", ok, err)
	}
	if got.DataCalls != 1500 || got.CommandCalls != 1600 || got.WakeCalls != 20 {
		t.Fatalf("second load = %d/%d/%d, want 1500/1600/20", got.DataCalls, got.CommandCalls, got.WakeCalls)
	}
	events, err := store.QueryEvents(ctx, utc(1, 0, 0), utc(28, 0, 0), usageBaselineEventType, 10, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("baseline events = %d (err %v), want 1", len(events), err)
	}
}

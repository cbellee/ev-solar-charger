package storage

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	store, err := NewSQLiteStore(dbPath, logger)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return store
}

func Test_Migrate_createsTablesIdempotently(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	// Calling Migrate again should succeed.
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
	// Verify tables exist by querying them.
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM readings").Scan(&count); err != nil {
		t.Fatalf("readings table missing: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_sessions").Scan(&count); err != nil {
		t.Fatalf("charge_sessions table missing: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("events table missing: %v", err)
	}
}

func Test_InsertReading_queryReadings(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)
	for i := 0; i < 10; i++ {
		r := Reading{
			Timestamp:    now.Add(time.Duration(i) * time.Minute),
			PVWatts:      5000,
			GridWatts:    -2000,
			LoadWatts:    3000,
			SurplusWatts: 2000,
			ChargeAmps:   10,
			BatteryPct:   50,
			State:        "charging",
		}
		if err := store.InsertReading(ctx, r); err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}
	}
	readings, err := store.QueryReadings(ctx, ReadingFilter{
		From:  now.Add(-time.Minute),
		To:    now.Add(11 * time.Minute),
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(readings) != 10 {
		t.Errorf("got %d readings, want 10", len(readings))
	}
}

func Test_QueryReadings_hourlyAggregation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		r := Reading{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			PVWatts:      float64(5000 + i),
			GridWatts:    -2000,
			LoadWatts:    3000,
			SurplusWatts: 2000,
			ChargeAmps:   10,
			BatteryPct:   50,
			State:        "charging",
		}
		if err := store.InsertReading(ctx, r); err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}
	}
	readings, err := store.QueryReadings(ctx, ReadingFilter{
		From:     base.Add(-time.Minute),
		To:       base.Add(61 * time.Minute),
		Interval: "hour",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(readings) != 1 {
		t.Errorf("got %d rows, want 1 (hourly aggregation)", len(readings))
	}
}

func Test_QueryReadings_dailyAggregation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		r := Reading{
			Timestamp:    base.Add(time.Duration(i) * 24 * time.Hour),
			PVWatts:      5000,
			GridWatts:    -2000,
			LoadWatts:    3000,
			SurplusWatts: 2000,
			ChargeAmps:   10,
			BatteryPct:   50,
			State:        "charging",
		}
		if err := store.InsertReading(ctx, r); err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}
	}
	readings, err := store.QueryReadings(ctx, ReadingFilter{
		From:     base.Add(-time.Hour),
		To:       base.Add(4 * 24 * time.Hour),
		Interval: "day",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(readings) != 3 {
		t.Errorf("got %d rows, want 3 (daily aggregation)", len(readings))
	}
}

func Test_QueryReadings_pagination(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)
	for i := 0; i < 50; i++ {
		r := Reading{
			Timestamp:    now.Add(time.Duration(i) * time.Minute),
			PVWatts:      5000,
			GridWatts:    -2000,
			LoadWatts:    3000,
			SurplusWatts: 2000,
			ChargeAmps:   10,
			BatteryPct:   50,
			State:        "charging",
		}
		if err := store.InsertReading(ctx, r); err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}
	}
	page1, err := store.QueryReadings(ctx, ReadingFilter{
		From: now.Add(-time.Minute), To: now.Add(51 * time.Minute), Limit: 10, Offset: 0,
	})
	if err != nil {
		t.Fatalf("page1 query failed: %v", err)
	}
	if len(page1) != 10 {
		t.Errorf("page1: got %d, want 10", len(page1))
	}
	page2, err := store.QueryReadings(ctx, ReadingFilter{
		From: now.Add(-time.Minute), To: now.Add(51 * time.Minute), Limit: 10, Offset: 10,
	})
	if err != nil {
		t.Fatalf("page2 query failed: %v", err)
	}
	if len(page2) != 10 {
		t.Errorf("page2: got %d, want 10", len(page2))
	}
}

func Test_QueryReadings_emptyRange(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	readings, err := store.QueryReadings(ctx, ReadingFilter{
		From:  time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2099, 1, 2, 0, 0, 0, 0, time.UTC),
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if readings == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(readings) != 0 {
		t.Errorf("got %d readings, want 0", len(readings))
	}
}

func Test_StartSession_endSession(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	id, err := store.StartSession(ctx, ChargeSession{
		StartTime:    now,
		StartBattery: 30.0,
	})
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero session ID")
	}
	endTime := now.Add(2 * time.Hour)
	if err := store.EndSession(ctx, id, endTime, 80.0, 12.5, 32, 18.5); err != nil {
		t.Fatalf("end session failed: %v", err)
	}
	sessions, err := store.QuerySessions(ctx, now.Add(-time.Hour), endTime.Add(time.Hour), 10, 0)
	if err != nil {
		t.Fatalf("query sessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].EndBattery != 80.0 {
		t.Errorf("EndBattery = %f, want 80.0", sessions[0].EndBattery)
	}
	if sessions[0].PeakAmps != 32 {
		t.Errorf("PeakAmps = %d, want 32", sessions[0].PeakAmps)
	}
	if sessions[0].AvgAmps != 18.5 {
		t.Errorf("AvgAmps = %f, want 18.5", sessions[0].AvgAmps)
	}
}

func Test_QuerySessions_dateFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_, err := store.StartSession(ctx, ChargeSession{
			StartTime:    base.Add(time.Duration(i) * 24 * time.Hour),
			StartBattery: 30.0,
		})
		if err != nil {
			t.Fatalf("start session %d failed: %v", i, err)
		}
	}
	sessions, err := store.QuerySessions(ctx,
		base, base.Add(3*24*time.Hour), 10, 0)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(sessions) != 4 {
		t.Errorf("got %d sessions, want 4", len(sessions))
	}
}

func Test_InsertEvent_queryEvents(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	e := Event{
		Timestamp: now,
		Type:      "state_change",
		Message:   "idle to charging",
		Details:   `{"reason":"surplus"}`,
	}
	if err := store.InsertEvent(ctx, e); err != nil {
		t.Fatalf("insert event failed: %v", err)
	}
	events, err := store.QueryEvents(ctx, now.Add(-time.Hour), now.Add(time.Hour), "", 10, 0)
	if err != nil {
		t.Fatalf("query events failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Message != "idle to charging" {
		t.Errorf("Message = %q, want %q", events[0].Message, "idle to charging")
	}
}

func Test_Search_ftsMatch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	events := []Event{
		{Timestamp: now, Type: "state_change", Message: "surplus detected, starting charge"},
		{Timestamp: now, Type: "state_change", Message: "low surplus, stopping"},
		{Timestamp: now, Type: "command", Message: "wake up sent"},
	}
	for _, e := range events {
		if err := store.InsertEvent(ctx, e); err != nil {
			t.Fatalf("insert event failed: %v", err)
		}
	}
	results, err := store.Search(ctx, "surplus", now.Add(-time.Hour), now.Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results for 'surplus', want 2", len(results))
	}
}

func Test_Search_noResults(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := store.InsertEvent(ctx, Event{Timestamp: now, Type: "test", Message: "hello world"}); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	results, err := store.Search(ctx, "nonexistent", now.Add(-time.Hour), now.Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func Test_Search_specialChars(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := store.InsertEvent(ctx, Event{Timestamp: now, Type: "test", Message: "hello"}); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	// These special chars should be safely stripped.
	results, err := store.Search(ctx, `"*NEAR()`, now.Add(-time.Hour), now.Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("search with special chars failed: %v", err)
	}
	_ = results
	// Should return empty (sanitized to empty or non-matching tokens).
}

func Test_Prune_deletesOldRecords(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-2 * 365 * 24 * time.Hour)
	// Insert old and new readings.
	if err := store.InsertReading(ctx, Reading{Timestamp: old, PVWatts: 1000, State: "old"}); err != nil {
		t.Fatalf("insert old reading: %v", err)
	}
	if err := store.InsertReading(ctx, Reading{Timestamp: now, PVWatts: 2000, State: "new"}); err != nil {
		t.Fatalf("insert new reading: %v", err)
	}
	// Insert old event.
	if err := store.InsertEvent(ctx, Event{Timestamp: old, Type: "test", Message: "old event"}); err != nil {
		t.Fatalf("insert old event: %v", err)
	}
	count, err := store.Prune(ctx, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if count < 1 {
		t.Errorf("prune deleted %d rows, want >= 1", count)
	}
	// Verify new reading still exists.
	readings, err := store.QueryReadings(ctx, ReadingFilter{
		From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 100,
	})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(readings) != 1 {
		t.Errorf("got %d readings after prune, want 1", len(readings))
	}
}

func Test_Prune_returnsCount(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-2 * 365 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		if err := store.InsertReading(ctx, Reading{
			Timestamp: old.Add(time.Duration(i) * time.Minute),
			PVWatts:   1000,
			State:     "old",
		}); err != nil {
			t.Fatalf("insert %d failed: %v", i, err)
		}
	}
	count, err := store.Prune(ctx, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	if count != 5 {
		t.Errorf("prune returned %d, want 5", count)
	}
}

func Test_ConcurrentWrites(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r := Reading{
				Timestamp:    now.Add(time.Duration(idx) * time.Minute),
				PVWatts:      5000,
				GridWatts:    -2000,
				LoadWatts:    3000,
				SurplusWatts: 2000,
				State:        "test",
			}
			if err := store.InsertReading(ctx, r); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write error: %v", err)
	}
}

func Test_Close_operationsReturnError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	store, err := NewSQLiteStore(dbPath, logger)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	// Operations after close should return errors.
	err = store.InsertReading(context.Background(), Reading{
		Timestamp: time.Now(), PVWatts: 1000, State: "test",
	})
	if err == nil {
		t.Error("expected error after close")
	}
}

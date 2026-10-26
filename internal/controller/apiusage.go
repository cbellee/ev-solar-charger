package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/storage"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
)

const (
	usageBaselineEventType = "api_usage_baseline"
	// maxUsageSnapshots covers a month of per-minute snapshots.
	maxUsageSnapshots = 100000
)

// LoadAPIUsage returns the current billing month's Tesla API usage from the
// persisted per-minute snapshots. Counters only grow within a month, so it
// takes the maximum of each counter, which ignores zero snapshots written
// while no Tesla client was active. ok is false when there is nothing to
// restore.
//
// When baseline falls in the current billing month it is applied once: the
// counters become the baseline plus the calls recorded since its timestamp.
// A snapshot and an event mark the correction, and later loads only consider
// snapshots from that point, so earlier over-counted snapshots are ignored.
func LoadAPIUsage(ctx context.Context, store storage.Store, baseline *config.UsageBaseline, now time.Time) (tesla.APIUsage, bool, error) {
	monthStart := tesla.BillingMonthStart(now)
	from := monthStart

	if baseline != nil && tesla.BillingMonthStart(baseline.At).Equal(monthStart) && !baseline.At.After(now) {
		appliedAt, applied, err := baselineAppliedAt(ctx, store, *baseline, monthStart, now)
		if err != nil {
			return tesla.APIUsage{}, false, err
		}
		if !applied {
			usage, err := applyBaseline(ctx, store, *baseline, monthStart, now)
			return usage, err == nil, err
		}
		from = appliedAt
	}

	snaps, err := store.QueryAPIUsage(ctx, from, now, maxUsageSnapshots)
	if err != nil {
		return tesla.APIUsage{}, false, err
	}
	if len(snaps) == 0 {
		return tesla.APIUsage{}, false, nil
	}
	var peak storage.APIUsageSnapshot
	for _, s := range snaps {
		peak = maxUsage(peak, s)
	}
	return usageFrom(peak, monthStart), true, nil
}

func baselineAppliedAt(ctx context.Context, store storage.Store, baseline config.UsageBaseline, monthStart, now time.Time) (time.Time, bool, error) {
	events, err := store.QueryEvents(ctx, monthStart, now, usageBaselineEventType, 100, 0)
	if err != nil {
		return time.Time{}, false, err
	}
	for _, e := range events {
		if e.Message == baseline.String() {
			return e.Timestamp, true, nil
		}
	}
	return time.Time{}, false, nil
}

func applyBaseline(ctx context.Context, store storage.Store, baseline config.UsageBaseline, monthStart, now time.Time) (tesla.APIUsage, error) {
	snaps, err := store.QueryAPIUsage(ctx, monthStart, now, maxUsageSnapshots)
	if err != nil {
		return tesla.APIUsage{}, err
	}

	// Calls since the baseline are measured against the last snapshot taken
	// at or before it (or the first one after, when there is none) rather
	// than the monthly peak, which may be an over-counted carry-over.
	var ref, refAfter, peak storage.APIUsageSnapshot
	var haveRef bool
	for _, s := range snaps {
		if s.Timestamp.After(baseline.At) {
			if refAfter.Timestamp.IsZero() || s.Timestamp.Before(refAfter.Timestamp) {
				refAfter = s
			}
			peak = maxUsage(peak, s)
			continue
		}
		if !haveRef || s.Timestamp.After(ref.Timestamp) {
			ref, haveRef = s, true
		}
	}
	if !haveRef {
		ref = refAfter
	}
	peak = maxUsage(peak, ref)

	corrected := storage.APIUsageSnapshot{
		Timestamp:     now,
		DataCalls:     baseline.DataCalls + max(0, peak.DataCalls-ref.DataCalls),
		CommandCalls:  baseline.CommandCalls + max(0, peak.CommandCalls-ref.CommandCalls),
		WakeCalls:     baseline.WakeCalls + max(0, peak.WakeCalls-ref.WakeCalls),
		StreamSignals: peak.StreamSignals,
	}
	usage := usageFrom(corrected, monthStart)
	corrected.EstimatedCost = usage.EstimatedCost

	if err := store.InsertAPIUsage(ctx, corrected); err != nil {
		return tesla.APIUsage{}, err
	}
	if err := store.InsertEvent(ctx, storage.Event{
		Timestamp: now,
		Type:      usageBaselineEventType,
		Message:   baseline.String(),
		Details: fmt.Sprintf("Tesla API usage reset to Tesla's billing figures: data=%d command=%d wake=%d",
			corrected.DataCalls, corrected.CommandCalls, corrected.WakeCalls),
	}); err != nil {
		return tesla.APIUsage{}, err
	}
	return usage, nil
}

func maxUsage(a, b storage.APIUsageSnapshot) storage.APIUsageSnapshot {
	return storage.APIUsageSnapshot{
		DataCalls:     max(a.DataCalls, b.DataCalls),
		CommandCalls:  max(a.CommandCalls, b.CommandCalls),
		WakeCalls:     max(a.WakeCalls, b.WakeCalls),
		StreamSignals: max(a.StreamSignals, b.StreamSignals),
	}
}

func usageFrom(s storage.APIUsageSnapshot, monthStart time.Time) tesla.APIUsage {
	return tesla.APIUsage{
		DataCalls:     s.DataCalls,
		CommandCalls:  s.CommandCalls,
		WakeCalls:     s.WakeCalls,
		StreamSignals: s.StreamSignals,
		MonthStarted:  monthStart,
		EstimatedCost: tesla.EstimateCost(s.DataCalls, s.CommandCalls, s.WakeCalls, s.StreamSignals),
	}
}

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store using a SQLite database.
type SQLiteStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSQLiteStore opens or creates a SQLite database at dbPath.
func NewSQLiteStore(dbPath string, logger *slog.Logger) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("storage: open database: %w", err)
	}
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA cache_size=-64000",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return nil, fmt.Errorf("storage: set pragma %q: %w", p, err)
		}
	}

	return &SQLiteStore{db: db, logger: logger}, nil
}

// Migrate creates tables, indexes, and FTS virtual tables if they do not exist.
func (s *SQLiteStore) Migrate(ctx context.Context) error {
	ddl := `
CREATE TABLE IF NOT EXISTS readings (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp     DATETIME NOT NULL,
    pv_watts      REAL NOT NULL,
    grid_watts    REAL NOT NULL,
    load_watts    REAL NOT NULL,
    surplus_watts REAL NOT NULL,
    charge_amps   INTEGER NOT NULL DEFAULT 0,
    battery_pct   REAL NOT NULL DEFAULT 0,
    state         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_readings_timestamp ON readings(timestamp);

CREATE TABLE IF NOT EXISTS charge_sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    start_time    DATETIME NOT NULL,
    end_time      DATETIME,
    start_battery REAL NOT NULL DEFAULT 0,
    end_battery   REAL NOT NULL DEFAULT 0,
    energy_kwh    REAL NOT NULL DEFAULT 0,
    peak_amps     INTEGER NOT NULL DEFAULT 0,
    avg_amps      REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_sessions_start ON charge_sessions(start_time);

CREATE TABLE IF NOT EXISTS events (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp DATETIME NOT NULL,
    type      TEXT NOT NULL,
    message   TEXT NOT NULL,
    details   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_type ON events(type);

CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
    message, details, content=events, content_rowid=id
);

CREATE TRIGGER IF NOT EXISTS events_ai AFTER INSERT ON events BEGIN
    INSERT INTO events_fts(rowid, message, details) VALUES (new.id, new.message, new.details);
END;
CREATE TRIGGER IF NOT EXISTS events_ad AFTER DELETE ON events BEGIN
    INSERT INTO events_fts(events_fts, rowid, message, details) VALUES ('delete', old.id, old.message, old.details);
END;
`
	_, err := s.db.ExecContext(ctx, ddl)
	if err != nil {
		return fmt.Errorf("storage: migrate: %w", err)
	}
	return nil
}

// InsertReading persists a single averaged reading.
func (s *SQLiteStore) InsertReading(ctx context.Context, r Reading) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO readings (timestamp, pv_watts, grid_watts, load_watts, surplus_watts, charge_amps, battery_pct, state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Timestamp.UTC().Format("2006-01-02 15:04:05"), r.PVWatts, r.GridWatts, r.LoadWatts, r.SurplusWatts, r.ChargeAmps, r.BatteryPct, r.State)
	if err != nil {
		return fmt.Errorf("storage: insert reading: %w", err)
	}
	return nil
}

// QueryReadings retrieves readings matching the filter with optional aggregation.
func (s *SQLiteStore) QueryReadings(ctx context.Context, f ReadingFilter) ([]Reading, error) {
	var query string
	switch f.Interval {
	case "hour":
		query = `SELECT 0, strftime('%Y-%m-%d %H:00:00', timestamp),
			AVG(pv_watts), AVG(grid_watts), AVG(load_watts), AVG(surplus_watts),
			CAST(ROUND(AVG(charge_amps)) AS INTEGER), MAX(battery_pct), MAX(state)
			FROM readings WHERE timestamp BETWEEN ? AND ?
			GROUP BY strftime('%Y-%m-%d %H', timestamp)
			ORDER BY strftime('%Y-%m-%d %H', timestamp) DESC LIMIT ? OFFSET ?`
	case "day":
		query = `SELECT 0, strftime('%Y-%m-%d', timestamp),
			AVG(pv_watts), AVG(grid_watts), AVG(load_watts), AVG(surplus_watts),
			CAST(ROUND(AVG(charge_amps)) AS INTEGER), MAX(battery_pct), MAX(state)
			FROM readings WHERE timestamp BETWEEN ? AND ?
			GROUP BY strftime('%Y-%m-%d', timestamp)
			ORDER BY strftime('%Y-%m-%d', timestamp) DESC LIMIT ? OFFSET ?`
	default:
		query = `SELECT id, timestamp, pv_watts, grid_watts, load_watts, surplus_watts,
			charge_amps, battery_pct, state
			FROM readings WHERE timestamp BETWEEN ? AND ?
			ORDER BY timestamp DESC LIMIT ? OFFSET ?`
	}

	fromStr := f.From.UTC().Format("2006-01-02 15:04:05")
	toStr := f.To.UTC().Format("2006-01-02 15:04:05")
	rows, err := s.db.QueryContext(ctx, query, fromStr, toStr, f.Limit, f.Offset)
	if err != nil {
		return nil, fmt.Errorf("storage: query readings: %w", err)
	}
	defer rows.Close()

	var readings []Reading
	for rows.Next() {
		var r Reading
		var ts sql.NullString
		if err := rows.Scan(&r.ID, &ts, &r.PVWatts, &r.GridWatts, &r.LoadWatts,
			&r.SurplusWatts, &r.ChargeAmps, &r.BatteryPct, &r.State); err != nil {
			return nil, fmt.Errorf("storage: scan reading: %w", err)
		}
		if ts.Valid {
			for _, layout := range []string{
				time.RFC3339Nano,
				time.RFC3339,
				"2006-01-02T15:04:05Z",
				"2006-01-02 15:04:05",
				"2006-01-02 15:04",
				"2006-01-02",
				"2006-01-02 15:04:05-07:00",
				"2006-01-02T15:04:05.999999999-07:00",
			} {
				if t, err := time.Parse(layout, ts.String); err == nil {
					r.Timestamp = t
					break
				}
			}
		}
		readings = append(readings, r)
	}
	if readings == nil {
		readings = []Reading{}
	}
	return readings, rows.Err()
}

// StartSession creates a new charge session and returns its ID.
func (s *SQLiteStore) StartSession(ctx context.Context, cs ChargeSession) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO charge_sessions (start_time, start_battery) VALUES (?, ?)`,
		cs.StartTime.UTC().Format("2006-01-02 15:04:05"), cs.StartBattery)
	if err != nil {
		return 0, fmt.Errorf("storage: start session: %w", err)
	}
	return res.LastInsertId()
}

// EndSession finalizes a charge session.
func (s *SQLiteStore) EndSession(ctx context.Context, id int64, endTime time.Time, endBattery, energyKWh float64, peakAmps int, avgAmps float64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE charge_sessions SET end_time=?, end_battery=?, energy_kwh=?, peak_amps=?, avg_amps=? WHERE id=?`,
		endTime.UTC().Format("2006-01-02 15:04:05"), endBattery, energyKWh, peakAmps, avgAmps, id)
	if err != nil {
		return fmt.Errorf("storage: end session: %w", err)
	}
	return nil
}

// QuerySessions retrieves charge sessions within a date range.
func (s *SQLiteStore) QuerySessions(ctx context.Context, from, to time.Time, limit, offset int) ([]ChargeSession, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, start_time, end_time, start_battery, end_battery, energy_kwh, peak_amps, avg_amps
		 FROM charge_sessions WHERE start_time BETWEEN ? AND ?
		 ORDER BY start_time DESC LIMIT ? OFFSET ?`,
		from.UTC().Format("2006-01-02 15:04:05"), to.UTC().Format("2006-01-02 15:04:05"), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("storage: query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []ChargeSession
	for rows.Next() {
		var cs ChargeSession
		var startStr string
		var endStr sql.NullString
		if err := rows.Scan(&cs.ID, &startStr, &endStr, &cs.StartBattery,
			&cs.EndBattery, &cs.EnergyKWh, &cs.PeakAmps, &cs.AvgAmps); err != nil {
			return nil, fmt.Errorf("storage: scan session: %w", err)
		}
		cs.StartTime, _ = time.Parse("2006-01-02 15:04:05", startStr)
		if endStr.Valid && endStr.String != "" {
			cs.EndTime, _ = time.Parse("2006-01-02 15:04:05", endStr.String)
		}
		sessions = append(sessions, cs)
	}
	if sessions == nil {
		sessions = []ChargeSession{}
	}
	return sessions, rows.Err()
}

// InsertEvent persists an event record.
func (s *SQLiteStore) InsertEvent(ctx context.Context, e Event) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events (timestamp, type, message, details) VALUES (?, ?, ?, ?)`,
		e.Timestamp.UTC().Format("2006-01-02 15:04:05"), e.Type, e.Message, e.Details)
	if err != nil {
		return fmt.Errorf("storage: insert event: %w", err)
	}
	return nil
}

// QueryEvents retrieves events with optional type filter.
func (s *SQLiteStore) QueryEvents(ctx context.Context, from, to time.Time, eventType string, limit, offset int) ([]Event, error) {
	var query string
	var args []any

	fromStr := from.UTC().Format("2006-01-02 15:04:05")
	toStr := to.UTC().Format("2006-01-02 15:04:05")

	if eventType != "" {
		query = `SELECT id, timestamp, type, message, details FROM events
			WHERE timestamp BETWEEN ? AND ? AND type = ?
			ORDER BY timestamp DESC LIMIT ? OFFSET ?`
		args = []any{fromStr, toStr, eventType, limit, offset}
	} else {
		query = `SELECT id, timestamp, type, message, details FROM events
			WHERE timestamp BETWEEN ? AND ?
			ORDER BY timestamp DESC LIMIT ? OFFSET ?`
		args = []any{fromStr, toStr, limit, offset}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Type, &e.Message, &e.Details); err != nil {
			return nil, fmt.Errorf("storage: scan event: %w", err)
		}
		e.Timestamp, _ = time.Parse("2006-01-02 15:04:05", ts)
		events = append(events, e)
	}
	if events == nil {
		events = []Event{}
	}
	return events, rows.Err()
}

// sanitizeFTS5Query strips characters that could cause FTS5 syntax errors.
var ftsSpecialChars = regexp.MustCompile(`[*"(){}[\]:^~!@#$%&|\\/<>+=;,]`)

func sanitizeFTS5Query(query string) string {
	sanitized := ftsSpecialChars.ReplaceAllString(query, " ")
	sanitized = strings.TrimSpace(sanitized)
	if sanitized == "" {
		return ""
	}
	return sanitized
}

// Search performs full-text search across events using FTS5.
func (s *SQLiteStore) Search(ctx context.Context, query string, from, to time.Time, limit int) ([]Event, error) {
	sanitized := sanitizeFTS5Query(query)
	if sanitized == "" {
		return []Event{}, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.timestamp, e.type, e.message, e.details
		 FROM events e
		 JOIN events_fts ON events_fts.rowid = e.id
		 WHERE events_fts MATCH ?
		   AND e.timestamp BETWEEN ? AND ?
		 ORDER BY e.timestamp DESC LIMIT ?`,
		sanitized, from.UTC().Format("2006-01-02 15:04:05"), to.UTC().Format("2006-01-02 15:04:05"), limit)
	if err != nil {
		return nil, fmt.Errorf("storage: search: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Type, &e.Message, &e.Details); err != nil {
			return nil, fmt.Errorf("storage: scan search result: %w", err)
		}
		e.Timestamp, _ = time.Parse("2006-01-02 15:04:05", ts)
		events = append(events, e)
	}
	if events == nil {
		events = []Event{}
	}
	return events, rows.Err()
}

// Prune deletes records older than the specified duration and returns the total rows deleted.
func (s *SQLiteStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan).UTC().Format("2006-01-02 15:04:05")
	var total int64

	res, err := s.db.ExecContext(ctx, `DELETE FROM readings WHERE timestamp < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("storage: prune readings: %w", err)
	}
	n, _ := res.RowsAffected()
	total += n

	res, err = s.db.ExecContext(ctx, `DELETE FROM charge_sessions WHERE end_time IS NOT NULL AND end_time < ?`, cutoff)
	if err != nil {
		return total, fmt.Errorf("storage: prune sessions: %w", err)
	}
	n, _ = res.RowsAffected()
	total += n

	res, err = s.db.ExecContext(ctx, `DELETE FROM events WHERE timestamp < ?`, cutoff)
	if err != nil {
		return total, fmt.Errorf("storage: prune events: %w", err)
	}
	n, _ = res.RowsAffected()
	total += n

	_, err = s.db.ExecContext(ctx, `INSERT INTO events_fts(events_fts) VALUES('rebuild')`)
	if err != nil {
		return total, fmt.Errorf("storage: rebuild fts index: %w", err)
	}

	return total, nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

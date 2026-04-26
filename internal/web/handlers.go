package web

import (
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/cbellee/solar-ev-charger/internal/controller"
	"github.com/cbellee/solar-ev-charger/internal/storage"
)

//go:embed templates/index.html
var templateFS embed.FS

//go:embed images
var imageFS embed.FS

var indexTemplate = template.Must(template.ParseFS(templateFS, "templates/index.html"))

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTemplate.Execute(w, nil); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func handleState(ctrl *controller.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := ctrl.GetStateSnapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	}
}

type controlRequest struct {
	Action string `json:"action"`
	Amps   int    `json:"amps"`
}

func handleControl(ctrl *controller.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req controlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}

		var err error
		switch req.Action {
		case "start":
			err = ctrl.ManualStart(r.Context())
		case "stop":
			err = ctrl.ManualStop(r.Context())
		case "setAmps":
			if req.Amps == 0 {
				http.Error(w, `{"error":"amps is required for setAmps action"}`, http.StatusBadRequest)
				return
			}
			err = ctrl.ManualSetAmps(r.Context(), req.Amps)
		default:
			http.Error(w, `{"error":"invalid action"}`, http.StatusBadRequest)
			return
		}

		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":"ok"}`))
	}
}

type modeRequest struct {
	Mode string `json:"mode"`
}

func handleMode(ctrl *controller.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req modeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}

		switch req.Mode {
		case "auto":
			ctrl.SetMode(controller.ModeAuto)
		case "manual":
			ctrl.SetMode(controller.ModeManual)
		default:
			http.Error(w, `{"error":"invalid mode, must be auto or manual"}`, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":"ok"}`))
	}
}

func handleHistory(store storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		from := parseTimeParam(r, "from", now.Add(-24*time.Hour))
		to := parseTimeParam(r, "to", now)
		interval := r.URL.Query().Get("interval")
		if interval == "" {
			interval = "minute"
		}
		if interval != "minute" && interval != "hour" && interval != "day" {
			http.Error(w, `{"error":"interval must be minute, hour, or day"}`, http.StatusBadRequest)
			return
		}
		limit := parseIntParam(r, "limit", 100)
		if limit > 1000 {
			limit = 1000
		}
		offset := parseIntParam(r, "offset", 0)

		readings, err := store.QueryReadings(r.Context(), storage.ReadingFilter{
			From:     from,
			To:       to,
			Interval: interval,
			Limit:    limit,
			Offset:   offset,
		})
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(readings)
	}
}

func handleSessions(store storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		from := parseTimeParam(r, "from", now.Add(-30*24*time.Hour))
		to := parseTimeParam(r, "to", now)
		limit := parseIntParam(r, "limit", 50)
		offset := parseIntParam(r, "offset", 0)

		sessions, err := store.QuerySessions(r.Context(), from, to, limit, offset)
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}
}

func handleEvents(store storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		from := parseTimeParam(r, "from", now.Add(-24*time.Hour))
		to := parseTimeParam(r, "to", now)
		eventType := r.URL.Query().Get("type")
		limit := parseIntParam(r, "limit", 100)
		offset := parseIntParam(r, "offset", 0)

		events, err := store.QueryEvents(r.Context(), from, to, eventType, limit, offset)
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	}
}

func handleSearch(store storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(w, `{"error":"q parameter is required"}`, http.StatusBadRequest)
			return
		}

		now := time.Now()
		from := parseTimeParam(r, "from", now.Add(-30*24*time.Hour))
		to := parseTimeParam(r, "to", now)
		limit := parseIntParam(r, "limit", 50)

		events, err := store.Search(r.Context(), q, from, to, limit)
		if err != nil {
			http.Error(w, `{"error":"search failed"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// Tesla Fleet API pricing: $10 monthly discount is a shared credit pool
// across all categories, not per-category free tiers.
const (
	costPerData   = 1.0 / 500.0    // $0.002 per data request
	costPerCmd    = 1.0 / 1000.0   // $0.001 per command
	costPerWake   = 1.0 / 50.0     // $0.02 per wake
	costPerStream = 1.0 / 150000.0 // ~$0.0000067 per stream signal
	monthlyCredit = 10.0           // shared $10 discount pool
)

type apiUsageResponse struct {
	DataCalls       int64   `json:"dataCalls"`
	DataCost        float64 `json:"dataCost"`
	CommandCalls    int64   `json:"commandCalls"`
	CommandCost     float64 `json:"commandCost"`
	WakeCalls       int64   `json:"wakeCalls"`
	WakeCost        float64 `json:"wakeCost"`
	StreamSignals   int64   `json:"streamSignals"`
	StreamCost      float64 `json:"streamCost"`
	EstimatedCost   float64 `json:"estimatedCost"`
	MonthlyDiscount float64 `json:"monthlyDiscount"`
	NetCost         float64 `json:"netCost"`
	MonthStarted    string  `json:"monthStarted"`
}

func handleAPIUsage(ctrl *controller.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		usage := ctrl.GetAPIUsage()
		dataCost := float64(usage.DataCalls) * costPerData
		cmdCost := float64(usage.CommandCalls) * costPerCmd
		wakeCost := float64(usage.WakeCalls) * costPerWake
		streamCost := float64(usage.StreamSignals) * costPerStream
		net := usage.EstimatedCost - monthlyCredit
		if net < 0 {
			net = 0
		}
		resp := apiUsageResponse{
			DataCalls:       usage.DataCalls,
			DataCost:        dataCost,
			CommandCalls:    usage.CommandCalls,
			CommandCost:     cmdCost,
			WakeCalls:       usage.WakeCalls,
			WakeCost:        wakeCost,
			StreamSignals:   usage.StreamSignals,
			StreamCost:      streamCost,
			EstimatedCost:   usage.EstimatedCost,
			MonthlyDiscount: monthlyCredit,
			NetCost:         net,
			MonthStarted:    usage.MonthStarted.Format("2006-01-02"),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func handleAPIUsageHistory(store storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		from := parseTimeParam(r, "from", now.Add(-30*24*time.Hour))
		to := parseTimeParam(r, "to", now)
		limit := parseIntParam(r, "limit", 1000)
		if limit > 10000 {
			limit = 10000
		}

		snapshots, err := store.QueryAPIUsage(r.Context(), from, to, limit)
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snapshots)
	}
}

func parseTimeParam(r *http.Request, key string, defaultVal time.Time) time.Time {
	s := r.URL.Query().Get(key)
	if s == "" {
		return defaultVal
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return defaultVal
	}
	return t
}

func parseIntParam(r *http.Request, key string, defaultVal int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return n
}

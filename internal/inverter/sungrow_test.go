package inverter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nhooyr.io/websocket"
)

func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func makeRegisterHandler(paramValue string, resultCode int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"result_code": resultCode,
			"result_msg":  "success",
			"result_data": map[string]any{
				"param_value": paramValue,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func Test_GetPowerData_normal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("param_addr")
		var val string
		switch addr {
		case "5031":
			val = "0,5000" // PV = 5000W
		case "5083":
			val = "65535,63536" // Grid = -2000 (signed, two's complement of -2000 as S32)
		case "5091":
			val = "0,3000" // Load = 3000W
		default:
			val = "0,0"
		}
		resp := map[string]any{
			"result_code": 0,
			"result_msg":  "success",
			"result_data": map[string]any{"param_value": val},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	srv := newTestServer(t, mux)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := New(host, 8082, nil, nil)
	client.token = "test-token"
	client.host = host
	power, err := client.GetPowerData(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if power.PVWatts != 5000 {
		t.Errorf("PVWatts = %f, want 5000", power.PVWatts)
	}
	if power.GridWatts != -2000 {
		t.Errorf("GridWatts = %f, want -2000", power.GridWatts)
	}
	if power.LoadWatts != 3000 {
		t.Errorf("LoadWatts = %f, want 3000", power.LoadWatts)
	}
	if power.SurplusWatts != 2000 {
		t.Errorf("SurplusWatts = %f, want 2000", power.SurplusWatts)
	}
}

func Test_GetPowerData_importing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("param_addr")
		var val string
		switch addr {
		case "5031":
			val = "0,3000"
		case "5083":
			val = "0,1500" // Grid = 1500 (importing)
		case "5091":
			val = "0,4500"
		default:
			val = "0,0"
		}
		resp := map[string]any{
			"result_code": 0,
			"result_msg":  "success",
			"result_data": map[string]any{"param_value": val},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	srv := newTestServer(t, mux)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := New(host, 8082, nil, nil)
	client.token = "test-token"
	client.host = host
	power, err := client.GetPowerData(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if power.SurplusWatts != 0 {
		t.Errorf("SurplusWatts = %f, want 0 (importing)", power.SurplusWatts)
	}
}

func Test_GetPowerData_zeroPV(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"result_code": 0,
			"result_msg":  "success",
			"result_data": map[string]any{"param_value": "0,0"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	srv := newTestServer(t, mux)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := New(host, 8082, nil, nil)
	client.token = "test-token"
	client.host = host
	power, err := client.GetPowerData(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if power.PVWatts != 0 || power.GridWatts != 0 || power.LoadWatts != 0 || power.SurplusWatts != 0 {
		t.Errorf("expected all zeros, got %+v", power)
	}
}

func Test_GetPowerData_tokenExpiry(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		code := 0
		if token == "old-token" {
			code = 106
		}
		resp := map[string]any{
			"result_code": code,
			"result_msg":  "success",
			"result_data": map[string]any{"param_value": "0,1000"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	// Handle WS reconnect on the same server.
	mux.HandleFunc("/ws/home/overview", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		_, _, err = conn.Read(r.Context())
		if err != nil {
			return
		}
		resp := map[string]any{
			"result_code": 0,
			"token":       "new-token",
		}
		data, _ := json.Marshal(resp)
		conn.Write(r.Context(), websocket.MessageText, data)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)
	client := New(parts[0], port, nil, nil)
	client.token = "old-token"
	// readRegister uses http://c.host/... so set host to include port
	client.host = host
	power, err := client.GetPowerData(context.Background())
	if err != nil {
		t.Fatalf("unexpected error (should have retried after token expiry): %v", err)
	}
	_ = power
}

func Test_GetPowerData_malformedJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{bad json`))
	})
	srv := newTestServer(t, mux)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := New(host, 8082, nil, nil)
	client.token = "test-token"
	client.host = host
	_, err := client.GetPowerData(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func Test_GetPowerData_httpError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := newTestServer(t, mux)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := New(host, 8082, nil, nil)
	client.token = "test-token"
	client.host = host
	_, err := client.GetPowerData(context.Background())
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func Test_Connect_success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		_, _, err = conn.Read(r.Context())
		if err != nil {
			return
		}
		resp := map[string]any{
			"result_code": 0,
			"token":       "valid-token-123",
		}
		data, _ := json.Marshal(resp)
		conn.Write(r.Context(), websocket.MessageText, data)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)
	client := New(parts[0], port, nil, nil)
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	if client.token != "valid-token-123" {
		t.Errorf("token = %q, want %q", client.token, "valid-token-123")
	}
}

func Test_Connect_webSocketRefused(t *testing.T) {
	// No server listening on this address.
	client := New("127.0.0.1", 19999, nil, nil)
	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error for refused connection")
	}
}

func Test_Connect_invalidResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		_, _, err = conn.Read(r.Context())
		if err != nil {
			return
		}
		// Send non-JSON.
		conn.Write(r.Context(), websocket.MessageText, []byte("not json"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)
	client := New(parts[0], port, nil, nil)
	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func Test_Close_closesConnectionAndIsIdempotent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		_, _, err = conn.Read(r.Context())
		if err != nil {
			return
		}
		resp := map[string]any{
			"result_code": 0,
			"token":       "valid-token-123",
		}
		data, _ := json.Marshal(resp)
		if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
			return
		}
		_, _, _ = conn.Read(r.Context())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)
	client := New(parts[0], port, nil, nil)
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if client.wsConn != nil {
		t.Fatal("expected wsConn to be cleared after Close")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second close should be nil, got %v", err)
	}
}

func Test_parseRegisterValue_unsigned(t *testing.T) {
	val, err := parseRegisterValue("0,5000", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 5000 {
		t.Errorf("val = %f, want 5000", val)
	}
}

func Test_parseRegisterValue_signed_negative(t *testing.T) {
	// -2000 in int32 = 0xFFFFF830
	// high word = 0xFFFF = 65535, low word = 0xF830 = 63536
	val, err := parseRegisterValue("65535,63536", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != -2000 {
		t.Errorf("val = %f, want -2000", val)
	}
}

func Test_GetPowerData_invalidRegisterValue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("param_addr")
		val := "0,1000"
		if addr == "5031" {
			val = "not-a-number"
		}
		resp := map[string]any{
			"result_code": 0,
			"result_msg":  "success",
			"result_data": map[string]any{"param_value": val},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	srv := newTestServer(t, mux)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := New(host, 8082, nil, nil)
	client.token = "test-token"
	client.host = host

	_, err := client.GetPowerData(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid register value")
	}
	if !strings.Contains(err.Error(), "parse register value") {
		t.Fatalf("expected parse register value error, got %v", err)
	}
}

func Test_parseRegisterValue_singleValueFallback(t *testing.T) {
	val, err := parseRegisterValue("1234", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 1234 {
		t.Errorf("val = %f, want 1234", val)
	}
}

func Test_parseRegisterValue_invalid(t *testing.T) {
	_, err := parseRegisterValue("bad-value", false)
	if err == nil {
		t.Fatal("expected error for invalid register value")
	}
}

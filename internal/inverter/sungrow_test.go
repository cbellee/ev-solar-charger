package inverter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"nhooyr.io/websocket"
)

// wsTestHandler serves WebSocket connections, handling "connect" and "devicelist" services.
func wsTestHandler(token string, devType, devCode int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		for {
			_, msg, err := conn.Read(r.Context())
			if err != nil {
				return
			}

			var req map[string]string
			if err := json.Unmarshal(msg, &req); err != nil {
				return
			}

			var resp map[string]any
			switch req["service"] {
			case "connect":
				resp = map[string]any{
					"result_code": 1,
					"result_msg":  "success",
					"result_data": map[string]any{"token": token},
				}
			case "devicelist":
				resp = map[string]any{
					"result_code": 1,
					"result_msg":  "success",
					"result_data": map[string]any{
						"list": []map[string]any{
							{"dev_type": devType, "dev_code": devCode},
						},
					},
				}
			default:
				resp = map[string]any{
					"result_code": 1,
					"result_msg":  "success",
					"result_data": map[string]any{},
				}
			}

			data, _ := json.Marshal(resp)
			conn.Write(r.Context(), websocket.MessageText, data)
		}
	}
}

// httpGetParamHandler serves HTTP GET /device/getParam returning hex register values.
// registerValues maps param_addr → hex byte string (e.g. "13 88 00 00 ").
func httpGetParamHandler(registerValues map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("param_addr")
		val, ok := registerValues[addr]
		if !ok {
			val = "00 00 00 00 "
		}
		resp := map[string]any{
			"result_code": 1,
			"result_msg":  "success",
			"result_data": map[string]any{"param_value": val},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// newTestClient creates a test server with WS (connect+devicelist) and HTTP (getParam)
// endpoints and returns a connected SungrowClient.
func newTestClient(t *testing.T, registerValues map[string]string) *SungrowClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", wsTestHandler("test-token", 1, 0))
	mux.HandleFunc("/device/getParam", httpGetParamHandler(registerValues))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)
	client := New(parts[0], port, nil, nil)
	// Override host to include port so HTTP requests go to the test server.
	client.host = host
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return client
}

func Test_GetPowerData_normal(t *testing.T) {
	// Hex encoding: word0=low, word1=high (Sungrow returns low register first)
	// PV 5000W: U32 0x00001388 → low=0x1388, high=0x0000 → "13 88 00 00 "
	// Grid -2000: S32 0xFFFFF830 → low=0xF830, high=0xFFFF → "F8 30 FF FF "
	// Load 3000W: U32 0x00000BB8 → low=0x0BB8, high=0x0000 → "0B B8 00 00 "
	client := newTestClient(t, map[string]string{
		"5031": "13 88 00 00 ",
		"5083": "F8 30 FF FF ",
		"5091": "0B B8 00 00 ",
	})
	defer client.Close()

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
	client := newTestClient(t, map[string]string{
		"5031": "0B B8 00 00 ", // PV = 3000
		"5083": "05 DC 00 00 ", // Grid = 1500 (importing)
		"5091": "11 94 00 00 ", // Load = 4500
	})
	defer client.Close()

	power, err := client.GetPowerData(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if power.SurplusWatts != 0 {
		t.Errorf("SurplusWatts = %f, want 0 (importing)", power.SurplusWatts)
	}
}

func Test_GetPowerData_zeroPV(t *testing.T) {
	client := newTestClient(t, nil)
	defer client.Close()

	power, err := client.GetPowerData(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if power.PVWatts != 0 || power.GridWatts != 0 || power.LoadWatts != 0 || power.SurplusWatts != 0 {
		t.Errorf("expected all zeros, got %+v", power)
	}
}

func Test_GetPowerData_tokenExpiry(t *testing.T) {
	var expired atomic.Bool
	expired.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		for {
			_, msg, err := conn.Read(r.Context())
			if err != nil {
				return
			}

			var req map[string]string
			json.Unmarshal(msg, &req)

			var resp map[string]any
			switch req["service"] {
			case "connect":
				expired.Store(false)
				resp = map[string]any{
					"result_code": 1,
					"result_msg":  "success",
					"result_data": map[string]any{"token": "new-token"},
				}
			case "devicelist":
				resp = map[string]any{
					"result_code": 1,
					"result_msg":  "success",
					"result_data": map[string]any{
						"list": []map[string]any{
							{"dev_type": 1, "dev_code": 0},
						},
					},
				}
			default:
				resp = map[string]any{
					"result_code": 1,
					"result_msg":  "success",
					"result_data": map[string]any{},
				}
			}

			data, _ := json.Marshal(resp)
			conn.Write(r.Context(), websocket.MessageText, data)
		}
	})
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		var resp map[string]any
		if expired.Load() {
			resp = map[string]any{
				"result_code": 106,
				"result_msg":  "token expired",
				"result_data": map[string]any{"param_value": ""},
			}
		} else {
			resp = map[string]any{
				"result_code": 1,
				"result_msg":  "success",
				"result_data": map[string]any{"param_value": "03 E8 00 00 "},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)
	client := New(parts[0], port, nil, nil)
	client.host = host

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	expired.Store(true)

	power, err := client.GetPowerData(context.Background())
	if err != nil {
		t.Fatalf("unexpected error (should have retried after token expiry): %v", err)
	}
	_ = power
}

func Test_GetPowerData_malformedJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", wsTestHandler("test-token", 1, 0))
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{bad json`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)
	client := New(parts[0], port, nil, nil)
	client.host = host
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	_, err := client.GetPowerData(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func Test_GetPowerData_notConnected(t *testing.T) {
	client := New("127.0.0.1", 0, nil, nil)
	_, err := client.GetPowerData(context.Background())
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got %v", err)
	}
}

func Test_Connect_success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", wsTestHandler("valid-token-123", 21, 3000))
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
	if client.devType != "21" {
		t.Errorf("devType = %q, want %q", client.devType, "21")
	}
	if client.devCode != "3000" {
		t.Errorf("devCode = %q, want %q", client.devCode, "3000")
	}
}

func Test_Connect_refused(t *testing.T) {
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
	client := newTestClient(t, nil)

	if err := client.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if client.token != "" {
		t.Fatal("expected token to be cleared after Close")
	}
	if client.wsConn != nil {
		t.Fatal("expected wsConn to be cleared after Close")
	}
	if client.devType != "" {
		t.Fatal("expected devType to be cleared after Close")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second close should be nil, got %v", err)
	}
}

func Test_GetPowerData_invalidRegisterValue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", wsTestHandler("test-token", 1, 0))
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("param_addr")
		val := "03 E8 00 00 "
		if addr == "5031" {
			val = "ZZ XX "
		}
		resp := map[string]any{
			"result_code": 1,
			"result_msg":  "success",
			"result_data": map[string]any{"param_value": val},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)
	client := New(parts[0], port, nil, nil)
	client.host = host
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	_, err := client.GetPowerData(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid register value")
	}
	if !strings.Contains(err.Error(), "parse register value") {
		t.Fatalf("expected parse register value error, got %v", err)
	}
}

func Test_parseHexRegisterValue_unsigned32(t *testing.T) {
	val, err := parseHexRegisterValue("13 88 00 00 ", 2, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 5000 {
		t.Errorf("val = %f, want 5000", val)
	}
}

func Test_parseHexRegisterValue_signed32_negative(t *testing.T) {
	// -2000 as S32 = 0xFFFFF830
	// -2000 as S32 = 0xFFFFF830 → low=0xF830, high=0xFFFF
	val, err := parseHexRegisterValue("F8 30 FF FF ", 2, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != -2000 {
		t.Errorf("val = %f, want -2000", val)
	}
}

func Test_parseHexRegisterValue_single_register(t *testing.T) {
	// Single U16: 0x04D2 = 1234
	val, err := parseHexRegisterValue("04 D2 ", 1, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 1234 {
		t.Errorf("val = %f, want 1234", val)
	}
}

func Test_parseHexRegisterValue_invalid(t *testing.T) {
	_, err := parseHexRegisterValue("ZZ XX ", 1, false)
	if err == nil {
		t.Fatal("expected error for invalid hex value")
	}
}

func Test_parseHexRegisterValue_empty(t *testing.T) {
	_, err := parseHexRegisterValue("", 1, false)
	if err == nil {
		t.Fatal("expected error for empty value")
	}
}

func Test_parseHexRegisterValue_trailingSpace(t *testing.T) {
	// Should handle trailing space gracefully.
	val, err := parseHexRegisterValue("  13 88 00 00  ", 2, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 5000 {
		t.Errorf("val = %f, want 5000", val)
	}
}

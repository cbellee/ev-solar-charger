package inverter

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"nhooyr.io/websocket"
)

func TestLiveWiNetTLS(t *testing.T) {
	if os.Getenv("WINET_LIVE_TEST") != "1" {
		t.Skip("opt-in read-only hardware test")
	}
	cfg := config.Config{
		SungrowHost: os.Getenv("SUNGROW_HOST"), SungrowPort: 443, SungrowTLS: true,
		SungrowCertSHA256: os.Getenv("SUNGROW_CERT_SHA256"),
		SungrowUsername:   os.Getenv("SUNGROW_USERNAME"), SungrowPassword: os.Getenv("SUNGROW_PASSWORD"),
	}
	if cfg.SungrowHost == "" || cfg.SungrowCertSHA256 == "" || cfg.SungrowUsername == "" || cfg.SungrowPassword == "" {
		t.Fatal("live test requires host, verified certificate fingerprint, username and password environment variables")
	}
	client, err := NewConfigured(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("live client cleanup: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.ping(ctx); err != nil {
		t.Fatalf("keepalive ping: %v", err)
	}
	client.mu.Lock()
	sessionToken := client.token
	client.mu.Unlock()
	power, err := client.GetPowerData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	reused := client.token == sessionToken
	client.mu.Unlock()
	if !reused {
		t.Fatal("keepalive caused an unnecessary re-login")
	}
	for _, watts := range []float64{power.PVWatts, power.GridWatts, power.LoadWatts, power.SurplusWatts} {
		if math.IsNaN(watts) || math.IsInf(watts, 0) {
			t.Fatal("non-finite power reading")
		}
	}
	t.Logf("Read-only hardware check: PV %.0f W, grid %.0f W, load %.0f W, surplus %.0f W",
		power.PVWatts, power.GridWatts, power.LoadWatts, power.SurplusWatts)
}

func TestSecureSession(t *testing.T) {
	for _, tc := range []struct {
		name     string
		badPin   bool
		badLogin bool
		noPin    bool
		expire   bool
		http401  bool
		code401  bool
	}{
		{name: "authenticated reads"},
		{name: "certificate mismatch", badPin: true},
		{name: "login rejection", badLogin: true},
		{name: "untrusted certificate", noPin: true},
		{name: "expired session reauthenticates", expire: true},
		{name: "HTTP unauthorized reauthenticates", expire: true, http401: true},
		{name: "protocol unauthorized reauthenticates", expire: true, code401: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logins, reads atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/ws/home/overview", func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() {
					if err := conn.CloseNow(); err != nil {
						t.Logf("test socket cleanup: %v", err)
					}
				}()
				authenticated := false
				for {
					_, payload, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					var req map[string]string
					if err := json.Unmarshal(payload, &req); err != nil {
						t.Error(err)
						return
					}
					code := 1
					data := map[string]any{"service": req["service"]}
					switch req["service"] {
					case "connect":
						data["token"] = "anonymous"
					case "login":
						if req["username"] != "test-admin" || req["passwd"] != "test-password" || req["token"] != "anonymous" || tc.badLogin {
							code = 401
						} else {
							authenticated = true
							data["token"] = fmt.Sprintf("authenticated-%d", logins.Add(1))
						}
					case "devicelist":
						if !authenticated || !strings.HasPrefix(req["token"], "authenticated-") {
							code = 401
						} else {
							data["list"] = []map[string]int{{"dev_type": 21, "dev_code": 9736}}
						}
					case "ping":
						if req["token"] != fmt.Sprintf("authenticated-%d", logins.Load()) {
							code = 401
						}
					default:
						t.Errorf("unexpected service %q", req["service"])
						return
					}
					message := "success"
					if code != 1 {
						message = "rejected"
					}
					resp, err := json.Marshal(map[string]any{"result_code": code, "result_msg": message, "result_data": data})
					if err != nil {
						t.Error(err)
						return
					}
					if err := conn.Write(r.Context(), websocket.MessageText, resp); err != nil {
						return
					}
				}
			})
			mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				want := fmt.Sprintf("authenticated-%d", logins.Load())
				if query.Get("token") != want || query.Get("dev_type") != "21" || query.Get("dev_code") != "9736" {
					t.Error("register read did not use authenticated discovery")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if tc.expire && reads.Add(1) == 1 {
					if tc.http401 {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					code := 106
					if tc.code401 {
						code = 401
					}
					if _, err := fmt.Fprintf(w, `{"result_code":%d}`, code); err != nil {
						t.Error(err)
					}
					return
				}
				httpGetParamHandler(map[string]string{
					"5031": "13 88 00 00 ", "5083": "F8 30 FF FF ", "5091": "0B B8 00 00 ",
				})(w, r)
			})
			server := httptest.NewTLSServer(mux)
			defer server.Close()
			sum := sha256.Sum256(server.Certificate().Raw)
			pin := hex.EncodeToString(sum[:])
			if tc.badPin {
				pin = strings.Repeat("0", 64)
			}
			if tc.noPin {
				pin = ""
			}
			client, err := NewConfigured(config.Config{
				SungrowHost: strings.TrimPrefix(server.URL, "https://"), SungrowPort: 443,
				SungrowTLS: true, SungrowCertSHA256: pin,
				SungrowUsername: "test-admin", SungrowPassword: "test-password",
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := client.Close(); err != nil {
					t.Logf("test client cleanup: %v", err)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = client.Connect(ctx)
			if tc.badPin || tc.badLogin || tc.noPin {
				if err == nil {
					t.Fatal("expected connection rejection")
				}
				if strings.Contains(err.Error(), "test-password") {
					t.Fatal("password leaked in error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			power, err := client.GetPowerData(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if power.PVWatts != 5000 || power.GridWatts != -2000 || power.LoadWatts != 3000 || power.SurplusWatts != 2000 {
				t.Fatalf("unexpected power: %+v", power)
			}
			wantLogins := int32(1)
			if tc.expire {
				wantLogins = 2
			}
			if logins.Load() != wantLogins {
				t.Fatalf("logins = %d, want %d", logins.Load(), wantLogins)
			}
		})
	}
}

func TestPinnedCertificateValidity(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cert := server.Certificate()
	sum := sha256.Sum256(cert.Raw)
	client, err := NewConfigured(config.Config{
		SungrowTLS: true, SungrowCertSHA256: hex.EncodeToString(sum[:]),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.httpClient.Transport.(*sungrowTransport)
	if !ok {
		t.Fatal("missing Sungrow transport")
	}
	base, ok := transport.rt.(*http.Transport)
	if !ok {
		t.Fatal("missing HTTP transport")
	}
	for _, tc := range []struct {
		name       string
		notBefore  time.Time
		notAfter   time.Time
		wantReject bool
	}{
		{"valid", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false},
		{"expired", time.Now().Add(-2 * time.Hour), time.Now().Add(-time.Hour), true},
		{"not yet valid", time.Now().Add(time.Hour), time.Now().Add(2 * time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert.NotBefore, cert.NotAfter = tc.notBefore, tc.notAfter
			err := base.TLSClientConfig.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}})
			if (err != nil) != tc.wantReject {
				t.Fatalf("validity rejection = %v, want %v", err, tc.wantReject)
			}
		})
	}
}

func TestRegisterErrorRedactsToken(t *testing.T) {
	client := New("127.0.0.1:0", 0, nil, nil)
	client.token = "private-session-token"
	_, err := client.readRegister(context.Background(), 5031, 2, false)
	if err == nil {
		t.Fatal("expected transport failure")
	}
	if strings.Contains(err.Error(), client.token) {
		t.Fatal("session token leaked in transport error")
	}
}

func TestConfiguredLegacyClientConnectsOnRead(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", wsTestHandler("legacy-token", 21, 9736))
	mux.HandleFunc("/device/getParam", httpGetParamHandler(nil))
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := NewConfigured(config.Config{
		SungrowHost: strings.TrimPrefix(server.URL, "http://"), SungrowPort: 8082,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := client.GetPowerData(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidSecureConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
	}{
		{"missing password", config.Config{SungrowTLS: true, SungrowUsername: "admin"}},
		{"cleartext password", config.Config{SungrowUsername: "admin", SungrowPassword: "secret"}},
		{"cleartext pin", config.Config{SungrowCertSHA256: strings.Repeat("0", 64)}},
		{"invalid pin hex", config.Config{SungrowTLS: true, SungrowCertSHA256: strings.Repeat("z", 64)}},
		{"short pin", config.Config{SungrowTLS: true, SungrowCertSHA256: "abcd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewConfigured(tc.cfg, nil, nil); err == nil {
				t.Fatal("expected invalid connection configuration to fail")
			}
		})
	}
}

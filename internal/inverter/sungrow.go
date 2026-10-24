package inverter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/observability"
	"nhooyr.io/websocket"
)

const (
	// sessionResetAfter is the number of consecutive failed polls on an
	// established session before it is abandoned and a fresh login is made.
	// The WiNet-S can keep answering result_code 120 for a stale session
	// indefinitely; a new session recovers it.
	sessionResetAfter = 3
	// reconnectBackoffBase and reconnectBackoffMax bound the delay between
	// repeated session resets so a genuinely faulty module is not hammered
	// with logins.
	reconnectBackoffBase = 30 * time.Second
	reconnectBackoffMax  = 5 * time.Minute
	// pingTimeout bounds the WebSocket keepalive round trip.
	pingTimeout = 5 * time.Second
	// defaultReadGap spaces out register reads for the WiNet-S's slow gateway.
	defaultReadGap = 100 * time.Millisecond
)

// SungrowClient communicates with a Sungrow inverter via the WiNet-S dongle.
// Architecture (per SungrowModbusWebClient reference implementation):
//   - Legacy: WS on port 8082, register reads over HTTP on port 80.
//   - Secure: WSS/HTTPS on port 443, with login before device discovery.
type SungrowClient struct {
	host       string
	port       int
	token      string
	devType    string
	devCode    string
	wsConn     *websocket.Conn
	httpClient *http.Client
	logger     *slog.Logger
	metrics    *observability.Metrics
	mu         sync.Mutex
	secure     bool
	username   string
	password   string

	readGap time.Duration
	now     func() time.Time
	// failedPolls counts consecutive failed polls on the current session.
	failedPolls int
	// resets counts session resets/failed connects since the last good poll.
	resets      int
	nextConnect time.Time
}

// NewConfigured creates a client supporting legacy or authenticated TLS firmware.
// A certificate pin explicitly trusts a leaf certificate, including factory certificates
// that lack a hostname SAN. Without a pin, standard PKI verification is used.
func NewConfigured(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) (*SungrowClient, error) {
	if (cfg.SungrowUsername == "") != (cfg.SungrowPassword == "") {
		return nil, fmt.Errorf("sungrow: username and password must both be set")
	}
	if !cfg.SungrowTLS && (cfg.SungrowUsername != "" || cfg.SungrowCertSHA256 != "") {
		return nil, fmt.Errorf("sungrow: credentials and certificate pin require TLS")
	}
	client := New(cfg.SungrowHost, cfg.SungrowPort, logger, metrics)
	client.secure = cfg.SungrowTLS
	client.username = cfg.SungrowUsername
	client.password = cfg.SungrowPassword
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.SungrowCertSHA256 != "" {
		pin, err := hex.DecodeString(strings.ReplaceAll(cfg.SungrowCertSHA256, ":", ""))
		if err != nil || len(pin) != sha256.Size {
			return nil, fmt.Errorf("sungrow: SUNGROW_CERT_SHA256 must contain a SHA-256 certificate fingerprint")
		}
		// Pin verification replaces PKI/hostname verification for this client only.
		transport.TLSClientConfig.InsecureSkipVerify = true
		transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("sungrow: server supplied no certificate")
			}
			cert := state.PeerCertificates[0]
			sum := sha256.Sum256(cert.Raw)
			if !bytes.Equal(sum[:], pin) {
				return fmt.Errorf("sungrow: server certificate does not match SUNGROW_CERT_SHA256")
			}
			now := time.Now()
			if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return fmt.Errorf("sungrow: pinned certificate is outside its validity period")
			}
			return nil
		}
	}
	client.httpClient.Transport = &sungrowTransport{rt: transport}
	client.httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client, nil
}

// sungrowTransport wraps an http.RoundTripper to fix WebSocket header casing.
// The Sungrow WiNet-S dongle rejects handshakes when Go's net/http
// canonicalises Sec-WebSocket-Key/Version to Sec-Websocket-Key/Version.
type sungrowTransport struct {
	rt http.RoundTripper
}

func (t *sungrowTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for key, vals := range req.Header {
		if len(key) >= 14 && strings.EqualFold(key[:14], "Sec-Websocket-") {
			corrected := "Sec-WebSocket-" + key[14:]
			if corrected != key {
				req.Header.Del(key)
				for _, v := range vals {
					req.Header[corrected] = append(req.Header[corrected], v)
				}
			}
		}
	}
	return t.rt.RoundTrip(req)
}

// New creates a new SungrowClient.
func New(host string, port int, logger *slog.Logger, metrics *observability.Metrics) *SungrowClient {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DisableCompression = true
	return &SungrowClient{
		host:       host,
		port:       port,
		httpClient: &http.Client{Timeout: 10 * time.Second, Transport: &sungrowTransport{rt: base}},
		logger:     logger,
		metrics:    metrics,
		readGap:    defaultReadGap,
		now:        time.Now,
	}
}

// wsMessage is the generic WebSocket request/response envelope.
type wsMessage struct {
	ResultCode int             `json:"result_code"`
	ResultMsg  string          `json:"result_msg"`
	ResultData json.RawMessage `json:"result_data"`
}

type connectData struct {
	Token string `json:"token"`
}

type deviceListData struct {
	List []struct {
		DevType int `json:"dev_type"`
		DevCode int `json:"dev_code"`
	} `json:"list"`
}

// Connect establishes a WebSocket connection, obtains a session token,
// and queries the device list for dev_type and dev_code.
func (c *SungrowClient) Connect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()

	// Close any existing connection.
	c.closeSessionLocked("reconnecting")

	scheme := "ws"
	if c.secure {
		scheme = "wss"
	}
	wsURL := scheme + "://" + c.socketAddress() + "/ws/home/overview"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient:      c.httpClient,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return fmt.Errorf("sungrow: connect: %w", err)
	}

	// Step 1: Authenticate and get token.
	token, err := c.wsRPC(ctx, conn, map[string]string{
		"lang": "en_us", "token": "", "service": "connect",
	})
	if err != nil {
		conn.Close(websocket.StatusInternalError, "connect failed")
		return fmt.Errorf("sungrow: connect: %w", err)
	}

	var cd connectData
	if err := json.Unmarshal(token, &cd); err != nil || cd.Token == "" {
		conn.Close(websocket.StatusInternalError, "no token")
		return fmt.Errorf("sungrow: connect: no token in response")
	}
	if c.username != "" {
		login, err := c.wsRPC(ctx, conn, map[string]string{
			"lang": "en_us", "token": cd.Token, "service": "login",
			"username": c.username, "passwd": c.password,
		})
		if err != nil {
			if closeErr := conn.Close(websocket.StatusNormalClosure, "login failed"); closeErr != nil {
				c.logger.WarnContext(ctx, "sungrow: close after login failure", "error", closeErr)
			}
			return fmt.Errorf("sungrow: login: %w", err)
		}
		var session connectData
		if err := json.Unmarshal(login, &session); err != nil || session.Token == "" {
			if closeErr := conn.Close(websocket.StatusNormalClosure, "no login token"); closeErr != nil {
				c.logger.WarnContext(ctx, "sungrow: close after missing login token", "error", closeErr)
			}
			return fmt.Errorf("sungrow: login: no token in response")
		}
		cd.Token = session.Token
	}

	// Step 2: Query device list to get dev_type and dev_code.
	devData, err := c.wsRPC(ctx, conn, map[string]string{
		"lang": "en_us", "token": cd.Token, "service": "devicelist",
		"type": "0", "is_check_token": "0",
	})
	if err != nil {
		conn.Close(websocket.StatusInternalError, "devicelist failed")
		return fmt.Errorf("sungrow: devicelist: %w", err)
	}

	var dl deviceListData
	if err := json.Unmarshal(devData, &dl); err != nil || len(dl.List) == 0 {
		conn.Close(websocket.StatusInternalError, "no devices")
		return fmt.Errorf("sungrow: devicelist: no devices found")
	}

	c.token = cd.Token
	c.devType = fmt.Sprintf("%d", dl.List[0].DevType)
	c.devCode = fmt.Sprintf("%d", dl.List[0].DevCode)
	c.wsConn = conn
	c.logger.InfoContext(ctx, "sungrow: connected",
		"host", c.host, "dev_type", c.devType, "dev_code", c.devCode)
	return nil
}

func (c *SungrowClient) socketAddress() string {
	if _, _, err := net.SplitHostPort(c.host); err == nil {
		return c.host
	}
	return net.JoinHostPort(c.host, strconv.Itoa(c.port))
}

// wsRPC sends a JSON message over WebSocket and returns the result_data.
func (c *SungrowClient) wsRPC(ctx context.Context, conn *websocket.Conn, msg map[string]string) (json.RawMessage, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var resp wsMessage
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	if resp.ResultMsg != "success" {
		return nil, &resultError{Code: resp.ResultCode, Msg: resp.ResultMsg}
	}

	return resp.ResultData, nil
}

// resultError is a non-success WiNet-S WebSocket reply.
type resultError struct {
	Code int
	Msg  string
}

func (e *resultError) Error() string {
	return fmt.Sprintf("result_code %d: %s", e.Code, e.Msg)
}

type registerResponse struct {
	ResultCode int    `json:"result_code"`
	ResultMsg  string `json:"result_msg"`
	ResultData struct {
		ParamValue string `json:"param_value"`
	} `json:"result_data"`
}

// GetPowerData reads Modbus registers via the WiNet-S HTTP API.
// It keeps the WiNet-S session alive, and abandons a session that keeps
// failing so the next poll logs in again (with backoff between resets).
func (c *SungrowClient) GetPowerData(ctx context.Context) (PowerData, error) {
	data, err := c.pollPowerData(ctx)
	c.recordPoll(ctx, err)
	return data, err
}

func (c *SungrowClient) pollPowerData(ctx context.Context) (PowerData, error) {
	if err := c.ensureSession(ctx); err != nil {
		return PowerData{}, err
	}
	pvWatts, err := c.readRegisterWithRetry(ctx, 5031, 2, false)
	if err != nil {
		return PowerData{}, fmt.Errorf("sungrow: read pv power: %w", err)
	}
	if err := c.pause(ctx); err != nil {
		return PowerData{}, err
	}

	gridWatts, err := c.readRegisterWithRetry(ctx, 5083, 2, true)
	if err != nil {
		return PowerData{}, fmt.Errorf("sungrow: read grid power: %w", err)
	}
	if err := c.pause(ctx); err != nil {
		return PowerData{}, err
	}

	loadWatts, err := c.readRegisterWithRetry(ctx, 5091, 2, true)
	if err != nil {
		return PowerData{}, fmt.Errorf("sungrow: read load power: %w", err)
	}

	surplusWatts := math.Max(0, -gridWatts)

	if c.metrics != nil {
		c.metrics.PVPower.Record(ctx, pvWatts)
		c.metrics.GridPower.Record(ctx, gridWatts)
		c.metrics.LoadPower.Record(ctx, loadWatts)
		c.metrics.SurplusPower.Record(ctx, surplusWatts)
	}

	return PowerData{
		PVWatts:      pvWatts,
		GridWatts:    gridWatts,
		LoadWatts:    loadWatts,
		SurplusWatts: surplusWatts,
		Timestamp:    time.Now(),
	}, nil
}

func (c *SungrowClient) readRegisterWithRetry(ctx context.Context, addr, count int, signed bool) (float64, error) {
	val, err := c.readRegister(ctx, addr, count, signed)
	if err == nil {
		return val, nil
	}

	if c.isTokenExpiry(err) {
		c.logger.InfoContext(ctx, "sungrow: token expired, reconnecting")
		if reconnErr := c.replaceSession(ctx, "token expired"); reconnErr != nil {
			return 0, fmt.Errorf("sungrow: reconnect after token expiry: %w", reconnErr)
		}
		return c.readRegister(ctx, addr, count, signed)
	}
	return 0, err
}

// ensureSession verifies the current session with a keepalive ping, or
// establishes a new one when there is none or it has died.
func (c *SungrowClient) ensureSession(ctx context.Context) error {
	c.mu.Lock()
	connected := c.token != ""
	c.mu.Unlock()
	if connected {
		err := c.ping(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("sungrow: keepalive: %w", err)
		}
		c.logger.WarnContext(ctx, "sungrow: keepalive failed, reconnecting", "error", err)
		return c.replaceSession(ctx, "keepalive failed")
	}

	if err := c.waitForBackoff(); err != nil {
		return err
	}
	return c.reconnect(ctx)
}

// replaceSession abandons an established session and logs in again. Each
// replacement counts towards backoff so a module that accepts logins but keeps
// rejecting the sessions is not flooded with logins.
func (c *SungrowClient) replaceSession(ctx context.Context, reason string) error {
	c.mu.Lock()
	c.closeSessionLocked(reason)
	c.scheduleReconnectLocked()
	c.mu.Unlock()
	if err := c.waitForBackoff(); err != nil {
		return err
	}
	return c.reconnect(ctx)
}

func (c *SungrowClient) waitForBackoff() error {
	c.mu.Lock()
	wait := c.nextConnect.Sub(c.now())
	c.mu.Unlock()
	if wait > 0 {
		return fmt.Errorf("sungrow: waiting %s before reconnecting", wait.Round(time.Second))
	}
	return nil
}

// reconnect logs in again and schedules backoff if that fails.
func (c *SungrowClient) reconnect(ctx context.Context) error {
	err := c.Connect(ctx)
	if err != nil && ctx.Err() == nil {
		c.mu.Lock()
		c.scheduleReconnectLocked()
		c.mu.Unlock()
	}
	return err
}

// ping sends the WiNet-S keepalive the vendor web UI sends. Reading the reply
// also services WebSocket control frames, which are otherwise never read.
// It fails only when the socket is dead or the session token is rejected.
func (c *SungrowClient) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wsConn == nil {
		return fmt.Errorf("sungrow: ping: no websocket")
	}
	_, err := c.wsRPC(ctx, c.wsConn, map[string]string{
		"lang": "en_us", "service": "ping", "token": c.token,
		"id": strconv.FormatInt(c.now().UnixNano(), 36),
	})
	var result *resultError
	if errors.As(err, &result) && result.Code != 106 && result.Code != 401 {
		c.logger.DebugContext(ctx, "sungrow: ping not acknowledged", "result_code", result.Code)
		return nil
	}
	if err != nil {
		return fmt.Errorf("sungrow: ping: %w", err)
	}
	return nil
}

// recordPoll tracks consecutive failures on an established session and
// abandons the session once it looks stale.
func (c *SungrowClient) recordPoll(ctx context.Context, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.failedPolls, c.resets, c.nextConnect = 0, 0, time.Time{}
		return
	}
	// Cancellation is not a module fault, and connect failures are already
	// scheduled by reconnect.
	if ctx.Err() != nil || c.token == "" {
		return
	}
	c.failedPolls++
	if c.failedPolls < sessionResetAfter {
		return
	}
	c.scheduleReconnectLocked()
	c.closeSessionLocked("session reset")
	c.logger.WarnContext(ctx, "sungrow: abandoning session after repeated failures",
		"failed_polls", sessionResetAfter, "retry_in", c.nextConnect.Sub(c.now()).Round(time.Second), "error", err)
}

// scheduleReconnectLocked applies exponential backoff: the first reset
// reconnects on the next poll, later ones wait 30s, 60s, ... up to 5m.
func (c *SungrowClient) scheduleReconnectLocked() {
	c.resets++
	c.failedPolls = 0
	var delay time.Duration
	if c.resets > 1 {
		delay = reconnectBackoffBase << min(c.resets-2, 16)
		delay = min(delay, reconnectBackoffMax)
	}
	c.nextConnect = c.now().Add(delay)
}

// closeSessionLocked drops the WebSocket and session token.
func (c *SungrowClient) closeSessionLocked(reason string) {
	c.token = ""
	c.devType = ""
	c.devCode = ""
	if c.wsConn != nil {
		if err := c.wsConn.Close(websocket.StatusNormalClosure, reason); err != nil {
			c.logger.Debug("sungrow: close websocket", "error", err)
		}
		c.wsConn = nil
	}
}

func (c *SungrowClient) pause(ctx context.Context) error {
	if c.readGap <= 0 {
		return nil
	}
	timer := time.NewTimer(c.readGap)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("sungrow: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

var errTokenExpired = fmt.Errorf("sungrow: token expired")

func (c *SungrowClient) isTokenExpiry(err error) bool {
	return errors.Is(err, errTokenExpired)
}

// readRegister reads Modbus registers via HTTP(S) GET.
// Per SungrowModbusWebClient, register reads use the HTTP API with
// dev_type and dev_code obtained during WebSocket connect.
// param_type: 0 = input registers (FC4), 1 = holding registers (FC3).
// The registers we use (5xxx) are input registers → param_type=0.
func (c *SungrowClient) readRegister(ctx context.Context, addr, count int, signed bool) (float64, error) {
	c.mu.Lock()
	token := c.token
	devType := c.devType
	devCode := c.devCode
	c.mu.Unlock()

	if token == "" {
		return 0, fmt.Errorf("sungrow: read register %d: not connected", addr)
	}

	scheme, address := "http", c.host
	if c.secure {
		scheme, address = "https", c.socketAddress()
	}
	registerURL := fmt.Sprintf("%s://%s/device/getParam?dev_id=1&dev_type=%s&dev_code=%s"+
		"&type=3&param_addr=%d&param_num=%d&param_type=0"+
		"&token=%s&lang=en_us&time123456=%d",
		scheme, address, devType, devCode, addr, count, token, time.Now().Unix())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, registerURL, nil)
	if err != nil {
		return 0, fmt.Errorf("sungrow: build request for register %d: %w", addr, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// net/http includes the request URL (and session token) in url.Error.
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return 0, fmt.Errorf("sungrow: read register %d: %w", addr, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return 0, errTokenExpired
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("sungrow: read register %d: status %d", addr, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("sungrow: read register %d body: %w", addr, err)
	}

	var rr registerResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return 0, fmt.Errorf("sungrow: read register %d: unmarshal: %w", addr, err)
	}

	if rr.ResultCode == 106 || rr.ResultCode == 401 {
		return 0, errTokenExpired
	}
	// 120 (I18N_COMMON_INTER_ABNORMAL) is what the WiNet-S returns to a session
	// superseded by another login (web UI or another client); the keepalive
	// still succeeds, so only a fresh login recovers it.
	if rr.ResultCode == 120 {
		return 0, fmt.Errorf("sungrow: read register %d: result_code %d: %s: %w",
			addr, rr.ResultCode, rr.ResultMsg, errTokenExpired)
	}

	if rr.ResultCode != 0 && rr.ResultCode != 1 {
		return 0, fmt.Errorf("sungrow: read register %d: result_code %d: %s", addr, rr.ResultCode, rr.ResultMsg)
	}

	return parseHexRegisterValue(rr.ResultData.ParamValue, count, signed)
}

// parseHexRegisterValue parses the WiNet-S hex byte response.
// Values come as space-separated hex bytes, e.g. "19 2C 00 00 ".
// Each pair of bytes forms one 16-bit register (big-endian within the word).
// For multi-register reads, the words are in Modbus order (first word is
// the high word of a 32-bit value).
func parseHexRegisterValue(val string, regCount int, signed bool) (float64, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0, fmt.Errorf("sungrow: parse register value: empty")
	}

	parts := strings.Fields(val)
	if len(parts) < 2 {
		return 0, fmt.Errorf("sungrow: parse register value %q: too few bytes", val)
	}

	// Parse hex bytes into 16-bit words (each word = 2 bytes, big-endian).
	var words []uint16
	for i := 0; i+1 < len(parts); i += 2 {
		hi, err := strconv.ParseUint(parts[i], 16, 8)
		if err != nil {
			return 0, fmt.Errorf("sungrow: parse register value %q: %w", val, err)
		}
		lo, err := strconv.ParseUint(parts[i+1], 16, 8)
		if err != nil {
			return 0, fmt.Errorf("sungrow: parse register value %q: %w", val, err)
		}
		words = append(words, uint16(hi)<<8|uint16(lo))
	}

	if len(words) == 1 {
		if signed {
			return float64(int16(words[0])), nil
		}
		return float64(words[0]), nil
	}

	// Two registers: Sungrow returns low word first (register N), then high word (register N+1).
	combined := uint32(words[1])<<16 | uint32(words[0])
	if signed {
		return float64(int32(combined)), nil
	}
	return float64(combined), nil
}

// Close closes the WebSocket connection and clears state.
func (c *SungrowClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.token = ""
	c.devType = ""
	c.devCode = ""
	if c.wsConn != nil {
		err := c.wsConn.Close(websocket.StatusNormalClosure, "closing")
		c.wsConn = nil
		return err
	}
	return nil
}

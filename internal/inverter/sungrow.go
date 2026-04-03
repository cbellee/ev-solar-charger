package inverter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cbellee/solar-ev-charger/internal/observability"
	"nhooyr.io/websocket"
)

// SungrowClient communicates with a Sungrow inverter via the WiNet-S dongle.
type SungrowClient struct {
	host       string
	port       int
	token      string
	wsConn     *websocket.Conn
	httpClient *http.Client
	logger     *slog.Logger
	metrics    *observability.Metrics
	mu         sync.Mutex
}

// New creates a new SungrowClient.
func New(host string, port int, logger *slog.Logger, metrics *observability.Metrics) *SungrowClient {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &SungrowClient{
		host:       host,
		port:       port,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		logger:     logger,
		metrics:    metrics,
	}
}

type wsResponse struct {
	ResultCode int    `json:"result_code"`
	ResultData any    `json:"result_data"`
	Token      string `json:"token"`
}

// Connect establishes a WebSocket connection and obtains a session token.
func (c *SungrowClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var wsURL string
	if strings.Contains(c.host, ":") {
		wsURL = fmt.Sprintf("ws://%s/ws/home/overview", c.host)
	} else {
		wsURL = fmt.Sprintf("ws://%s:%d/ws/home/overview", c.host, c.port)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("sungrow: connect: %w", err)
	}

	msg := map[string]string{
		"lang":    "en_us",
		"token":   "",
		"service": "connect",
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "marshal error")
		return fmt.Errorf("sungrow: connect: marshal: %w", err)
	}

	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		conn.Close(websocket.StatusInternalError, "write error")
		return fmt.Errorf("sungrow: connect: write: %w", err)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "read error")
		return fmt.Errorf("sungrow: connect: read: %w", err)
	}

	var resp wsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		conn.Close(websocket.StatusInternalError, "unmarshal error")
		return fmt.Errorf("sungrow: connect: unmarshal: %w", err)
	}

	if resp.Token == "" {
		conn.Close(websocket.StatusInternalError, "no token")
		return fmt.Errorf("sungrow: connect: no token in response")
	}

	c.token = resp.Token
	c.wsConn = conn
	c.logger.InfoContext(ctx, "sungrow: connected", "host", c.host)
	return nil
}

type registerResponse struct {
	ResultCode int    `json:"result_code"`
	ResultMsg  string `json:"result_msg"`
	ResultData struct {
		ParamValue string `json:"param_value"`
	} `json:"result_data"`
}

// GetPowerData reads Modbus registers via the WiNet-S HTTP API.
func (c *SungrowClient) GetPowerData(ctx context.Context) (PowerData, error) {
	pvWatts, err := c.readRegisterWithRetry(ctx, 5031, 2, false)
	if err != nil {
		return PowerData{}, fmt.Errorf("sungrow: read pv power: %w", err)
	}

	gridWatts, err := c.readRegisterWithRetry(ctx, 5083, 2, true)
	if err != nil {
		return PowerData{}, fmt.Errorf("sungrow: read grid power: %w", err)
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

	// Check for token expiry (result_code 106).
	if c.isTokenExpiry(err) {
		c.logger.InfoContext(ctx, "sungrow: token expired, reconnecting")
		if reconnErr := c.Connect(ctx); reconnErr != nil {
			return 0, fmt.Errorf("sungrow: reconnect after token expiry: %w", reconnErr)
		}
		return c.readRegister(ctx, addr, count, signed)
	}
	return 0, err
}

var errTokenExpired = fmt.Errorf("sungrow: token expired")

func (c *SungrowClient) isTokenExpiry(err error) bool {
	return err != nil && err.Error() == errTokenExpired.Error()
}

func (c *SungrowClient) readRegister(ctx context.Context, addr, count int, signed bool) (float64, error) {
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()

	url := fmt.Sprintf("http://%s/device/getParam?dev_id=1&dev_type=0&dev_code=0"+
		"&type=3&param_addr=%d&param_num=%d&param_type=0"+
		"&token=%s&lang=en_us&time123456=%d",
		c.host, addr, count, token, time.Now().Unix())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("sungrow: build request for register %d: %w", addr, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("sungrow: read register %d: %w", addr, err)
	}
	defer resp.Body.Close()

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

	if rr.ResultCode == 106 {
		return 0, errTokenExpired
	}

	if rr.ResultCode != 0 {
		return 0, fmt.Errorf("sungrow: read register %d: result_code %d: %s", addr, rr.ResultCode, rr.ResultMsg)
	}

	return parseRegisterValue(rr.ResultData.ParamValue, signed)
}

func parseRegisterValue(val string, signed bool) (float64, error) {
	// The WiNet-S returns register values as comma-separated 16-bit words.
	// For a U32/S32, we get "high,low" and combine them.
	var high, low uint16
	n, err := fmt.Sscanf(val, "%d,%d", &high, &low)
	if err != nil || n != 2 {
		// Try single value as fallback.
		var single int64
		if _, err := fmt.Sscanf(val, "%d", &single); err != nil {
			return 0, fmt.Errorf("sungrow: parse register value %q: %w", val, err)
		}
		return float64(single), nil
	}

	combined := uint32(high)<<16 | uint32(low)
	if signed {
		return float64(int32(combined)), nil
	}
	return float64(combined), nil
}

// Close closes the WebSocket connection.
func (c *SungrowClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.wsConn != nil {
		err := c.wsConn.Close(websocket.StatusNormalClosure, "closing")
		c.wsConn = nil
		return err
	}
	return nil
}

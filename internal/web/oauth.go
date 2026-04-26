package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cbellee/solar-ev-charger/internal/config"
	"github.com/cbellee/solar-ev-charger/internal/tesla"
)

const (
	oauthStateCookieName = "tesla_oauth_state"
	tokenEndpoint        = "https://fleet-auth.prd.vn.cloud.tesla.com/oauth2/v3/token"
	authorizeEndpoint    = "https://auth.tesla.com/oauth2/v3/authorize"
)

var audienceByRegion = map[string]string{
	"na": "https://fleet-api.prd.na.vn.cloud.tesla.com",
	"eu": "https://fleet-api.prd.eu.vn.cloud.tesla.com",
	"cn": "https://fleet-api.prd.cn.vn.cloud.tesla.com",
}

type oauthServer struct {
	cfg        config.Config
	vehicle    tesla.VehicleController
	httpClient *http.Client
	logger     *slog.Logger
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func newOAuthServer(cfg config.Config, vehicle tesla.VehicleController, logger *slog.Logger) *oauthServer {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &oauthServer{
		cfg:     cfg,
		vehicle: vehicle,
		logger:  logger,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (s *oauthServer) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	publicKey, err := os.ReadFile(s.cfg.TeslaPublicKeyPEMPath)
	if err != nil {
		renderOAuthHTML(w, http.StatusInternalServerError, "Public key unavailable", "The configured public key file could not be read.")
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(publicKey)
}

func (s *oauthServer) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	state, err := randomState()
	if err != nil {
		s.logger.Error("oauth: generate state", "error", err)
		renderOAuthHTML(w, http.StatusInternalServerError, "State generation failed", "Could not generate OAuth state.")
		return
	}

	http.SetCookie(w, signedStateCookie(state, s.cfg.OAuthStateHMACKey, s.secureCookies()))
	authorizeURL, err := s.buildAuthorizeURL(state)
	if err != nil {
		s.logger.Error("oauth: build authorize URL", "error", err)
		renderOAuthHTML(w, http.StatusInternalServerError, "Authorize URL failed", "Could not build Tesla authorize URL.")
		return
	}

	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

func (s *oauthServer) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	if oauthErr := strings.TrimSpace(query.Get("error")); oauthErr != "" {
		description := strings.TrimSpace(query.Get("error_description"))
		s.logger.Warn("oauth: provider returned error", "error", oauthErr, "description", description)
		renderOAuthHTML(w, http.StatusBadRequest, "Tesla authorization failed", "Tesla returned an authorization error. See server logs for details.")
		return
	}

	code := strings.TrimSpace(query.Get("code"))
	state := strings.TrimSpace(query.Get("state"))
	if code == "" || state == "" {
		renderOAuthHTML(w, http.StatusBadRequest, "Missing callback parameters", "Both code and state are required.")
		return
	}

	cookie, err := r.Cookie(oauthStateCookieName)
	if err != nil || !validSignedState(cookie.Value, state, s.cfg.OAuthStateHMACKey) {
		renderOAuthHTML(w, http.StatusBadRequest, "Invalid OAuth state", "The callback state did not match the signed state cookie.")
		return
	}

	tokens, err := s.exchangeCode(r.Context(), code)
	if err != nil {
		s.logger.Error("oauth: token exchange", "error", err)
		renderOAuthHTML(w, http.StatusBadGateway, "Token exchange failed", "The Tesla token exchange did not succeed. See server logs for details.")
		return
	}
	if strings.TrimSpace(tokens.RefreshToken) == "" {
		renderOAuthHTML(w, http.StatusBadGateway, "Missing refresh token", "Tesla did not return a refresh token.")
		return
	}

	if err := writeRefreshTokenFile(s.cfg.TeslaTokenPath, tokens.RefreshToken); err != nil {
		s.logger.Error("oauth: write refresh token", "error", err)
		renderOAuthHTML(w, http.StatusInternalServerError, "Token persistence failed", "Could not persist the Tesla refresh token. See server logs for details.")
		return
	}

	if err := s.vehicle.SetRefreshToken(r.Context(), tokens.RefreshToken); err != nil {
		s.logger.Error("oauth: activate refresh token", "error", err)
		renderOAuthHTML(w, http.StatusBadGateway, "Token activation failed", "Could not activate the Tesla refresh token. See server logs for details.")
		return
	}

	http.SetCookie(w, expiredStateCookie(s.secureCookies()))
	renderOAuthHTML(w, http.StatusOK, "Tesla authorization complete", fmt.Sprintf("Refresh token saved to %s and activated.", s.cfg.TeslaTokenPath))
}

func (s *oauthServer) buildAuthorizeURL(state string) (string, error) {
	u, err := url.Parse(authorizeEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse authorize endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", s.cfg.TeslaClientID)
	q.Set("redirect_uri", s.cfg.TeslaRedirectURI)
	q.Set("scope", s.cfg.TeslaScope)
	q.Set("state", state)
	q.Set("prompt_missing_scopes", "true")
	q.Set("require_requested_scopes", "true")
	q.Set("show_keypair_step", "true")
	q.Set("locale", "en-US")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *oauthServer) exchangeCode(ctx context.Context, code string) (oauthTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", s.cfg.TeslaClientID)
	form.Set("client_secret", s.cfg.TeslaClientSecret)
	form.Set("code", code)
	form.Set("audience", audienceForRegion(s.cfg.TeslaRegion))
	form.Set("redirect_uri", s.cfg.TeslaRedirectURI)
	form.Set("scope", s.cfg.TeslaScope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokenResponse{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return oauthTokenResponse{}, fmt.Errorf("send token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return oauthTokenResponse{}, fmt.Errorf("Tesla token exchange failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokens oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return oauthTokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}

	return tokens, nil
}

func (s *oauthServer) secureCookies() bool {
	parsed, err := url.Parse(s.cfg.TeslaRedirectURI)
	if err != nil {
		return true
	}
	return parsed.Scheme == "https"
}

func audienceForRegion(region string) string {
	aud, ok := audienceByRegion[strings.ToLower(strings.TrimSpace(region))]
	if ok {
		return aud
	}
	return audienceByRegion["na"]
}

func randomState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func signedStateCookie(state, signingKey string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    signStateValue(state, signingKey),
		Path:     "/auth/tesla/callback",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func expiredStateCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    "",
		Path:     "/auth/tesla/callback",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func signStateValue(state, signingKey string) string {
	mac := hmac.New(sha256.New, []byte(signingKey))
	_, _ = mac.Write([]byte(state))
	signature := hex.EncodeToString(mac.Sum(nil))
	return state + "." + signature
}

func validSignedState(cookieValue, expectedState, signingKey string) bool {
	parts := strings.Split(cookieValue, ".")
	if len(parts) != 2 || parts[0] != expectedState {
		return false
	}
	mac := hmac.New(sha256.New, []byte(signingKey))
	_, _ = mac.Write([]byte(expectedState))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(expectedSig))
}

func writeRefreshTokenFile(path, refreshToken string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("token path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "refresh-token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp token file: %w", err)
	}
	tmpPath := tmpFile.Name()

	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	if err := tmpFile.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temp token file: %w", err)
	}
	if _, err := tmpFile.WriteString(strings.TrimSpace(refreshToken)); err != nil {
		return fmt.Errorf("write temp token file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp token file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp token file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace token file: %w", err)
	}

	return nil
}

func renderOAuthHTML(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; margin: 2rem auto; max-width: 42rem; padding: 0 1rem; line-height: 1.5; }
    main { border: 1px solid #d0d7de; border-radius: 12px; padding: 1.5rem; }
    h1 { margin-top: 0; }
  </style>
</head>
<body>
  <main>
    <h1>%s</h1>
    <p>%s</p>
  </main>
</body>
</html>`, template.HTMLEscapeString(title), template.HTMLEscapeString(title), template.HTMLEscapeString(message)))
}

var _ = context.Background

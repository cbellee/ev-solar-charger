package app

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
	"strings"
	"time"
)

const stateCookieName = "tesla_oauth_state"

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// Server serves the Tesla OAuth helper routes.
type Server struct {
	cfg         Config
	secretStore SecretStore
	httpClient  *http.Client
	logger      *slog.Logger
}

// NewServer creates the HTTP server for the custom handler.
func NewServer(cfg Config, secretStore SecretStore, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{
		cfg:         cfg,
		secretStore: secretStore,
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		logger:      logger,
	}
}

// Routes builds the handler mux for the custom handler routes.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/appspecific/com.tesla.3p.public-key.pem", s.handlePublicKey)
	mux.HandleFunc("/oauth/start", s.handleOAuthStart)
	mux.HandleFunc("/oauth/callback", s.handleOAuthCallback)
	return mux
}

func (s *Server) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	publicKey := strings.TrimSpace(s.cfg.PublicKeyPEM)
	if publicKey == "" {
		renderHTML(w, http.StatusInternalServerError, "Missing public key", "TESLA_PUBLIC_KEY_PEM is not configured.")
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = io.WriteString(w, publicKey+"\n")
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	state, err := randomState()
	if err != nil {
		s.logger.Error("generate state", "error", err)
		renderHTML(w, http.StatusInternalServerError, "State generation failed", "Could not generate an OAuth state value.")
		return
	}

	http.SetCookie(w, signedStateCookie(state, s.cfg.StateSigningKey, s.cfg.SecureCookies))
	authorizeURL, err := s.cfg.BuildAuthorizeURL(state)
	if err != nil {
		s.logger.Error("build authorize URL", "error", err)
		renderHTML(w, http.StatusInternalServerError, "Authorize URL failed", "Could not build the Tesla authorize URL.")
		return
	}

	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	if oauthError := strings.TrimSpace(query.Get("error")); oauthError != "" {
		message := oauthError
		if description := strings.TrimSpace(query.Get("error_description")); description != "" {
			message += ": " + description
		}
		renderHTML(w, http.StatusBadRequest, "Tesla authorization failed", message)
		return
	}

	code := strings.TrimSpace(query.Get("code"))
	state := strings.TrimSpace(query.Get("state"))
	if code == "" || state == "" {
		renderHTML(w, http.StatusBadRequest, "Missing callback parameters", "Both code and state query parameters are required.")
		return
	}

	cookie, err := r.Cookie(stateCookieName)
	if err != nil || !validSignedState(cookie.Value, state, s.cfg.StateSigningKey) {
		renderHTML(w, http.StatusBadRequest, "Invalid OAuth state", "The callback state did not match the signed state cookie.")
		return
	}

	token, err := s.exchangeCode(r.Context(), code)
	if err != nil {
		s.logger.Error("exchange authorization code", "error", err)
		renderHTML(w, http.StatusBadGateway, "Token exchange failed", err.Error())
		return
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		renderHTML(w, http.StatusBadGateway, "Missing refresh token", "Tesla did not return a refresh token.")
		return
	}

	if err := s.secretStore.SetSecret(r.Context(), s.cfg.RefreshTokenSecretName, token.RefreshToken); err != nil {
		s.logger.Error("persist refresh token", "error", err)
		renderHTML(w, http.StatusInternalServerError, "Key Vault write failed", err.Error())
		return
	}

	http.SetCookie(w, expiredStateCookie(s.cfg.SecureCookies))
	renderHTML(
		w,
		http.StatusOK,
		"Tesla authorization complete",
		fmt.Sprintf("The refresh token was stored in Azure Key Vault secret %q. You can close this window.", s.cfg.RefreshTokenSecretName),
	)
}

func (s *Server) exchangeCode(ctx context.Context, code string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", s.cfg.ClientID)
	form.Set("client_secret", s.cfg.ClientSecret)
	form.Set("code", code)
	form.Set("audience", s.cfg.Audience())
	form.Set("redirect_uri", s.cfg.RedirectURI)
	form.Set("scope", s.cfg.Scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("build Tesla token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("send Tesla token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return tokenResponse{}, fmt.Errorf("Tesla token exchange failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var token tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return tokenResponse{}, fmt.Errorf("decode Tesla token response: %w", err)
	}
	return token, nil
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
		Name:     stateCookieName,
		Value:    signStateValue(state, signingKey),
		Path:     "/oauth/callback",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func expiredStateCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/oauth/callback",
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

func renderHTML(w http.ResponseWriter, status int, title, message string) {
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
    code { background: #f6f8fa; padding: 0.15rem 0.35rem; border-radius: 4px; }
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

// Ensure the compiler keeps these interfaces aligned.
var _ SecretStore = (*KeyVaultSecretStore)(nil)
var _ = context.Background

package app

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	defaultTeslaScope    = "openid offline_access vehicle_device_data vehicle_cmds vehicle_charging_cmds"
	defaultAuthorizeURL  = "https://auth.tesla.com/oauth2/v3/authorize"
	defaultTokenURL      = "https://fleet-auth.prd.vn.cloud.tesla.com/oauth2/v3/token"
	defaultRefreshSecret = "tesla-refresh-token"
)

var audienceByRegion = map[string]string{
	"na": "https://fleet-api.prd.na.vn.cloud.tesla.com",
	"eu": "https://fleet-api.prd.eu.vn.cloud.tesla.com",
	"cn": "https://fleet-api.prd.cn.vn.cloud.tesla.com",
}

// Config holds the runtime configuration for the Azure Function helper.
type Config struct {
	ClientID               string
	ClientSecret           string
	RedirectURI            string
	Region                 string
	Scope                  string
	PublicKeyPEM           string
	StateSigningKey        string
	KeyVaultURI            string
	RefreshTokenSecretName string
	AuthorizeEndpoint      string
	TokenEndpoint          string
	SecureCookies          bool
}

// LoadConfigFromEnv reads and validates environment variables.
func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		ClientID:               strings.TrimSpace(os.Getenv("TESLA_CLIENT_ID")),
		ClientSecret:           strings.TrimSpace(os.Getenv("TESLA_CLIENT_SECRET")),
		RedirectURI:            strings.TrimSpace(os.Getenv("TESLA_REDIRECT_URI")),
		Region:                 strings.ToLower(strings.TrimSpace(envOrDefault("TESLA_REGION", "na"))),
		Scope:                  strings.TrimSpace(envOrDefault("TESLA_SCOPE", defaultTeslaScope)),
		PublicKeyPEM:           strings.TrimSpace(os.Getenv("TESLA_PUBLIC_KEY_PEM")),
		StateSigningKey:        strings.TrimSpace(os.Getenv("OAUTH_STATE_HMAC_KEY")),
		KeyVaultURI:            strings.TrimSpace(os.Getenv("KEY_VAULT_URI")),
		RefreshTokenSecretName: strings.TrimSpace(envOrDefault("TESLA_REFRESH_TOKEN_SECRET_NAME", defaultRefreshSecret)),
		AuthorizeEndpoint:      defaultAuthorizeURL,
		TokenEndpoint:          defaultTokenURL,
	}

	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "TESLA_CLIENT_ID", value: cfg.ClientID},
		{name: "TESLA_CLIENT_SECRET", value: cfg.ClientSecret},
		{name: "TESLA_REDIRECT_URI", value: cfg.RedirectURI},
		{name: "TESLA_PUBLIC_KEY_PEM", value: cfg.PublicKeyPEM},
		{name: "OAUTH_STATE_HMAC_KEY", value: cfg.StateSigningKey},
		{name: "KEY_VAULT_URI", value: cfg.KeyVaultURI},
	} {
		if required.value == "" {
			return Config{}, fmt.Errorf("missing required setting %s", required.name)
		}
	}

	if _, ok := audienceByRegion[cfg.Region]; !ok {
		return Config{}, fmt.Errorf("unsupported TESLA_REGION %q", cfg.Region)
	}
	if cfg.RefreshTokenSecretName == "" {
		return Config{}, fmt.Errorf("TESLA_REFRESH_TOKEN_SECRET_NAME must not be empty")
	}

	redirectURL, err := url.Parse(cfg.RedirectURI)
	if err != nil {
		return Config{}, fmt.Errorf("parse TESLA_REDIRECT_URI: %w", err)
	}
	if redirectURL.Scheme == "" || redirectURL.Host == "" {
		return Config{}, fmt.Errorf("TESLA_REDIRECT_URI must be an absolute URL")
	}
	if redirectURL.Scheme != "https" && !isLocalHTTP(redirectURL) {
		return Config{}, fmt.Errorf("TESLA_REDIRECT_URI must use https unless it targets localhost")
	}
	cfg.SecureCookies = !isLocalHTTP(redirectURL)

	vaultURL, err := url.Parse(cfg.KeyVaultURI)
	if err != nil {
		return Config{}, fmt.Errorf("parse KEY_VAULT_URI: %w", err)
	}
	if vaultURL.Scheme != "https" || vaultURL.Host == "" {
		return Config{}, fmt.Errorf("KEY_VAULT_URI must be an https URL")
	}

	return cfg, nil
}

// Audience returns the Fleet API audience based on the configured region.
func (c Config) Audience() string {
	return audienceByRegion[c.Region]
}

// BuildAuthorizeURL constructs the Tesla authorization endpoint for the current config.
func (c Config) BuildAuthorizeURL(state string) (string, error) {
	if strings.TrimSpace(state) == "" {
		return "", fmt.Errorf("state must not be empty")
	}

	endpoint, err := url.Parse(c.AuthorizeEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse authorize endpoint: %w", err)
	}

	query := endpoint.Query()
	query.Set("response_type", "code")
	query.Set("client_id", c.ClientID)
	query.Set("redirect_uri", c.RedirectURI)
	query.Set("scope", c.Scope)
	query.Set("state", state)
	query.Set("prompt_missing_scopes", "true")
	query.Set("require_requested_scopes", "true")
	query.Set("show_keypair_step", "true")
	query.Set("locale", "en-US")
	endpoint.RawQuery = query.Encode()

	return endpoint.String(), nil
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func isLocalHTTP(parsedURL *url.URL) bool {
	host := strings.ToLower(parsedURL.Hostname())
	return parsedURL.Scheme == "http" && (host == "localhost" || host == "127.0.0.1")
}

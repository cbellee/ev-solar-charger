package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/storage"
)

const (
	// SessionCookieName holds an opaque, random session ID. The __Host-
	// prefix makes browsers reject it unless it is Secure, host-only and
	// Path=/.
	SessionCookieName = "__Host-ev_session"
	// loginCookieName binds an in-progress Entra sign-in to this browser.
	loginCookieName = "__Host-ev_login"

	// csrfHeader must be present on state-changing requests authenticated by
	// a cookie. Cross-site pages cannot set custom headers without a CORS
	// preflight, which this server never grants.
	csrfHeader = "X-Requested-With"

	// loginTTL bounds how long a login transaction stays valid and how old
	// an ID token may be when it is exchanged for a session.
	loginTTL = 10 * time.Minute
	// renewInterval throttles idle-expiry writes to the store.
	renewInterval = 5 * time.Minute
	// maxClockSkew tolerates small differences between Entra and this host.
	maxClockSkew = 2 * time.Minute
)

var (
	errNoSession      = errors.New("session cookie missing")
	errSessionExpired = errors.New("session expired or revoked")
	errNoLogin        = errors.New("login transaction missing; start sign-in again")
	errNonceMismatch  = errors.New("id token nonce does not match this sign-in")
	errStaleIDToken   = errors.New("id token is too old to start a session")
)

// SessionStore persists server-side sessions. *storage.SQLiteStore
// implements it.
type SessionStore interface {
	CreateAuthSession(ctx context.Context, s storage.AuthSession, nonceExpiresAt time.Time) error
	GetAuthSession(ctx context.Context, idHash string) (storage.AuthSession, error)
	RenewAuthSession(ctx context.Context, idHash string, expiresAt, renewedAt time.Time) error
	DeleteAuthSessionsForUser(ctx context.Context, oid string) (int64, error)
	PruneAuthSessions(ctx context.Context, now time.Time) (int64, error)
}

// SessionManager issues revocable, server-side sessions so browsers that lose
// MSAL's cache (notably iOS Safari, which drops MSAL's encryption-key session
// cookie whenever the app is terminated) stay signed in.
//
// A session can only be created from an ID token minted for a sign-in this
// server started: the token's nonce must match a login transaction bound to
// the browser by an HttpOnly cookie, and each nonce is single-use. Sessions
// expire after IdleTimeout without use and MaxAge after the Entra sign-in,
// and signing out revokes all of the user's sessions.
type SessionManager struct {
	store       SessionStore
	key         []byte
	idleTimeout time.Duration
	maxAge      time.Duration
	logger      *slog.Logger
	now         func() time.Time
}

// NewSessionManager derives the login-nonce key from secret. Rotating secret
// invalidates in-flight sign-ins but not established sessions.
func NewSessionManager(store SessionStore, secret string, idleTimeout, maxAge time.Duration, logger *slog.Logger) (*SessionManager, error) {
	if store == nil {
		return nil, errors.New("session: store is required")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("session: secret is required")
	}
	if idleTimeout <= 0 {
		return nil, errors.New("session: idle timeout must be positive")
	}
	if maxAge < idleTimeout {
		return nil, errors.New("session: max age must be >= idle timeout")
	}
	if logger == nil {
		logger = slog.Default()
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("ev-solar-charger/entra-login-nonce/v1"))
	return &SessionManager{
		store:       store,
		key:         mac.Sum(nil),
		idleTimeout: idleTimeout,
		maxAge:      maxAge,
		logger:      logger,
		now:         time.Now,
	}, nil
}

// startLogin begins a login transaction and returns the nonce the SPA must
// pass to Entra. The nonce is derived from a secret that only ever lives in
// an HttpOnly cookie, so a stolen ID token cannot be paired with it.
func (m *SessionManager) startLogin(w http.ResponseWriter) (string, error) {
	secret, err := randomToken()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     loginCookieName,
		Value:    secret,
		Path:     "/",
		MaxAge:   int(loginTTL.Seconds()),
		Secure:   true,
		HttpOnly: true,
		// Lax so the cookie survives the top-level redirect back from Entra.
		SameSite: http.SameSiteLaxMode,
	})
	return m.loginNonce(secret), nil
}

// create redeems a login transaction for a session. claims must come from an
// ID token that has already passed signature, audience and tenant checks.
func (m *SessionManager) create(ctx context.Context, w http.ResponseWriter, r *http.Request, claims *entraClaims) error {
	c, err := r.Cookie(loginCookieName)
	if err != nil || c.Value == "" {
		return errNoLogin
	}
	want := m.loginNonce(c.Value)
	if !hmac.Equal([]byte(claims.Nonce), []byte(want)) {
		return errNonceMismatch
	}
	now := m.now()
	issuedAt := time.Unix(claims.IAT, 0)
	if claims.IAT == 0 || now.Sub(issuedAt) > loginTTL || issuedAt.Sub(now) > maxClockSkew {
		return errStaleIDToken
	}

	if _, err := m.store.PruneAuthSessions(ctx, now); err != nil {
		m.logger.Warn("session: prune expired sessions failed", "error", err)
	}

	id, err := randomToken()
	if err != nil {
		return err
	}
	exp := m.expiry(issuedAt, now)
	err = m.store.CreateAuthSession(ctx, storage.AuthSession{
		IDHash:    hashToken(id),
		NonceHash: hashToken(want),
		OID:       claims.OID,
		TID:       claims.TID,
		Name:      claims.displayName(),
		AuthAt:    issuedAt,
		ExpiresAt: exp,
		RenewedAt: now,
	}, issuedAt.Add(loginTTL+maxClockSkew))
	if err != nil {
		return err
	}
	clearCookie(w, loginCookieName, http.SameSiteLaxMode)
	m.setSessionCookie(w, id, exp, now)
	return nil
}

// lookup returns the live session for the request's cookie. When renew is
// true it slides the idle expiry (at most every renewInterval).
func (m *SessionManager) lookup(ctx context.Context, w http.ResponseWriter, r *http.Request, renew bool) (storage.AuthSession, error) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return storage.AuthSession{}, errNoSession
	}
	sess, err := m.store.GetAuthSession(ctx, hashToken(c.Value))
	if errors.Is(err, storage.ErrAuthSessionNotFound) {
		return storage.AuthSession{}, errSessionExpired
	}
	if err != nil {
		return storage.AuthSession{}, err
	}
	now := m.now()
	if !m.live(sess, now) {
		return storage.AuthSession{}, errSessionExpired
	}
	if renew && now.Sub(sess.RenewedAt) >= renewInterval {
		exp := m.expiry(sess.AuthAt, now)
		if err := m.store.RenewAuthSession(ctx, sess.IDHash, exp, now); err != nil {
			m.logger.Warn("session: renew failed", "error", err)
		} else {
			sess.ExpiresAt, sess.RenewedAt = exp, now
			m.setSessionCookie(w, c.Value, exp, now)
		}
	}
	return sess, nil
}

// live reports whether sess is within both its idle and absolute lifetimes.
func (m *SessionManager) live(sess storage.AuthSession, now time.Time) bool {
	return now.Before(sess.ExpiresAt) && now.Before(sess.AuthAt.Add(m.maxAge))
}

// revokeUser deletes every session belonging to oid.
func (m *SessionManager) revokeUser(ctx context.Context, oid string) (int64, error) {
	return m.store.DeleteAuthSessionsForUser(ctx, oid)
}

// clear expires the session and login cookies in this browser.
func (m *SessionManager) clear(w http.ResponseWriter) {
	clearCookie(w, SessionCookieName, http.SameSiteStrictMode)
	clearCookie(w, loginCookieName, http.SameSiteLaxMode)
}

// expiry is the sliding idle expiry, capped at authAt+maxAge.
func (m *SessionManager) expiry(authAt, now time.Time) time.Time {
	exp := now.Add(m.idleTimeout)
	if hard := authAt.Add(m.maxAge); exp.After(hard) {
		return hard
	}
	return exp
}

func (m *SessionManager) setSessionCookie(w http.ResponseWriter, id string, exp, now time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    id,
		Path:     "/",
		Expires:  exp,
		MaxAge:   max(int(exp.Sub(now).Seconds()), 1),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (m *SessionManager) loginNonce(secret string) string {
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func clearCookie(w http.ResponseWriter, name string, sameSite http.SameSite) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: sameSite,
	})
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("session: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

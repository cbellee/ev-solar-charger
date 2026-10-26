package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/storage"
)

// defaultStreamRecheck bounds how long an open SSE stream keeps flowing after
// its session is revoked (sign-out) or expires.
const defaultStreamRecheck = time.Minute

// isEventStream reports whether r opens the long-lived SSE stream, whose
// authentication must be re-checked for as long as it stays open.
func isEventStream(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/events"
}

// streamContext returns a context that is cancelled when the stream's
// credential lapses: at the ID token's expiry for bearer streams, or when a
// periodic re-check finds the session revoked or expired for cookie streams.
// Cancelling ends the SSE handler; the client then reconnects with a fresh
// credential or is sent to sign in.
func (a *EntraAuthenticator) streamContext(parent context.Context, claims *entraClaims, sessIDHash string) (context.Context, context.CancelFunc) {
	if sessIDHash == "" {
		if claims.Exp > 0 {
			return context.WithDeadline(parent, time.Unix(claims.Exp, 0))
		}
		return context.WithCancel(parent)
	}

	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(a.streamRecheck)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := a.sessions.check(ctx, sessIDHash); err != nil {
					if ctx.Err() == nil {
						a.logger.Info("entra: closing event stream", "oid", claims.OID, "reason", err)
					}
					cancel()
					return
				}
				if aerr := a.authorize(claims); aerr != nil {
					a.logger.Info("entra: closing event stream", "oid", claims.OID, "reason", aerr.code)
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

// check reports whether the session with idHash is still live, without
// extending it. Store errors are returned too, so streams fail closed.
func (m *SessionManager) check(ctx context.Context, idHash string) error {
	sess, err := m.store.GetAuthSession(ctx, idHash)
	if errors.Is(err, storage.ErrAuthSessionNotFound) {
		return errSessionExpired
	}
	if err != nil {
		return err
	}
	if !m.live(sess, m.now()) {
		return errSessionExpired
	}
	return nil
}

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrAuthSessionNotFound is returned when no live session matches an ID.
var ErrAuthSessionNotFound = errors.New("storage: auth session not found")

// ErrLoginNonceUsed is returned when a login nonce has already been redeemed,
// which makes every login transaction single-use.
var ErrLoginNonceUsed = errors.New("storage: login nonce already used")

// AuthSession is a server-side dashboard sign-in session. Only hashes of the
// cookie value and login nonce are stored.
type AuthSession struct {
	IDHash    string
	NonceHash string
	OID       string
	TID       string
	Name      string
	AuthAt    time.Time
	ExpiresAt time.Time
	RenewedAt time.Time
}

const authSessionsDDL = `
CREATE TABLE IF NOT EXISTS auth_sessions (
    id_hash    TEXT PRIMARY KEY,
    nonce_hash TEXT NOT NULL,
    oid        TEXT NOT NULL,
    tid        TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    auth_at    INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    renewed_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_oid ON auth_sessions(oid);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires ON auth_sessions(expires_at);
-- Redeemed login nonces, kept apart from sessions so that revoking a session
-- (sign-out) cannot make its nonce redeemable again.
CREATE TABLE IF NOT EXISTS auth_used_nonces (
    nonce_hash TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auth_used_nonces_expires ON auth_used_nonces(expires_at);
`

// CreateAuthSession atomically consumes the session's login nonce and inserts
// the session. The nonce stays consumed until nonceExpiresAt, even if the
// session is revoked first. It returns ErrLoginNonceUsed if the nonce has
// already been redeemed.
func (s *SQLiteStore) CreateAuthSession(ctx context.Context, a AuthSession, nonceExpiresAt time.Time) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: create auth session: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO auth_used_nonces (nonce_hash, expires_at) VALUES (?, ?)`,
		a.NonceHash, nonceExpiresAt.Unix()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: auth_used_nonces.nonce_hash") {
			return ErrLoginNonceUsed
		}
		return fmt.Errorf("storage: consume login nonce: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO auth_sessions (id_hash, nonce_hash, oid, tid, name, auth_at, expires_at, renewed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.IDHash, a.NonceHash, a.OID, a.TID, a.Name, a.AuthAt.Unix(), a.ExpiresAt.Unix(), a.RenewedAt.Unix()); err != nil {
		return fmt.Errorf("storage: create auth session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("storage: create auth session: commit: %w", err)
	}
	return nil
}

// GetAuthSession returns the session with idHash, or ErrAuthSessionNotFound.
// Expiry is enforced by the caller.
func (s *SQLiteStore) GetAuthSession(ctx context.Context, idHash string) (AuthSession, error) {
	var (
		a                          AuthSession
		authAt, expiresAt, renewed int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id_hash, nonce_hash, oid, tid, name, auth_at, expires_at, renewed_at
		 FROM auth_sessions WHERE id_hash = ?`, idHash).
		Scan(&a.IDHash, &a.NonceHash, &a.OID, &a.TID, &a.Name, &authAt, &expiresAt, &renewed)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthSession{}, ErrAuthSessionNotFound
	}
	if err != nil {
		return AuthSession{}, fmt.Errorf("storage: get auth session: %w", err)
	}
	a.AuthAt = time.Unix(authAt, 0)
	a.ExpiresAt = time.Unix(expiresAt, 0)
	a.RenewedAt = time.Unix(renewed, 0)
	return a, nil
}

// RenewAuthSession slides a session's idle expiry.
func (s *SQLiteStore) RenewAuthSession(ctx context.Context, idHash string, expiresAt, renewedAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE auth_sessions SET expires_at = ?, renewed_at = ? WHERE id_hash = ?`,
		expiresAt.Unix(), renewedAt.Unix(), idHash)
	if err != nil {
		return fmt.Errorf("storage: renew auth session: %w", err)
	}
	return nil
}

// DeleteAuthSessionsForUser revokes every session belonging to oid.
func (s *SQLiteStore) DeleteAuthSessionsForUser(ctx context.Context, oid string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE oid = ?`, oid)
	if err != nil {
		return 0, fmt.Errorf("storage: delete auth sessions: %w", err)
	}
	return res.RowsAffected()
}

// PruneAuthSessions removes sessions that expired before now, and used login
// nonces whose replay window has closed. It returns the number of sessions
// removed.
func (s *SQLiteStore) PruneAuthSessions(ctx context.Context, now time.Time) (int64, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_used_nonces WHERE expires_at <= ?`, now.Unix()); err != nil {
		return 0, fmt.Errorf("storage: prune used nonces: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("storage: prune auth sessions: %w", err)
	}
	return res.RowsAffected()
}

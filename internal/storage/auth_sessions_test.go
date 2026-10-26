package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuthSessionsLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	mk := func(id, nonce, oid string, exp time.Time) AuthSession {
		return AuthSession{IDHash: id, NonceHash: nonce, OID: oid, TID: "t", Name: "n", AuthAt: now, ExpiresAt: exp, RenewedAt: now}
	}

	nonceExp := now.Add(10 * time.Minute)
	if err := store.CreateAuthSession(ctx, mk("a", "na", "u1", now.Add(time.Hour)), nonceExp); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := store.CreateAuthSession(ctx, mk("dup", "na", "u1", now.Add(time.Hour)), nonceExp); !errors.Is(err, ErrLoginNonceUsed) {
		t.Fatalf("reused nonce: err = %v, want ErrLoginNonceUsed", err)
	}
	if _, err := store.GetAuthSession(ctx, "dup"); !errors.Is(err, ErrAuthSessionNotFound) {
		t.Fatalf("rejected session was stored: %v", err)
	}
	for _, s := range []AuthSession{
		mk("b", "nb", "u1", now.Add(time.Hour)),
		mk("c", "nc", "u2", now.Add(time.Hour)),
		mk("old", "nold", "u2", now.Add(-time.Second)),
	} {
		if err := store.CreateAuthSession(ctx, s, nonceExp); err != nil {
			t.Fatalf("create %s: %v", s.IDHash, err)
		}
	}

	got, err := store.GetAuthSession(ctx, "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if want := mk("a", "na", "u1", now.Add(time.Hour)); !got.ExpiresAt.Equal(want.ExpiresAt) || got.OID != "u1" || got.NonceHash != "na" {
		t.Fatalf("get = %+v", got)
	}
	if _, err := store.GetAuthSession(ctx, "missing"); !errors.Is(err, ErrAuthSessionNotFound) {
		t.Fatalf("missing: err = %v", err)
	}

	newExp := now.Add(2 * time.Hour)
	if err := store.RenewAuthSession(ctx, "a", newExp, now.Add(time.Minute)); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if got, _ := store.GetAuthSession(ctx, "a"); !got.ExpiresAt.Equal(newExp) || !got.RenewedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("after renew = %+v", got)
	}

	if n, err := store.PruneAuthSessions(ctx, now); err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
	if n, err := store.DeleteAuthSessionsForUser(ctx, "u1"); err != nil || n != 2 {
		t.Fatalf("delete u1 = %d, %v; want 2", n, err)
	}
	if _, err := store.GetAuthSession(ctx, "c"); err != nil {
		t.Fatalf("other user's session removed: %v", err)
	}

	// Revoking u1's sessions must not free nonce "na" for reuse...
	if err := store.CreateAuthSession(ctx, mk("replay", "na", "u1", now.Add(time.Hour)), nonceExp); !errors.Is(err, ErrLoginNonceUsed) {
		t.Fatalf("nonce reusable after revoke: err = %v", err)
	}
	// ...until its replay window has closed and it is pruned.
	if _, err := store.PruneAuthSessions(ctx, nonceExp); err != nil {
		t.Fatalf("prune nonces: %v", err)
	}
	if err := store.CreateAuthSession(ctx, mk("later", "na", "u1", now.Add(time.Hour)), nonceExp.Add(time.Hour)); err != nil {
		t.Fatalf("nonce not pruned: %v", err)
	}
}

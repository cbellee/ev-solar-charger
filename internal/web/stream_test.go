package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// openStream runs an authenticated GET /events through the middleware and
// returns the handler's context once it is serving, plus a channel that
// closes when the handler returns.
func openStream(t *testing.T, f *entraFixture, prepare func(*http.Request)) (context.Context, <-chan struct{}) {
	t.Helper()
	ctxCh := make(chan context.Context, 1)
	done := make(chan struct{})
	h := f.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxCh <- r.Context()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	reqCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		<-done
	})
	r := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(reqCtx)
	prepare(r)
	rec := httptest.NewRecorder()
	go func() {
		defer close(done)
		h.ServeHTTP(rec, r)
	}()
	select {
	case ctx := <-ctxCh:
		return ctx, done
	case <-done:
		t.Fatalf("stream rejected: status = %d", rec.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("stream handler never started")
	}
	return nil, nil
}

func waitClosed(t *testing.T, ctx context.Context, done <-chan struct{}) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stream context was not cancelled")
	}
	<-done
}

func TestCookieStreamClosesWhenSessionEnds(t *testing.T) {
	tests := []struct {
		name string
		end  func(f *entraFixture)
	}{
		{"revoked by sign-out", func(f *entraFixture) {
			if _, err := f.store.DeleteAuthSessionsForUser(context.Background(), testOID); err != nil {
				t.Errorf("delete: %v", err)
			}
		}},
		{"idle timeout", func(f *entraFixture) { f.clock.advance(25 * time.Hour) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEntraFixture(t, nil)
			f.auth.streamRecheck = 5 * time.Millisecond
			cookie := f.signIn(t)

			ctx, done := openStream(t, f, func(r *http.Request) { r.AddCookie(cookie) })
			time.Sleep(20 * time.Millisecond)
			if ctx.Err() != nil {
				t.Fatal("stream closed while session still valid")
			}
			tt.end(f)
			waitClosed(t, ctx, done)
		})
	}
}

func TestBearerStreamEndsAtTokenExpiry(t *testing.T) {
	f := newEntraFixture(t, nil)
	exp := time.Now().Add(time.Hour).Unix()
	f.tokens["good"] = &entraClaims{OID: testOID, TID: testTenant, Exp: exp}

	ctx, _ := openStream(t, f, func(r *http.Request) { r.Header.Set("Authorization", "Bearer good") })
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Unix() != exp {
		t.Fatalf("deadline = %v (set=%v), want %v", deadline, ok, time.Unix(exp, 0))
	}

	f.tokens["short"] = &entraClaims{OID: testOID, TID: testTenant, Exp: time.Now().Unix()}
	ctx2, done2 := openStream(t, f, func(r *http.Request) { r.Header.Set("Authorization", "Bearer short") })
	waitClosed(t, ctx2, done2)
}

func TestNonStreamRequestsHaveNoStreamDeadline(t *testing.T) {
	f := newEntraFixture(t, nil)
	f.tokens["good"] = &entraClaims{OID: testOID, TID: testTenant, Exp: time.Now().Add(time.Hour).Unix()}
	var hasDeadline bool
	h := f.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasDeadline = r.Context().Deadline()
	}))
	r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	r.Header.Set("Authorization", "Bearer good")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if hasDeadline {
		t.Fatal("non-stream request got a stream deadline")
	}
}

func TestQueryStringTokenRejected(t *testing.T) {
	f := newEntraFixture(t, nil)
	f.tokens["good"] = &entraClaims{OID: testOID, TID: testTenant}
	for _, path := range []string{"/api/state?access_token=good", "/events?access_token=good"} {
		t.Run(path, func(t *testing.T) {
			called := false
			h := f.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusUnauthorized || called {
				t.Fatalf("status = %d called = %v, want 401", rec.Code, called)
			}
		})
	}
}

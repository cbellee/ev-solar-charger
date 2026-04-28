package web

import "net/http"

// Authenticator gates access to protected routes. NewServer calls Middleware
// once when wiring routes; the middleware decides whether to allow the request
// to reach the next handler.
type Authenticator interface {
	Middleware(next http.Handler) http.Handler
}

// AuthenticatorFunc adapts an ordinary function into an Authenticator.
type AuthenticatorFunc func(next http.Handler) http.Handler

// Middleware satisfies the Authenticator interface.
func (f AuthenticatorFunc) Middleware(next http.Handler) http.Handler {
	return f(next)
}

// NoopAuthenticator allows every request through. Intended for tests only.
type NoopAuthenticator struct{}

// Middleware returns next unchanged.
func (NoopAuthenticator) Middleware(next http.Handler) http.Handler {
	return next
}

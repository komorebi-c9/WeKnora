package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestNoAuthAPICoversPublicAuthSurfaces pins the anonymous contract of the
// credential-minting surfaces. /auth/register, /auth/login and
// /auth/user-token all hand out credentials, so presenting a Bearer token is
// not a prerequisite — a regression here (dropping an entry) would make the
// endpoint 401 before the handler ever runs.
func TestNoAuthAPICoversPublicAuthSurfaces(t *testing.T) {
	cases := []struct {
		path   string
		method string
		want   bool
	}{
		{path: "/api/v1/auth/register", method: http.MethodPost, want: true},
		{path: "/api/v1/auth/login", method: http.MethodPost, want: true},
		{path: "/api/v1/auth/user-token", method: http.MethodPost, want: true},
		{path: "/api/v1/auth/refresh", method: http.MethodPost, want: true},
		// Wrong verb must stay protected: the whitelist is per (path, method).
		{path: "/api/v1/auth/user-token", method: http.MethodGet, want: false},
		// Authenticated surfaces must not be swept in by accident.
		{path: "/api/v1/auth/me", method: http.MethodGet, want: false},
		{path: "/api/v1/auth/logout", method: http.MethodPost, want: false},
		// Prefix matching only applies to entries ending in "*".
		{path: "/api/v1/auth/user-token/extra", method: http.MethodPost, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if got := isNoAuthAPI(tc.path, tc.method); got != tc.want {
				t.Fatalf("isNoAuthAPI(%q, %q) = %v, want %v", tc.path, tc.method, got, tc.want)
			}
		})
	}
}

// TestAuthMiddlewareAllowsAnonymousTokenRequest walks the real middleware
// chain with nil dependencies: an anonymous POST must reach the handler,
// while a non-whitelisted route with no credentials must still 401. Nil
// services are safe precisely because the whitelist short-circuits before any
// lookup happens — if that ordering ever changes this test panics instead of
// silently returning 401.
func TestAuthMiddlewareAllowsAnonymousTokenRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }

	newRouter := func(path, method string) *gin.Engine {
		r := gin.New()
		r.Use(Auth(nil, nil, nil, nil, nil))
		r.Handle(method, path, handler)
		return r
	}

	cases := []struct {
		name   string
		path   string
		method string
		want   int
	}{
		{name: "token by username", path: "/api/v1/auth/user-token", method: http.MethodPost, want: http.StatusOK},
		{name: "login", path: "/api/v1/auth/login", method: http.MethodPost, want: http.StatusOK},
		{name: "protected route", path: "/api/v1/knowledge-bases", method: http.MethodGet, want: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			newRouter(tc.path, tc.method).ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestAPIKeyGateAllowsAnonymousRequests guards the second gate on the
// /api/v1 group: the API-key authorizer is default-deny for API-key
// principals, but a caller with no key scope must pass through untouched so
// public endpoints stay reachable.
func TestAPIKeyGateAllowsAnonymousRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authorizer := NewAPIKeyRouteAuthorizer()
	r := gin.New()
	r.Use(authorizer.Middleware())
	r.POST("/api/v1/auth/user-token", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/user-token", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

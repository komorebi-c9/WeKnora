package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newOrionCredentialContext(
	t *testing.T, method, target string, body string, headers map[string]string,
) *gin.Context {
	t.Helper()

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, reader)
	for name, value := range headers {
		if name == "Cookie" {
			req.Header.Add(name, value)
			continue
		}
		req.Header.Set(name, value)
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

// TestOrionCredentialPrefersAssertion pins the rule the exchange endpoint
// promises: an NS-TOKEN always wins, and the License SK is only consulted when
// every assertion carrier is empty. A regression here would silently switch
// existing SSO deployments onto the SK path.
func TestOrionCredentialPrefersAssertion(t *testing.T) {
	c := newOrionCredentialContext(t, http.MethodPost,
		"/api/v1/auth/orion/exchange?sk=sk-query&NS-APIKEY=sk-query-api", "",
		map[string]string{
			orionTokenHeader:    "assertion-header",
			orionAPIKeyHeader:   "sk-header",
			"X-Orion-Api-Key":   "sk-alt-header",
			"Authorization":     "Bearer sk-bearer",
			"X-Orion-Token":     "assertion-alt-header",
			"X-Forwarded-Proto": "https",
		})

	if got := orionCredentialFromRequest(c); got != "assertion-header" {
		t.Fatalf("credential = %q, want the NS-TOKEN header", got)
	}
}

func TestOrionCredentialCarriers(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		target  string
		body    string
		headers map[string]string
		want    string
	}{
		{
			name:    "assertion header wins over assertion cookie",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{orionTokenHeader: "header-token", "Cookie": "NS-TOKEN=cookie-token"},
			want:    "header-token",
		},
		{
			name:    "forwarded assertion header",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{orionTokenHeaderAlt: "alt-token"},
			want:    "alt-token",
		},
		{
			name:    "sk falls back to the NS-APIKEY header",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{orionAPIKeyHeader: "sk-header"},
			want:    "sk-header",
		},
		{
			name:    "sk via the NS-SK header",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{orionAPIKeyHeaderSK: "sk-header"},
			want:    "sk-header",
		},
		{
			name:    "sk via the forwarding alias",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{orionAPIKeyHeaderAlt: "sk-alt-header"},
			want:    "sk-alt-header",
		},
		{
			name:    "sk via Authorization Bearer",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{"Authorization": "Bearer sk-bearer"},
			want:    "sk-bearer",
		},
		{
			name:    "a foreign bearer token is not forwarded to Orion",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{"Authorization": "Bearer upstream-api-token"},
			want:    "",
		},
		{
			name:   "assertion query carrier",
			method: http.MethodPost,
			target: "/api/v1/auth/orion/exchange?NS-TOKEN=query-token",
			want:   "query-token",
		},
		{
			name:   "snake-case assertion query carrier",
			method: http.MethodPost,
			target: "/api/v1/auth/orion/exchange?ns_token=query-token",
			want:   "query-token",
		},
		{
			name:   "sk query carrier",
			method: http.MethodPost,
			target: "/api/v1/auth/orion/exchange?sk=sk-query",
			want:   "sk-query",
		},
		{
			name:   "NS-APIKEY query carrier",
			method: http.MethodPost,
			target: "/api/v1/auth/orion/exchange?NS-APIKEY=sk-query",
			want:   "sk-query",
		},
		{
			name:   "NS-SK query carrier",
			method: http.MethodPost,
			target: "/api/v1/auth/orion/exchange?NS-SK=sk-query",
			want:   "sk-query",
		},
		{
			name:   "snake-case NS-SK query carrier",
			method: http.MethodPost,
			target: "/api/v1/auth/orion/exchange?ns_sk=sk-query",
			want:   "sk-query",
		},
		{
			name:    "explicit sk outranks a stale assertion cookie",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange?sk=sk-query",
			headers: map[string]string{"Cookie": "NS-TOKEN=stale-token"},
			want:    "sk-query",
		},
		{
			name:    "assertion cookie only",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{"Cookie": "NS-TOKEN=cookie-token"},
			want:    "cookie-token",
		},
		{
			name:    "sk cookie only",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{"Cookie": "NS-APIKEY=sk-cookie"},
			want:    "sk-cookie",
		},
		{
			name:    "NS-SK cookie only",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			headers: map[string]string{"Cookie": "NS-SK=sk-cookie"},
			want:    "sk-cookie",
		},
		{
			name:    "json body sk",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			body:    `{"sk":"sk-body"}`,
			headers: map[string]string{"Content-Type": "application/json"},
			want:    "sk-body",
		},
		{
			name:    "json body NS-SK",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			body:    `{"NS-SK":"sk-body"}`,
			headers: map[string]string{"Content-Type": "application/json"},
			want:    "sk-body",
		},
		{
			name:    "json body ns_sk",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			body:    `{"ns_sk":"sk-body"}`,
			headers: map[string]string{"Content-Type": "application/json"},
			want:    "sk-body",
		},
		{
			name:    "json body prefers the assertion",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			body:    `{"token":"body-token","sk":"sk-body"}`,
			headers: map[string]string{"Content-Type": "application/json"},
			want:    "body-token",
		},
		{
			name:    "form body sk",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			body:    "sk=sk-form",
			headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			want:    "sk-form",
		},
		{
			name:    "form body NS-SK",
			method:  http.MethodPost,
			target:  "/api/v1/auth/orion/exchange",
			body:    "NS-SK=sk-form",
			headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			want:    "sk-form",
		},
		{
			name:   "nothing supplied",
			method: http.MethodPost,
			target: "/api/v1/auth/orion/exchange",
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newOrionCredentialContext(t, tc.method, tc.target, tc.body, tc.headers)
			if got := orionCredentialFromRequest(c); got != tc.want {
				t.Fatalf("credential = %q, want %q", got, tc.want)
			}
		})
	}
}

package orion

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *HTTPClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "", time.Second)
}

func TestMeParsesIdentity(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("NS-TOKEN"); got != "assertion-1" {
			t.Errorf("NS-TOKEN header = %q, want assertion-1", got)
		}
		if r.URL.Path != DefaultMePath {
			t.Errorf("path = %q, want %q", r.URL.Path, DefaultMePath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"成功","data":{
			"id":100000001,"orgId":1,"orgName":"cube","name":"管理员",
			"username":"admin","email":null,"avatar":null,"status":1,"isAdmin":true}}`))
	})

	id, err := client.Me(context.Background(), NewCredential("assertion-1"))
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if id.ID != 100000001 {
		t.Fatalf("identity = %+v, want id=100000001", id)
	}
	if id.Username != "admin" {
		t.Fatalf("username = %q, want admin", id.Username)
	}
	if id.Email != nil || id.Avatar != nil {
		t.Fatalf("email/avatar = %v/%v, want nil", id.Email, id.Avatar)
	}
	if id.Status == nil || *id.Status != 1 {
		t.Fatalf("status = %v, want 1", id.Status)
	}
	if id.IsAdmin == nil || !*id.IsAdmin {
		t.Fatalf("is_admin = %v, want true", id.IsAdmin)
	}
}

func TestMeAdminMayBeAbsent(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":{"id":7,"orgId":2,"username":"bob"}}`))
	})

	id, err := client.Me(context.Background(), NewCredential("a"))
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if id.IsAdmin != nil {
		t.Fatalf("is_admin = %v, want nil so callers assume false", *id.IsAdmin)
	}
}

func TestMeStatusMayBeAbsent(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":{"id":7,"orgId":2,"username":"bob"}}`))
	})

	id, err := client.Me(context.Background(), NewCredential("a"))
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if id.Status != nil {
		t.Fatalf("status = %v, want nil so callers assume normal", *id.Status)
	}
}

// TestMeIgnoresOrganisation proves the identity no longer depends on the
// org fields: WeKnora builds a personal workspace per user, so a response
// without orgId/orgName must still parse.
func TestMeIgnoresOrganisation(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":{"id":42,"username":"bob"}}`))
	})

	id, err := client.Me(context.Background(), NewCredential("a"))
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if id.ID != 42 || id.Username != "bob" {
		t.Fatalf("identity = %+v, want id=42 username=bob", id)
	}
}

// TestMeParsesIAMMePayload pins the response shape of the production
// endpoint (/orion/v1/iam/me): extra IAM fields must be ignored and the
// absent status must stay nil rather than reject the login.
func TestMeParsesIAMMePayload(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orion/v1/iam/me" {
			t.Errorf("path = %q, want /orion/v1/iam/me", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"成功","data":{"id":100000001,
			"orgId":1,"orgName":"默认机构","name":"管理员","username":"admin",
			"mobile":null,"avatar":null,"myInviteCode":null,"email":null,
			"hasPassword":true,"isAdmin":true,"roles":[],"permissions":[]}}`))
	})

	id, err := client.Me(context.Background(), NewCredential("a"))
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if id.ID != 100000001 || id.Username != "admin" {
		t.Fatalf("identity = %+v, want id=100000001 username=admin", id)
	}
	if id.Email != nil || id.Avatar != nil {
		t.Fatalf("email/avatar = %v/%v, want nil", id.Email, id.Avatar)
	}
	if id.Status != nil {
		t.Fatalf("status = %v, want nil (endpoint omits it)", *id.Status)
	}
	if id.IsAdmin == nil || !*id.IsAdmin {
		t.Fatalf("is_admin = %v, want true", id.IsAdmin)
	}
}

// TestMeParsesStringUserID pins the current Orion contract: since
// iam_user.id became a string primary key, /orion/v1/iam/me serialises it as
// "100000001". Decoding it as a number used to fail the whole exchange.
func TestMeParsesStringUserID(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"message":"成功","data":{"id":"100000001",
			"orgId":1,"orgName":"默认机构","name":"管理员","username":"admin",
			"mobile":null,"avatar":null,"myInviteCode":null,"email":null,
			"hasPassword":true,"isAdmin":true,"roles":[],"permissions":[]}}`))
	})

	id, err := client.Me(context.Background(), NewCredential("a"))
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if id.ID != 100000001 || id.Username != "admin" {
		t.Fatalf("identity = %+v, want id=100000001 username=admin", id)
	}
}

func TestMeRejectsNonNumericUserID(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`"abc"`, `true`, `""`} {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"code":200,"data":{"id":` + raw + `,"username":"bob"}}`))
		})
		if _, err := client.Me(context.Background(), NewCredential("a")); !errors.Is(err, ErrUpstream) {
			t.Fatalf("id=%s: err = %v, want ErrUpstream", raw, err)
		}
	}
}

func TestMeMapsCredentialRejection(t *testing.T) {
	t.Parallel()

	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		})
		if _, err := client.Me(context.Background(), NewCredential("a")); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("http %d: err = %v, want ErrUnauthorized", code, err)
		}
	}
}

func TestMeMapsUpstreamFailures(t *testing.T) {
	t.Parallel()

	t.Run("server error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		})
		if _, err := client.Me(context.Background(), NewCredential("a")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v, want ErrUnavailable", err)
		}
	})

	t.Run("business error code", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"code":10101,"message":"用户名或密码错误"}`))
		})
		_, err := client.Me(context.Background(), NewCredential("a"))
		if !errors.Is(err, ErrUpstream) {
			t.Fatalf("err = %v, want ErrUpstream", err)
		}
		if !strings.Contains(err.Error(), "10101") {
			t.Fatalf("err = %v, want it to mention the upstream code", err)
		}
	})

	t.Run("identity without user id", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"code":200,"data":{"username":"bob"}}`))
		})
		if _, err := client.Me(context.Background(), NewCredential("a")); !errors.Is(err, ErrUpstream) {
			t.Fatalf("err = %v, want ErrUpstream", err)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"code":200,"data":"`))
			_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes)))
			_, _ = w.Write([]byte(`"}`))
		})
		if _, err := client.Me(context.Background(), NewCredential("a")); !errors.Is(err, ErrUpstream) {
			t.Fatalf("err = %v, want ErrUpstream", err)
		}
	})
}

func TestMeMapsTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	client := NewClient(srv.URL, "", 50*time.Millisecond)
	if _, err := client.Me(context.Background(), NewCredential("a")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestMeRejectsEmptyCredential(t *testing.T) {
	t.Parallel()

	client := NewClient("http://127.0.0.1:1", "", time.Second)
	if _, err := client.Me(context.Background(), NewCredential("   ")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if _, err := client.Me(context.Background(), Credential{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

// TestNewCredentialClassifiesByShape pins the rule that decides which header
// a credential travels in: an "sk-" prefix is a License SK, everything else
// is a session assertion. It is case-insensitive and tolerates whitespace,
// because the value may come straight from a query string.
func TestNewCredentialClassifiesByShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		kind CredentialKind
		want string
	}{
		{"  sk-abc123  ", CredentialAPIKey, "sk-abc123"},
		{"SK-abc", CredentialAPIKey, "SK-abc"},
		{"eyJhbGciOi.token.sig", CredentialToken, "eyJhbGciOi.token.sig"},
		{"sk-", CredentialAPIKey, "sk-"},
		{"ask-abc", CredentialToken, "ask-abc"},
		{"", CredentialToken, ""},
	}
	for _, tc := range cases {
		got := NewCredential(tc.in)
		if got.Kind != tc.kind || got.Value != tc.want {
			t.Fatalf("NewCredential(%q) = %+v, want kind=%v value=%q",
				tc.in, got, tc.kind, tc.want)
		}
	}
}

// TestMeSendsAPIKeyAsNSAPIKEY proves an SK reaches Orion through every header
// name Orion has used for it (NS-APIKEY, the current name, and NS-SK, the one
// it is migrating to), and that it is not also sent as NS-TOKEN (an NS-TOKEN
// cookie/header pair would make Orion pick the wrong branch).
func TestMeSendsAPIKeyAsNSAPIKEY(t *testing.T) {
	t.Parallel()

	var gotKey, gotSK, gotToken string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get(apiKeyHeader)
		gotSK = r.Header.Get(apiKeyHeaderSK)
		gotToken = r.Header.Get("NS-TOKEN")
		_, _ = w.Write([]byte(`{"code":200,"data":{"id":5,"username":"sk-owner"}}`))
	})

	id, err := client.Me(context.Background(), NewCredential("sk-license"))
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if gotKey != "sk-license" {
		t.Fatalf("NS-APIKEY header = %q, want sk-license", gotKey)
	}
	if gotSK != "sk-license" {
		t.Fatalf("NS-SK header = %q, want sk-license", gotSK)
	}
	if gotToken != "" {
		t.Fatalf("NS-TOKEN header = %q, want it left unset for an SK", gotToken)
	}
	if id.ID != 5 || id.Username != "sk-owner" {
		t.Fatalf("identity = %+v, want id=5 username=sk-owner", id)
	}
}

// TestMeSendsTokenAsNSTOKEN is the mirror case: a non-SK value must never be
// sent as NS-APIKEY or NS-SK, or every existing SSO deployment would break.
func TestMeSendsTokenAsNSTOKEN(t *testing.T) {
	t.Parallel()

	var gotKey, gotSK, gotToken string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get(apiKeyHeader)
		gotSK = r.Header.Get(apiKeyHeaderSK)
		gotToken = r.Header.Get("NS-TOKEN")
		_, _ = w.Write([]byte(`{"code":200,"data":{"id":6,"username":"token-owner"}}`))
	})

	if _, err := client.Me(context.Background(), NewCredential("eyJhbGciOi.token.sig")); err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if gotToken != "eyJhbGciOi.token.sig" {
		t.Fatalf("NS-TOKEN header = %q, want the session assertion", gotToken)
	}
	if gotKey != "" {
		t.Fatalf("NS-APIKEY header = %q, want it left unset for an assertion", gotKey)
	}
	if gotSK != "" {
		t.Fatalf("NS-SK header = %q, want it left unset for an assertion", gotSK)
	}
}

// TestAPIKeyShape covers the helper the handler uses to decide whether an
// Authorization: Bearer value belongs to Orion.
func TestAPIKeyShape(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]bool{
		"sk-abc":   true,
		" SK-abc ": true,
		"sk-":      true,
		"eyJabc":   false,
		"":         false,
		"Bearer":   false,
	} {
		if got := APIKeyShape(value); got != want {
			t.Fatalf("APIKeyShape(%q) = %t, want %t", value, got, want)
		}
	}
}

func TestNewClientNormalisesPaths(t *testing.T) {
	t.Parallel()

	c := NewClient("http://host/", "orion/v1/iam/me", 0)
	if c.mePath != "/orion/v1/iam/me" {
		t.Fatalf("mePath = %q", c.mePath)
	}
	if c.timeout != DefaultTimeout {
		t.Fatalf("timeout = %v, want default", c.timeout)
	}
	if c.baseURL != "http://host" {
		t.Fatalf("baseURL = %q", c.baseURL)
	}
}

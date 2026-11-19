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

	id, err := client.Me(context.Background(), "assertion-1")
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
}

func TestMeStatusMayBeAbsent(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":{"id":7,"orgId":2,"username":"bob"}}`))
	})

	id, err := client.Me(context.Background(), "a")
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if id.Status != nil {
		t.Fatalf("status = %v, want nil so callers assume normal", *id.Status)
	}
}

// TestMeIgnoresOrganisation proves the identity no longer depends on the
// org fields: WeKnora builds a personal workspace per user, so a response
// without orgId/orgName/isAdmin must still parse.
func TestMeIgnoresOrganisation(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":{"id":42,"username":"bob"}}`))
	})

	id, err := client.Me(context.Background(), "a")
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if id.ID != 42 || id.Username != "bob" {
		t.Fatalf("identity = %+v, want id=42 username=bob", id)
	}
}

func TestMeMapsCredentialRejection(t *testing.T) {
	t.Parallel()

	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		})
		if _, err := client.Me(context.Background(), "a"); !errors.Is(err, ErrUnauthorized) {
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
		if _, err := client.Me(context.Background(), "a"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v, want ErrUnavailable", err)
		}
	})

	t.Run("business error code", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"code":10101,"message":"用户名或密码错误"}`))
		})
		_, err := client.Me(context.Background(), "a")
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
		if _, err := client.Me(context.Background(), "a"); !errors.Is(err, ErrUpstream) {
			t.Fatalf("err = %v, want ErrUpstream", err)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"code":200,"data":"`))
			_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes)))
			_, _ = w.Write([]byte(`"}`))
		})
		if _, err := client.Me(context.Background(), "a"); !errors.Is(err, ErrUpstream) {
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
	if _, err := client.Me(context.Background(), "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestMeRejectsEmptyAssertion(t *testing.T) {
	t.Parallel()

	client := NewClient("http://127.0.0.1:1", "", time.Second)
	if _, err := client.Me(context.Background(), "   "); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
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

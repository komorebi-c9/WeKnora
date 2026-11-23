package orion

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestModelCatalogListGatewayCatalogue pins the production response shape of
// GET /v1/models: only the id is read, the type is inferred from the name, and
// the providers[]/owned_by extras are ignored.
func TestModelCatalogListGatewayCatalogue(t *testing.T) {
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		gotKey = r.Header.Get("NS-APIKEY")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"bge-m3-Q8_0","object":"model","created":1795004102,"owned_by":"orion",
			 "providers":[{"providerName":"cube","displayName":"cube"}]},
			{"id":"bge-reranker-v2-m3-Q8_0","object":"model","created":1795004102,"owned_by":"orion",
			 "providers":[{"providerName":"cube","displayName":"cube"}]},
			{"id":"local_Qwen3.6-35B-A3B-MTP","object":"model","created":1795004102,"owned_by":"orion",
			 "providers":[{"providerName":"cube","displayName":"cube"}]},
			{"id":"local_Qwen3.6-35B-A3B-MTP","object":"model","created":1795004102,"owned_by":"orion"},
			{"id":"  "}
		]}`))
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, 2*time.Second)
	entries, err := client.List(context.Background(), "sk-user-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotKey != "sk-user-1" {
		t.Fatalf("credential header = %q, want sk-user-1", gotKey)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (duplicate and blank id dropped): %+v", len(entries), entries)
	}

	want := map[string]string{
		"bge-m3-Q8_0":               CatalogTypeEmbedding,
		"bge-reranker-v2-m3-Q8_0":   CatalogTypeRerank,
		"local_Qwen3.6-35B-A3B-MTP": CatalogTypeChat,
	}
	for _, entry := range entries {
		if got := want[entry.Name]; got != entry.Type {
			t.Errorf("%s type = %q, want %q", entry.Name, entry.Type, got)
		}
		// The gateway face proves reachability only: no limit metadata is
		// invented and every listed model is treated as routable.
		if !entry.Available {
			t.Errorf("%s unexpectedly unavailable", entry.Name)
		}
		if entry.ContextWindow != 0 || entry.MaxTokens != 0 || entry.DisplayName != "" {
			t.Errorf("%s invented metadata: %+v", entry.Name, entry)
		}
	}
}

func TestModelCatalogListRejectsEmptyKeyWithoutRequest(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, time.Second)
	if _, err := client.List(context.Background(), "  "); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatal("empty credential must not reach Orion")
	}
}

func TestModelCatalogListMapsUnauthorized(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"未认证"}`))
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, time.Second)
	if _, err := client.List(context.Background(), "sk-revoked"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits = %d, want exactly 1: a rejected key must not be retried", hits)
	}
}

func TestModelCatalogListMapsServerFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, time.Second)
	_, err := client.List(context.Background(), "sk-user-3")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), "console") {
		t.Fatalf("error must not mention a fallback endpoint: %v", err)
	}
}

func TestProbeEmbeddingDimension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, time.Second)
	dimension, err := client.ProbeEmbeddingDimension(context.Background(), "sk-user-4", "bge-m3-Q8_0")
	if err != nil {
		t.Fatalf("ProbeEmbeddingDimension: %v", err)
	}
	if dimension != 3 {
		t.Fatalf("dimension = %d, want 3", dimension)
	}
}

func TestProbeEmbeddingDimensionEmptyVector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, time.Second)
	if _, err := client.ProbeEmbeddingDimension(context.Background(), "sk-user-5", "m"); !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
}

func TestInferModelType(t *testing.T) {
	cases := map[string]string{
		"bge-m3-Q8_0":               CatalogTypeEmbedding,
		"bge-reranker-v2-m3-Q8_0":   CatalogTypeRerank,
		"local_Qwen3.6-35B-A3B-MTP": CatalogTypeChat,
		"whisper-large-v3":          CatalogTypeASR,
		"sensevoice-small":          CatalogTypeASR,
		"text-embedding-3-large":    CatalogTypeEmbedding,
		"e5-large":                  CatalogTypeEmbedding,
	}
	for name, want := range cases {
		if got := InferModelType(name); got != want {
			t.Errorf("InferModelType(%q) = %q, want %q", name, got, want)
		}
	}
}

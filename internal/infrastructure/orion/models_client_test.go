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

func TestModelCatalogListConsole(t *testing.T) {
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotKey = r.Header.Get("NS-APIKEY")
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"models":[
			{"modelName":"bge-m3-Q8_0","displayName":"BGE M3","modelType":"embedding","contextWindow":8192,"maxTokens":0,"status":1,"availability":"available"},
			{"modelName":"bge-reranker-v2-m3-Q8_0","modelType":"rerank","status":1,"availability":"stale"},
			{"modelName":"local_Qwen3.6-35B-A3B-MTP","modelType":"chat","contextWindow":131072,"maxTokens":8192,"status":1,"availability":"available"},
			{"modelName":"broken","modelType":"chat","status":2,"availability":"unhealthy"},
			{"modelName":"local_Qwen3.6-35B-A3B-MTP","modelType":"chat","status":1,"availability":"available"}
		]}}`))
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
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4 (duplicate dropped): %+v", len(entries), entries)
	}

	byName := map[string]ModelCatalogEntry{}
	for _, entry := range entries {
		byName[entry.Name] = entry
	}

	embedding := byName["bge-m3-Q8_0"]
	if embedding.Type != CatalogTypeEmbedding || !embedding.Available {
		t.Fatalf("embedding entry = %+v", embedding)
	}
	if embedding.ContextWindow != 8192 || embedding.DisplayName != "BGE M3" {
		t.Fatalf("console metadata not copied: %+v", embedding)
	}
	// "stale" is routable-on-retry and must stay available.
	if !byName["bge-reranker-v2-m3-Q8_0"].Available {
		t.Fatal("stale model must remain available")
	}
	if byName["broken"].Available {
		t.Fatal("status=2/unhealthy model must be marked unavailable")
	}
}

func TestModelCatalogListRejectsEmptyKeyWithoutRequest(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"code":200,"data":{"models":[]}}`))
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

func TestModelCatalogListUnauthorizedDoesNotFallBack(t *testing.T) {
	var consoleHits, v1Hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			atomic.AddInt32(&consoleHits, 1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"未认证"}`))
		case "/v1/models":
			atomic.AddInt32(&v1Hits, 1)
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, time.Second)
	_, err := client.List(context.Background(), "sk-revoked")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if atomic.LoadInt32(&v1Hits) != 0 {
		t.Fatal("rejected credential must not be retried against /v1/models")
	}
	if atomic.LoadInt32(&consoleHits) != 1 {
		t.Fatalf("console hits = %d, want 1", consoleHits)
	}
}

func TestModelCatalogListEnvelopeUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HTTP 200 with a business 401 is how Orion reports some failures.
		_, _ = w.Write([]byte(`{"code":401,"message":"未认证，请登录或重新认证","data":null}`))
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, time.Second)
	if _, err := client.List(context.Background(), "sk-old"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestModelCatalogFallsBackToOpenAIShape(t *testing.T) {
	var consoleHits, v1Hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			atomic.AddInt32(&consoleHits, 1)
			w.WriteHeader(http.StatusBadGateway)
		case "/v1/models":
			atomic.AddInt32(&v1Hits, 1)
			_, _ = w.Write([]byte(`{"object":"list","data":[
				{"id":"bge-m3-Q8_0"},
				{"id":"bge-reranker-v2-m3-Q8_0"},
				{"id":"local_Qwen3.6-35B-A3B-MTP"},
				{"id":"whisper-large-v3"}
			]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, time.Second)
	entries, err := client.List(context.Background(), "sk-user-2")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if atomic.LoadInt32(&consoleHits) != 1 || atomic.LoadInt32(&v1Hits) != 1 {
		t.Fatalf("console=%d v1=%d, want 1/1", consoleHits, v1Hits)
	}
	want := map[string]string{
		"bge-m3-Q8_0":               CatalogTypeEmbedding,
		"bge-reranker-v2-m3-Q8_0":   CatalogTypeRerank,
		"local_Qwen3.6-35B-A3B-MTP": CatalogTypeChat,
		"whisper-large-v3":          CatalogTypeASR,
	}
	for _, entry := range entries {
		if want[entry.Name] != entry.Type {
			t.Errorf("%s type = %q, want %q", entry.Name, entry.Type, want[entry.Name])
		}
		// The OpenAI shape proves reachability only: no limit metadata is
		// invented, and every listed model is treated as routable.
		if !entry.Available {
			t.Errorf("%s unexpectedly unavailable", entry.Name)
		}
		if entry.ContextWindow != 0 || entry.MaxTokens != 0 {
			t.Errorf("%s invented limits: %+v", entry.Name, entry)
		}
	}
}

func TestModelCatalogReportsBothFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewModelCatalog(server.URL, time.Second)
	_, err := client.List(context.Background(), "sk-user-3")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "console catalogue") {
		t.Fatalf("error should explain the fallback attempt: %v", err)
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

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
// GET /v1/models: model_type decides the bucket, context_length and
// top_provider.max_completion_tokens become the row limits, orion.availability
// becomes the availability flag, and the providers[]/owned_by/endpoints extras
// are ignored.
func TestModelCatalogListGatewayCatalogue(t *testing.T) {
	var gotKey, gotSK string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		gotKey = r.Header.Get("NS-APIKEY")
		gotSK = r.Header.Get("NS-SK")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"bge-m3-Q8_0","object":"model","created":1795685091,"owned_by":"orion",
			 "model_type":"embedding","endpoints":["/orion/v1/embeddings"],
			 "orion":{"availability":"available"},
			 "providers":[{"providerName":"cube","displayName":"cube"}]},
			{"id":"bge-reranker-v2-m3-Q8_0","object":"model","created":1795685091,"owned_by":"orion",
			 "model_type":"rerank","endpoints":["/orion/v1/rerank"],
			 "orion":{"availability":"available"},
			 "providers":[{"providerName":"cube","displayName":"cube"}]},
			{"id":"local_Qwen3.6-35B-A3B-MTP","object":"model","created":1795685091,"owned_by":"orion",
			 "model_type":"chat","context_length":1000000,
			 "top_provider":{"context_length":1000000,"max_completion_tokens":64000},
			 "endpoints":["/orion/v1/chat/completions"],
			 "orion":{"availability":"available"},
			 "providers":[{"providerName":"cube","displayName":"cube"}]},
			{"id":"local_Qwen3.6-35B-A3B-MTP","object":"model","created":1795685091,"owned_by":"orion"},
			{"id":"offline-model","object":"model","model_type":"chat",
			 "top_provider":{"context_length":8192,"max_completion_tokens":1024},
			 "orion":{"availability":"unavailable"}},
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
	if gotSK != "sk-user-1" {
		t.Fatalf("NS-SK header = %q, want sk-user-1", gotSK)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4 (duplicate and blank id dropped): %+v", len(entries), entries)
	}

	want := map[string]ModelCatalogEntry{
		"bge-m3-Q8_0":               {Type: CatalogTypeEmbedding, Available: true},
		"bge-reranker-v2-m3-Q8_0":   {Type: CatalogTypeRerank, Available: true},
		"local_Qwen3.6-35B-A3B-MTP": {Type: CatalogTypeChat, ContextWindow: 1000000, MaxTokens: 64000, Available: true},
		// context_length is absent, so the top_provider value is used.
		"offline-model": {Type: CatalogTypeChat, ContextWindow: 8192, MaxTokens: 1024, Available: false},
	}
	for _, entry := range entries {
		expected, ok := want[entry.Name]
		if !ok {
			t.Errorf("unexpected entry %+v", entry)
			continue
		}
		if entry.Type != expected.Type {
			t.Errorf("%s type = %q, want %q", entry.Name, entry.Type, expected.Type)
		}
		if entry.ContextWindow != expected.ContextWindow || entry.MaxTokens != expected.MaxTokens {
			t.Errorf("%s limits = %d/%d, want %d/%d",
				entry.Name, entry.ContextWindow, entry.MaxTokens,
				expected.ContextWindow, expected.MaxTokens)
		}
		if entry.Available != expected.Available {
			t.Errorf("%s available = %v, want %v", entry.Name, entry.Available, expected.Available)
		}
	}
}

// TestCatalogModelType pins the declared-first resolution: model_type wins even
// when the name suggests another bucket, and legacy gateways without the field
// still fall back to the name heuristic. The five Orion declarations map onto
// WeKnora's buckets one-to-one: chat->chat, embedding->embedding, rerank->rerank,
// asr->asr, video->vision (CatalogTypeVLM).
func TestCatalogModelType(t *testing.T) {
	cases := []struct {
		name     string
		declared string
		want     string
	}{
		{"text-embedding-3-large", "chat", CatalogTypeChat},
		{"local_Qwen3.6-35B-A3B-MTP", "embedding", CatalogTypeEmbedding},
		{"bge-reranker-v2-m3-Q8_0", "", CatalogTypeRerank},
		{"mystery-model", "", CatalogTypeChat},
		{"some-model", "Text-Embedding", CatalogTypeEmbedding},
		{"some-model", "Reranker", CatalogTypeRerank},
		{"some-model", "asr", CatalogTypeASR},
		{"some-model", "vlm", CatalogTypeVLM},
		{"cube-vllm", "video", CatalogTypeVLM},
		{"some-model", "unknown-future-type", CatalogTypeUnsupported},
	}
	for _, c := range cases {
		if got := CatalogModelType(c.name, c.declared); got != c.want {
			t.Errorf("CatalogModelType(%q, %q) = %q, want %q",
				c.name, c.declared, got, c.want)
		}
	}
}

// TestModelCatalogListVisionMetadata pins architecture parsing: declared input
// modalities decide whether a chat row accepts images, the modality-only shape
// older payloads use still resolves, and Orion's "video" declaration lands in
// the vision bucket instead of the chat bucket.
func TestModelCatalogListVisionMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"cube-char","model_type":"chat",
			 "architecture":{"modality":"text+image->text","input_modalities":["text","image"],
			                 "output_modalities":["text"]}},
			{"id":"cube-text","model_type":"chat",
			 "architecture":{"input_modalities":["text"]}},
			{"id":"legacy-chat","model_type":"chat"},
			{"id":"modality-only","model_type":"chat",
			 "architecture":{"modality":"text+image->text"}},
			{"id":"cube-vllm","model_type":"video",
			 "architecture":{"modality":"text->video","input_modalities":["text"],
			                 "output_modalities":["video"]}}
		]}`))
	}))
	defer server.Close()

	entries, err := NewModelCatalog(server.URL, time.Second).List(context.Background(), "sk-user")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := make(map[string]ModelCatalogEntry, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}

	checks := []struct {
		name            string
		wantType        string
		wantVision      bool
		wantVisionKnown bool
	}{
		{"cube-char", CatalogTypeChat, true, true},
		{"cube-text", CatalogTypeChat, false, true},
		{"legacy-chat", CatalogTypeChat, false, false},
		{"modality-only", CatalogTypeChat, true, true},
		{"cube-vllm", CatalogTypeVLM, false, true},
	}
	for _, c := range checks {
		entry, ok := byName[c.name]
		if !ok {
			t.Errorf("%s missing from catalogue", c.name)
			continue
		}
		if entry.Type != c.wantType || entry.SupportsVision != c.wantVision ||
			entry.VisionKnown != c.wantVisionKnown {
			t.Errorf("%s = type %q vision %v known %v, want %q/%v/%v",
				c.name, entry.Type, entry.SupportsVision, entry.VisionKnown,
				c.wantType, c.wantVision, c.wantVisionKnown)
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

package orion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxCatalogResponseBytes caps how much of an Orion catalogue body is read. A
// deployment with thousands of models still fits comfortably; anything larger
// means the endpoint is not what we think it is.
const maxCatalogResponseBytes = 8 << 20

// Canonical model types. The gateway catalogue (GET /v1/models) declares
// model_type explicitly; recognised values are mapped one-to-one onto these
// buckets (Orion's "video" is its vision model, so it maps onto CatalogTypeVLM).
// Gateways predating the field fall back to InferModelType, the name-based
// heuristic Orion itself uses on probe import; a declared value this build does
// not know is reported as CatalogTypeUnsupported rather than guessed.
const (
	CatalogTypeChat      = "chat"
	CatalogTypeEmbedding = "embedding"
	CatalogTypeRerank    = "rerank"
	CatalogTypeASR       = "asr"
	CatalogTypeVLM       = "vlm"
	// CatalogTypeUnsupported marks a declared model_type this build cannot
	// serve. The synchroniser skips it instead of guessing a bucket.
	CatalogTypeUnsupported = "unsupported"
)

// ModelCatalogEntry is one model Orion has configured for a user.
type ModelCatalogEntry struct {
	// Name is the wire model name ("bge-m3-Q8_0"). It is also the value clients
	// put in the request body's `model` field (a "provider/model" form is
	// accepted too, but the bare name is stable).
	Name string
	// DisplayName is the operator-facing label from Orion; always empty for
	// the gateway catalogue, which only exposes the model id.
	DisplayName string
	// Type is one of CatalogType* above: the declared model_type mapped onto a
	// bucket, CatalogTypeUnsupported when the declaration is unknown, or the
	// name heuristic when the gateway omits the field entirely.
	Type string
	// ContextWindow / MaxTokens are model-declared limits; 0 means unknown.
	// The gateway reports them as context_length and
	// top_provider.max_completion_tokens.
	ContextWindow int
	MaxTokens     int
	// Available mirrors orion.availability; an absent value counts as
	// available because listing a model is itself an intent to serve it.
	Available bool
	// SupportsVision reports whether architecture.input_modalities declares
	// image input. It is only meaningful when VisionKnown is true.
	SupportsVision bool
	// VisionKnown reports whether the catalogue declared input modalities.
	// Older gateways omit architecture entirely; callers must not read
	// SupportsVision=false as "text-only" in that case.
	VisionKnown bool
}

// ModelCatalog reads the model catalogue Orion has configured for one
// credential holder, and probes embedding dimensions the catalogue omits.
//
// Implementations must never log or echo the API key.
type ModelCatalog interface {
	// List returns every model visible to apiKey. Unavailable models are
	// included with Available=false so the caller can report them instead of
	// pretending they disappeared.
	List(ctx context.Context, apiKey string) ([]ModelCatalogEntry, error)
	// ProbeEmbeddingDimension asks Orion to embed a tiny input and returns the
	// vector width.
	ProbeEmbeddingDimension(ctx context.Context, apiKey, model string) (int, error)
}

// HTTPModelCatalog is the production ModelCatalog.
type HTTPModelCatalog struct {
	modelsURL     string
	embeddingsURL string
	timeout       time.Duration
	http          *http.Client
}

// NewModelCatalog builds a client for an Orion service root such as
// http://192.168.3.139:9298/orion. A non-positive timeout falls back to 10s.
//
// The catalogue is read from the OpenAI-compatible gateway face
// (GET {root}/v1/models), not the console management API: the gateway is the
// surface inference actually goes through, and the shared License SK is the
// credential it accepts.
func NewModelCatalog(baseURL string, timeout time.Duration) *HTTPModelCatalog {
	root := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPModelCatalog{
		modelsURL:     root + "/v1/models",
		embeddingsURL: root + "/v1/embeddings",
		timeout:       timeout,
		http:          &http.Client{Timeout: timeout},
	}
}

// gatewayCatalog is GET /v1/models: the OpenAI {object,data} shape plus Orion's
// own extras. Only the fields the synchroniser actually consumes are decoded;
// owned_by and providers[] are ignored because every row is served through the
// same gateway credential.
type gatewayCatalog struct {
	Object string `json:"object"`
	Data   []struct {
		ID            string `json:"id"`
		ModelType     string `json:"model_type"`
		ContextLength int    `json:"context_length"`
		Architecture  struct {
			Modality         string   `json:"modality"`
			InputModalities  []string `json:"input_modalities"`
			OutputModalities []string `json:"output_modalities"`
		} `json:"architecture"`
		TopProvider struct {
			ContextLength       int `json:"context_length"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		} `json:"top_provider"`
		Orion struct {
			Availability string `json:"availability"`
		} `json:"orion"`
	} `json:"data"`
}

// List reads the OpenAI-compatible gateway catalogue. Each listed id becomes
// one row, typed by model_type (name heuristic when absent) and carrying the
// declared context window, output limit and availability. Reachability is not
// probed: the gateway only advertises what it means to serve.
func (c *HTTPModelCatalog) List(ctx context.Context, apiKey string) ([]ModelCatalogEntry, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, ErrUnauthorized
	}
	if c == nil || c.modelsURL == "" {
		return nil, ErrUnavailable
	}

	var payload gatewayCatalog
	if err := c.doJSON(ctx, http.MethodGet, c.modelsURL, apiKey, nil, &payload); err != nil {
		return nil, err
	}

	entries := make([]ModelCatalogEntry, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, row := range payload.Data {
		name := strings.TrimSpace(row.ID)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		// top_provider repeats the model-level limits for the selected
		// endpoint; fall back to it only when the top-level value is missing.
		contextWindow := row.ContextLength
		if contextWindow <= 0 {
			contextWindow = row.TopProvider.ContextLength
		}
		supportsVision, visionKnown := catalogVision(
			row.Architecture.Modality, row.Architecture.InputModalities)
		entries = append(entries, ModelCatalogEntry{
			Name:           name,
			Type:           CatalogModelType(name, row.ModelType),
			ContextWindow:  contextWindow,
			MaxTokens:      row.TopProvider.MaxCompletionTokens,
			Available:      catalogAvailability(row.Orion.Availability),
			SupportsVision: supportsVision,
			VisionKnown:    visionKnown,
		})
	}
	return entries, nil
}

// CatalogModelType resolves the bucket for one entry. A declared model_type is
// authoritative: recognised values map one-to-one, and an unrecognised value is
// reported as unsupported instead of being guessed from the name. The name
// heuristic only runs for gateways that predate the field.
func CatalogModelType(modelName, declared string) string {
	if strings.TrimSpace(declared) != "" {
		if catalogType, ok := normalizeCatalogType(declared); ok {
			return catalogType
		}
		return CatalogTypeUnsupported
	}
	return InferModelType(modelName)
}

// normalizeCatalogType maps a wire model_type onto a CatalogType*. Matching is
// case-insensitive and ignores separators, so "Text-Embedding" and
// "text_embedding" both resolve. Orion reports its vision models as "video", so
// that declaration maps onto CatalogTypeVLM (WeKnora's vision type). Unknown
// values report ok=false; the caller treats them as unsupported rather than
// falling back to a guess.
func normalizeCatalogType(declared string) (string, bool) {
	compact := strings.ToLower(strings.TrimSpace(declared))
	compact = strings.NewReplacer("-", "", "_", "", " ", "").Replace(compact)
	switch compact {
	case "chat", "llm", "completion", "completions", "chatcompletion", "chatcompletions",
		"textgeneration":
		return CatalogTypeChat, true
	case "embedding", "embeddings", "embed", "textembedding":
		return CatalogTypeEmbedding, true
	case "rerank", "reranker", "reranking", "rank":
		return CatalogTypeRerank, true
	case "asr", "audio", "transcription", "speechtotext":
		return CatalogTypeASR, true
	case "vlm", "vision", "multimodal", "video", "videos":
		return CatalogTypeVLM, true
	default:
		return "", false
	}
}

// catalogVision resolves the declared input modalities. known is false when the
// gateway omits architecture altogether, so callers can tell "text-only" apart
// from "not declared". Older payloads only carry modality ("text+image->text"),
// so the input side is read from it.
func catalogVision(modality string, inputs []string) (supports, known bool) {
	if len(inputs) == 0 {
		if in, _, found := strings.Cut(modality, "->"); found {
			inputs = strings.Split(in, "+")
		}
	}
	if len(inputs) == 0 {
		return false, false
	}
	for _, m := range inputs {
		if strings.EqualFold(strings.TrimSpace(m), "image") {
			return true, true
		}
	}
	return false, true
}

// catalogAvailability maps orion.availability onto the entry flag. Only values
// that explicitly deny service mark a model unavailable: an absent or
// unrecognised vocabulary must not prune rows, because the synchroniser treats
// unavailable entries as deleted.
func catalogAvailability(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "unavailable", "notavailable", "disabled", "offline", "down", "error", "failed":
		return false
	default:
		return true
	}
}

// InferModelType mirrors Orion's own probe-import heuristic
// (shared/model.rs infer_model_type). It is the fallback for gateway entries
// that carry no model_type, so pre-metadata deployments still land in the same
// buckets Orion's console catalogue would have reported.
func InferModelType(modelName string) string {
	name := strings.ToLower(modelName)
	switch {
	case strings.Contains(name, "rerank"):
		return CatalogTypeRerank
	case strings.Contains(name, "embed"), strings.Contains(name, "bge-"), strings.Contains(name, "e5-"):
		return CatalogTypeEmbedding
	case strings.Contains(name, "whisper"), strings.Contains(name, "asr"),
		strings.Contains(name, "sensevoice"):
		return CatalogTypeASR
	default:
		return CatalogTypeChat
	}
}

// ProbeEmbeddingDimension embeds a one-token input and reports the vector width.
func (c *HTTPModelCatalog) ProbeEmbeddingDimension(
	ctx context.Context, apiKey, model string,
) (int, error) {
	apiKey = strings.TrimSpace(apiKey)
	model = strings.TrimSpace(model)
	if apiKey == "" {
		return 0, ErrUnauthorized
	}
	if c == nil || c.embeddingsURL == "" {
		return 0, ErrUnavailable
	}
	if model == "" {
		return 0, fmt.Errorf("%w: embedding model name is empty", ErrUpstream)
	}

	var payload struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	reqBody := map[string]any{"model": model, "input": []string{"ping"}}
	if err := c.doJSON(ctx, http.MethodPost, c.embeddingsURL, apiKey, reqBody, &payload); err != nil {
		return 0, err
	}
	if len(payload.Data) == 0 || len(payload.Data[0].Embedding) == 0 {
		return 0, fmt.Errorf("%w: embeddings response carries no vector", ErrUpstream)
	}
	return len(payload.Data[0].Embedding), nil
}

// doJSON performs one credential-carrying request and normalises failures into
// the package error vocabulary.
func (c *HTTPModelCatalog) doJSON(
	ctx context.Context, method, endpoint, apiKey string, body, out any,
) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%w: encode request: %v", ErrUpstream, err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	// Orion accepts the License SK via NS-APIKEY (Authorization: Bearer works
	// too, but the dedicated header cannot collide with a proxied upstream).
	// The key also goes out as NS-SK so Orion's rename of that header never
	// needs a matching change here.
	setAPIKeyHeaders(req, apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// The error text never contains the request headers, so the key cannot
		// leak through this path.
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("%w: http %d", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("%w: http %d", ErrUpstream, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%w: reading response: %v", ErrUnavailable, err)
	}
	if len(raw) > maxCatalogResponseBytes {
		return fmt.Errorf("%w: response exceeds %d bytes", ErrUpstream, maxCatalogResponseBytes)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: malformed response: %v", ErrUpstream, err)
	}
	return nil
}

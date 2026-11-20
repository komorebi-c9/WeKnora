package orion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// Canonical model types reported by Orion's console catalogue
// (business/llm/shared/model.rs ModelType, serialised as snake_case).
const (
	CatalogTypeChat      = "chat"
	CatalogTypeEmbedding = "embedding"
	CatalogTypeRerank    = "rerank"
	CatalogTypeASR       = "asr"
)

// ModelCatalogEntry is one model Orion has configured for a user.
type ModelCatalogEntry struct {
	// Name is the wire model name ("bge-m3-Q8_0"). It is also the value clients
	// put in the request body's `model` field (a "provider/model" form is
	// accepted too, but the bare name is stable).
	Name string
	// DisplayName is the operator-facing label from Orion; may be empty.
	DisplayName string
	// Type is one of catalogType* above, or an unregistered value from Orion.
	Type string
	// ContextWindow / MaxTokens are model-declared limits; 0 means unknown.
	ContextWindow int
	MaxTokens     int
	// Available mirrors Orion's routing readiness: the row is enabled and at
	// least one candidate can be routed (status=1 and availability != unhealthy).
	Available bool
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
	catalogURL      string
	openAIModelsURL string
	embeddingsURL   string
	timeout         time.Duration
	http            *http.Client
}

// NewModelCatalog builds a client for an Orion service root such as
// http://192.168.3.139:9298/orion. A non-positive timeout falls back to 10s.
func NewModelCatalog(baseURL string, timeout time.Duration) *HTTPModelCatalog {
	root := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPModelCatalog{
		catalogURL:      root + "/api/models",
		openAIModelsURL: root + "/v1/models",
		embeddingsURL:   root + "/v1/embeddings",
		timeout:         timeout,
		http:            &http.Client{Timeout: timeout},
	}
}

// consoleCatalog is GET /api/models: Orion's {code,message,data} envelope.
type consoleCatalog struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Models []struct {
			ModelName     string `json:"modelName"`
			DisplayName   string `json:"displayName"`
			ModelType     string `json:"modelType"`
			ContextWindow int    `json:"contextWindow"`
			MaxTokens     int    `json:"maxTokens"`
			Status        int    `json:"status"`
			Availability  string `json:"availability"`
		} `json:"models"`
	} `json:"data"`
}

// openAICatalog is GET /v1/models: the OpenAI {object,data} shape.
type openAICatalog struct {
	Object string `json:"object"`
	Data   []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// List prefers the console catalogue (it carries types and limits) and falls
// back to the OpenAI-shaped catalogue. An authentication failure is returned
// immediately: retrying the same rejected credential against the second
// endpoint would only add noise.
func (c *HTTPModelCatalog) List(ctx context.Context, apiKey string) ([]ModelCatalogEntry, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, ErrUnauthorized
	}
	if c == nil || c.catalogURL == "" {
		return nil, ErrUnavailable
	}

	entries, err := c.listConsole(ctx, apiKey)
	if err == nil {
		return entries, nil
	}
	if errors.Is(err, ErrUnauthorized) {
		return nil, err
	}

	fallback, fallbackErr := c.listOpenAIShape(ctx, apiKey)
	if fallbackErr != nil {
		return nil, fmt.Errorf("%w (console catalogue: %v)", fallbackErr, err)
	}
	return fallback, nil
}

// listConsole parses the console catalogue.
func (c *HTTPModelCatalog) listConsole(ctx context.Context, apiKey string) ([]ModelCatalogEntry, error) {
	var payload consoleCatalog
	if err := c.doJSON(ctx, http.MethodGet, c.catalogURL, apiKey, nil, &payload); err != nil {
		return nil, err
	}
	// Orion answers HTTP 200 with a business code in the envelope.
	if payload.Code != http.StatusOK {
		if payload.Code == http.StatusUnauthorized || payload.Code == http.StatusForbidden {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf(
			"%w: code %d: %s", ErrUpstream, payload.Code, strings.TrimSpace(payload.Message),
		)
	}

	entries := make([]ModelCatalogEntry, 0, len(payload.Data.Models))
	seen := make(map[string]struct{}, len(payload.Data.Models))
	for _, row := range payload.Data.Models {
		name := strings.TrimSpace(row.ModelName)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}

		modelType := strings.TrimSpace(row.ModelType)
		if modelType == "" {
			modelType = InferModelType(name)
		}
		entries = append(entries, ModelCatalogEntry{
			Name:          name,
			DisplayName:   strings.TrimSpace(row.DisplayName),
			Type:          modelType,
			ContextWindow: row.ContextWindow,
			MaxTokens:     row.MaxTokens,
			// status 1 = enabled; availability "stale" self-heals, only
			// "unhealthy" means nothing can be routed right now.
			Available: row.Status == 1 && !strings.EqualFold(row.Availability, "unhealthy"),
		})
	}
	return entries, nil
}

// listOpenAIShape parses the OpenAI catalogue and infers types from names.
func (c *HTTPModelCatalog) listOpenAIShape(ctx context.Context, apiKey string) ([]ModelCatalogEntry, error) {
	var payload openAICatalog
	if err := c.doJSON(ctx, http.MethodGet, c.openAIModelsURL, apiKey, nil, &payload); err != nil {
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
		entries = append(entries, ModelCatalogEntry{
			Name:      name,
			Type:      InferModelType(name),
			Available: true,
		})
	}
	return entries, nil
}

// InferModelType mirrors Orion's own probe-import heuristic
// (shared/model.rs infer_model_type) so the fallback catalogue lands models in
// the same buckets the console would have reported.
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
	req.Header.Set("NS-APIKEY", apiKey)
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

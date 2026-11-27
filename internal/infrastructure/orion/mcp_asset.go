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

const (
	// DefaultAssetsTimeout bounds one asset-management round trip.
	DefaultAssetsTimeout = 10 * time.Second

	// mcpAssetsPath is appended to the Orion service root, i.e. the origin
	// plus the "/orion" prefix (http://host:9298/orion).
	mcpAssetsPath = "/v1/assets/mcp"
)

// CreateMCPInput is the body of POST /orion/v1/assets/mcp. Field names follow
// the console API's camelCase serialisation.
type CreateMCPInput struct {
	Name              string         `json:"name"`
	ShowName          string         `json:"showName"`
	Description       string         `json:"description"`
	TransportProtocol int            `json:"transportProtocol"`
	InjectionMode     int            `json:"injectionMode"`
	TransportConfig   map[string]any `json:"transportConfig"`
	// CreatedBy is the owning user id. The endpoint has no separate userId
	// parameter any more; created_by carries that identity.
	CreatedBy string `json:"createdBy"`
}

// CreateMCP registers one MCP connector in Orion's asset catalogue.
//
// serviceRoot is the Orion service root including the "/orion" prefix, so the
// request goes to {serviceRoot}/v1/assets/mcp. apiKey is the platform-wide
// License SK and travels in both NS-APIKEY and NS-SK (setAPIKeyHeaders), the
// same headers the model side uses.
//
// The response envelope is read only to decide success: Orion answers
// {code,message,data} with code 200 on success, and the created connector is
// deliberately ignored because callers only need to know whether the push
// succeeded.
func CreateMCP(ctx context.Context, serviceRoot, apiKey string, in CreateMCPInput) error {
	root := strings.TrimRight(strings.TrimSpace(serviceRoot), "/")
	apiKey = strings.TrimSpace(apiKey)
	switch {
	case root == "":
		return fmt.Errorf("%w: orion service root is empty", ErrUnavailable)
	case apiKey == "":
		return ErrUnauthorized
	}

	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("%w: encode mcp asset: %v", ErrUpstream, err)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultAssetsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, root+mcpAssetsPath, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	setAPIKeyHeaders(req, apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: DefaultAssetsTimeout}).Do(req)
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%w: reading response: %v", ErrUnavailable, err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("%w: response exceeds %d bytes", ErrUpstream, maxResponseBytes)
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("%w: malformed envelope: %v", ErrUpstream, err)
	}
	if env.Code != http.StatusOK {
		return fmt.Errorf("%w: code %d: %s", ErrUpstream, env.Code, strings.TrimSpace(env.Message))
	}
	return nil
}

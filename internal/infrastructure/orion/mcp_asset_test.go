package orion

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateMCPSendsAuthenticatedAsset pins the wire contract of
// POST /v1/assets/mcp: the License SK travels in NS-APIKEY, the streamable
// HTTP + aggregated payload is encoded as documented, and the envelope's data
// is not needed to decide success.
func TestCreateMCPSendsAuthenticatedAsset(t *testing.T) {
	var gotPath, gotMethod, gotKey, gotSK, gotContentType string
	var gotInput map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotKey = r.Header.Get(apiKeyHeader)
		gotSK = r.Header.Get(apiKeyHeaderSK)
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotInput); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"成功","data":{"id":223456}}`))
	}))
	defer server.Close()

	input := CreateMCPInput{
		Name:              "lolr-lxj",
		ShowName:          "lolr-lxj",
		Description:       "企业邮件连接器",
		TransportProtocol: 2,
		InjectionMode:     2,
		TransportConfig: map[string]any{
			"url": "http://192.168.3.139:9298/lolr-kb/mcp/ep-1",
			"headers": map[string]any{
				"Authorization": "Bearer mcp_secret",
			},
		},
		CreatedBy: "21131313",
	}

	if err := CreateMCP(context.Background(), server.URL+"/", "  sk-license  ", input); err != nil {
		t.Fatalf("CreateMCP: %v", err)
	}

	if gotPath != "/v1/assets/mcp" {
		t.Errorf("path = %q, want /v1/assets/mcp", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotKey != "sk-license" {
		t.Errorf("NS-APIKEY = %q, want the trimmed key", gotKey)
	}
	if gotSK != "sk-license" {
		t.Errorf("NS-SK = %q, want the trimmed key", gotSK)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}

	if got := gotInput["name"]; got != "lolr-lxj" {
		t.Errorf("name = %v", got)
	}
	if got := gotInput["showName"]; got != "lolr-lxj" {
		t.Errorf("showName = %v", got)
	}
	if got := gotInput["description"]; got != "企业邮件连接器" {
		t.Errorf("description = %v", got)
	}
	if got := gotInput["transportProtocol"]; got != float64(2) {
		t.Errorf("transportProtocol = %v, want 2", got)
	}
	if got := gotInput["injectionMode"]; got != float64(2) {
		t.Errorf("injectionMode = %v, want 2", got)
	}
	if got := gotInput["createdBy"]; got != "21131313" {
		t.Errorf("createdBy = %v", got)
	}
	// The create contract no longer has a userId parameter; sending a stale one
	// would silently be ignored upstream.
	if _, present := gotInput["userId"]; present {
		t.Error("userId must not be sent any more")
	}

	config, ok := gotInput["transportConfig"].(map[string]any)
	if !ok {
		t.Fatalf("transportConfig = %v, want object", gotInput["transportConfig"])
	}
	if got := config["url"]; got != "http://192.168.3.139:9298/lolr-kb/mcp/ep-1" {
		t.Errorf("transportConfig.url = %v", got)
	}
	headers, ok := config["headers"].(map[string]any)
	if !ok || headers["Authorization"] != "Bearer mcp_secret" {
		t.Errorf("transportConfig.headers = %v", config["headers"])
	}
}

// TestCreateMCPRejectsFailedEnvelope keeps an Orion-side rejection (duplicate
// name, bad transport config, ...) distinguishable from a transport failure
// and carries Orion's message into the log line.
func TestCreateMCPRejectsFailedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":400,"message":"name already exists"}`))
	}))
	defer server.Close()

	err := CreateMCP(context.Background(), server.URL, "sk-license", CreateMCPInput{Name: "lolr-lxj"})
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	if !strings.Contains(err.Error(), "name already exists") {
		t.Fatalf("err = %v, want Orion's message", err)
	}
}

// TestCreateMCPMapsHTTPFailures mirrors the credential/availability split the
// identity and model clients use.
func TestCreateMCPMapsHTTPFailures(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrUnauthorized},
		{http.StatusInternalServerError, ErrUnavailable},
		{http.StatusBadGateway, ErrUnavailable},
		{http.StatusBadRequest, ErrUpstream},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			err := CreateMCP(context.Background(), server.URL, "sk-license", CreateMCPInput{Name: "x"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("status %d: err = %v, want %v", tc.status, err, tc.want)
			}
		})
	}
}

// TestCreateMCPRequiresCredentials makes sure an unconfigured deployment never
// issues an outbound request.
func TestCreateMCPRequiresCredentials(t *testing.T) {
	if err := CreateMCP(context.Background(), "", "sk-license", CreateMCPInput{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty root: err = %v, want ErrUnavailable", err)
	}
	if err := CreateMCP(context.Background(), "http://orion.example", "   ", CreateMCPInput{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("empty key: err = %v, want ErrUnauthorized", err)
	}
}

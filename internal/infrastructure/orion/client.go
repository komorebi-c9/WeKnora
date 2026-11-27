// Package orion talks to the Orion (LoLR) identity service.
//
// WeKnora treats Orion as its only identity provider. The single call this
// package makes is GET /orion/v1/iam/me, which validates the caller's
// credential and returns the current user's details. Two credential shapes
// are accepted, mirroring what Orion itself accepts on that endpoint:
//
//   - a session assertion (NS-TOKEN), forwarded as the NS-TOKEN header;
//   - a License SK ("sk-..."), forwarded as both NS-APIKEY and NS-SK so
//     Orion's header rename cannot break the call.
//
// The shape is what tells them apart: see NewCredential. The user identity is
// consumed, including the org-admin flag: an Orion administrator is mirrored
// as a WeKnora system administrator. The organisation itself (orgId/orgName)
// is still ignored — WeKnora gives every mirrored user their own personal
// workspace, so the org membership is not mapped onto a shared workspace.
package orion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultMePath is the identity endpoint exposed by Orion.
	DefaultMePath = "/orion/v1/iam/me"
	// DefaultTimeout bounds one exchange round trip.
	DefaultTimeout = 5 * time.Second
	// maxResponseBytes caps how much of an upstream body we are willing to
	// read, so a misbehaving Orion cannot exhaust memory during login.
	maxResponseBytes = 64 << 10

	// tokenHeader is the header Orion reads a session assertion from.
	tokenHeader = "NS-TOKEN"
	// apiKeyHeader is the header name Orion historically read a License SK
	// from. Its own gateway accepts the SK as Authorization: Bearer too, but
	// the dedicated header cannot collide with a proxied upstream.
	apiKeyHeader = "NS-APIKEY"
	// apiKeyHeaderSK is the name Orion is migrating the License SK header to.
	// Every outbound call carries the key in BOTH headers (see
	// setAPIKeyHeaders): the rename then never needs a lockstep release here,
	// and an Orion that still reads the old name keeps working.
	apiKeyHeaderSK = "NS-SK"

	// apiKeyPrefix is the shape every Orion License SK starts with. It is
	// what lets an opaque credential (a jump URL, a forwarded header) be
	// classified without any out-of-band hint.
	apiKeyPrefix = "sk-"
)

var (
	// ErrUnauthorized means Orion rejected the credential (expired, invalid,
	// or belonging to a disabled account).
	ErrUnauthorized = errors.New("orion: credential rejected")
	// ErrUnavailable means Orion could not be reached or timed out.
	ErrUnavailable = errors.New("orion: identity service unavailable")
	// ErrUpstream means Orion answered but reported a failure.
	ErrUpstream = errors.New("orion: unexpected response")
)

// Identity is the caller's identity as reported by Orion.
//
// The org-admin flag is consumed: an Orion administrator becomes a WeKnora
// system administrator. The organisation the caller belongs to (orgId/orgName)
// is still ignored, because every mirrored user gets a personal workspace of
// their own rather than sharing their Orion organisation's one.
type Identity struct {
	ID       int64
	Username string
	Email    *string
	Avatar   *string
	// IsAdmin mirrors Orion's org-admin flag. Nil means Orion did not
	// report it; callers must then assume false.
	IsAdmin *bool
	// Status mirrors iam_user.status: 1 normal, 2 locked, 3 cancelled.
	// Nil means Orion did not report it; callers must then assume normal
	// rather than rejecting the login.
	Status *int
}

// Credential is one Orion credential as presented by the caller.
//
// Kind is derived from the value's shape (see NewCredential), not from the
// carrier it arrived in: Orion's console can hand either shape over in the
// same query parameter, and a License SK is still an SK when it comes in
// through a header named NS-TOKEN.
type Credential struct {
	// Value is the raw credential. It is never logged.
	Value string
	// Kind selects the header the value is sent in.
	Kind CredentialKind
}

// CredentialKind selects the upstream header a credential travels in.
type CredentialKind int

const (
	// CredentialToken is an Orion session assertion (JWT), sent as NS-TOKEN.
	CredentialToken CredentialKind = iota
	// CredentialAPIKey is an Orion License SK, sent as NS-APIKEY.
	CredentialAPIKey
)

// APIKeyShape reports whether value looks like an Orion License SK rather
// than a session assertion. The comparison is case-insensitive because the
// prefix is a format marker, not part of the secret's entropy.
func APIKeyShape(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), apiKeyPrefix)
}

// NewCredential classifies a raw credential by shape: an "sk-" prefix means
// a License SK, anything else is treated as a session assertion.
func NewCredential(value string) Credential {
	value = strings.TrimSpace(value)
	if APIKeyShape(value) {
		return Credential{Value: value, Kind: CredentialAPIKey}
	}
	return Credential{Value: value, Kind: CredentialToken}
}

// IsEmpty reports whether the credential carries no value.
func (c Credential) IsEmpty() bool {
	return strings.TrimSpace(c.Value) == ""
}

// Client validates Orion credentials. Implementations must never log the
// credential itself.
type Client interface {
	Me(ctx context.Context, credential Credential) (*Identity, error)
}

// HTTPClient is the production Client.
type HTTPClient struct {
	baseURL string
	mePath  string
	timeout time.Duration
	http    *http.Client
}

// NewClient builds a Client for the given Orion base URL. An empty mePath
// falls back to DefaultMePath; a non-positive timeout falls back to
// DefaultTimeout.
func NewClient(baseURL, mePath string, timeout time.Duration) *HTTPClient {
	path := strings.TrimSpace(mePath)
	if path == "" {
		path = DefaultMePath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &HTTPClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		mePath:  path,
		timeout: timeout,
		http:    &http.Client{Timeout: timeout},
	}
}

// envelope is Orion's standard response wrapper: {code, message, data}.
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// mePayload mirrors the subset of the Orion IAM user that WeKnora stores.
// Field names are camelCase because the Rust struct is serialised with
// #[serde(rename_all = "camelCase")]. Unknown keys (orgId, orgName, mobile,
// myInviteCode, hasPassword, roles, permissions, ...) are ignored by
// encoding/json on purpose, and status is absent from this endpoint, so it
// stays nil and callers treat the account as active.
type mePayload struct {
	ID       flexibleID `json:"id"`
	Name     string     `json:"name"`
	Username string     `json:"username"`
	Email    *string    `json:"email"`
	Avatar   *string    `json:"avatar"`
	Status   *int       `json:"status"`
	IsAdmin  *bool      `json:"isAdmin"`
}

// flexibleID decodes an Orion user id whether it travels as a JSON number or
// as a decimal string.
//
// Orion switched iam_user.id to a string primary key (v0.0.67.1) and now
// serialises it as "100000001"; older builds emitted 100000001. Pinning one
// shape broke the whole exchange for the other: a strict int64 made every
// /orion/v1/iam/me response fail to unmarshal, which surfaced to users as
// "Orion identity service is unavailable" and an SSO-only entry page.
type flexibleID int64

func (v *flexibleID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*v = 0
		return nil
	}
	if raw[0] == '"' {
		var quoted string
		if err := json.Unmarshal(data, &quoted); err != nil {
			return err
		}
		raw = strings.TrimSpace(quoted)
		if raw == "" {
			*v = 0
			return nil
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("user id %s is not an integer: %w", raw, err)
	}
	*v = flexibleID(n)
	return nil
}

// setAPIKeyHeaders attaches a License SK to req under every header name Orion
// has used for it.
//
// Orion is renaming the SK header from NS-APIKEY to NS-SK. Sending both keeps
// this integration working whichever name the gateway currently reads, without
// waiting for a coordinated release; the extra header is invisible to callers
// and does not weaken anything (same secret, same request).
func setAPIKeyHeaders(req *http.Request, apiKey string) {
	req.Header.Set(apiKeyHeader, apiKey)
	req.Header.Set(apiKeyHeaderSK, apiKey)
}

// Me validates credential against Orion and returns the caller's identity.
//
// A session assertion travels as NS-TOKEN, a License SK as NS-APIKEY and
// NS-SK at once (see setAPIKeyHeaders), which is the same split Orion's own
// authentication middleware performs.
func (c *HTTPClient) Me(ctx context.Context, credential Credential) (*Identity, error) {
	credential.Value = strings.TrimSpace(credential.Value)
	if credential.IsEmpty() {
		return nil, ErrUnauthorized
	}
	if c == nil || c.baseURL == "" {
		return nil, ErrUnavailable
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+c.mePath, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if credential.Kind == CredentialAPIKey {
		setAPIKeyHeaders(req, credential.Value)
	} else {
		req.Header.Set(tokenHeader, credential.Value)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %v", ErrUnavailable, err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrUpstream, maxResponseBytes)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode >= http.StatusInternalServerError:
		return nil, fmt.Errorf("%w: http %d", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: http %d", ErrUpstream, resp.StatusCode)
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%w: malformed envelope: %v", ErrUpstream, err)
	}
	if env.Code != http.StatusOK {
		return nil, fmt.Errorf("%w: code %d: %s", ErrUpstream, env.Code, strings.TrimSpace(env.Message))
	}

	var payload mePayload
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		return nil, fmt.Errorf("%w: malformed identity: %v", ErrUpstream, err)
	}
	// Only the user id is required: the workspace is derived from the user.
	if payload.ID <= 0 {
		return nil, fmt.Errorf("%w: identity is missing the user id", ErrUpstream)
	}

	return &Identity{
		ID:       int64(payload.ID),
		Username: strings.TrimSpace(payload.Username),
		Email:    payload.Email,
		Avatar:   payload.Avatar,
		Status:   payload.Status,
		IsAdmin:  payload.IsAdmin,
	}, nil
}

// Package orion talks to the Orion (LoLR) identity service.
//
// WeKnora treats Orion as its only identity provider. The single call this
// package makes is GET /orion/v1/iam/me, which validates the caller's
// NS-TOKEN and returns the current user's details. The user identity is
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
)

var (
	// ErrUnauthorized means Orion rejected the assertion (expired, invalid,
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

// Client validates Orion assertions. Implementations must never log the
// assertion itself.
type Client interface {
	Me(ctx context.Context, assertion string) (*Identity, error)
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
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Username string  `json:"username"`
	Email    *string `json:"email"`
	Avatar   *string `json:"avatar"`
	Status   *int    `json:"status"`
	IsAdmin  *bool   `json:"isAdmin"`
}

// Me validates assertion against Orion and returns the caller's identity.
func (c *HTTPClient) Me(ctx context.Context, assertion string) (*Identity, error) {
	assertion = strings.TrimSpace(assertion)
	if assertion == "" {
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
	req.Header.Set("NS-TOKEN", assertion)
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
		ID:       payload.ID,
		Username: strings.TrimSpace(payload.Username),
		Email:    payload.Email,
		Avatar:   payload.Avatar,
		Status:   payload.Status,
		IsAdmin:  payload.IsAdmin,
	}, nil
}

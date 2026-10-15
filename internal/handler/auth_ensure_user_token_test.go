package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type stubEnsureUserTokenService struct {
	interfaces.UserService
	ensureUserToken func(
		ctx context.Context, username string, provisioning types.TenantProvisioningMode,
	) (*types.LoginResponse, error)
}

func (s *stubEnsureUserTokenService) EnsureUserToken(
	ctx context.Context,
	username string,
	provisioning types.TenantProvisioningMode,
) (*types.LoginResponse, error) {
	return s.ensureUserToken(ctx, username, provisioning)
}

func newEnsureUserTokenTestRouter(h *AuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// changePasswordErrorCapture is a generic AppError -> JSON bridge shared
	// with the other auth handler tests.
	r.Use(changePasswordErrorCapture())
	r.POST("/auth/user-token", h.EnsureUserToken)
	return r
}

func doEnsureUserToken(t *testing.T, r *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/auth/user-token", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestEnsureUserToken_ReturnsLoginShapedPayload(t *testing.T) {
	var gotUsername string
	h := &AuthHandler{
		userService: &stubEnsureUserTokenService{
			ensureUserToken: func(
				_ context.Context, username string, provisioning types.TenantProvisioningMode,
			) (*types.LoginResponse, error) {
				gotUsername = username
				if provisioning != types.TenantProvisioningCreatePersonal {
					t.Errorf("provisioning = %q, want the resolved default", provisioning)
				}
				return &types.LoginResponse{
					Success:      true,
					Message:      "Token is still valid",
					User:         &types.User{ID: "user-1", Username: username},
					Memberships:  []types.Membership{},
					Token:        "access-token",
					RefreshToken: "refresh-token",
				}, nil
			},
		},
	}

	w := doEnsureUserToken(t, newEnsureUserTokenTestRouter(h), map[string]string{"username": " alice "})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotUsername != "alice" {
		t.Fatalf("service username = %q, want the trimmed alice", gotUsername)
	}

	var resp struct {
		Success      bool   `json:"success"`
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
		User         *types.User
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success || resp.Token != "access-token" || resp.RefreshToken != "refresh-token" {
		t.Fatalf("payload = %s, want the token pair", w.Body.String())
	}
	if resp.User == nil || resp.User.Username != "alice" {
		t.Fatalf("payload user = %+v, want alice", resp.User)
	}
}

func TestEnsureUserToken_RejectsMissingUsername(t *testing.T) {
	h := &AuthHandler{
		userService: &stubEnsureUserTokenService{
			ensureUserToken: func(
				context.Context, string, types.TenantProvisioningMode,
			) (*types.LoginResponse, error) {
				t.Fatal("service must not be called without a username")
				return nil, nil
			},
		},
	}

	w := doEnsureUserToken(t, newEnsureUserTokenTestRouter(h), map[string]string{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestEnsureUserToken_MapsServiceFailureTo500(t *testing.T) {
	h := &AuthHandler{
		userService: &stubEnsureUserTokenService{
			ensureUserToken: func(
				context.Context, string, types.TenantProvisioningMode,
			) (*types.LoginResponse, error) {
				return nil, errors.New("storage down")
			},
		},
	}

	w := doEnsureUserToken(t, newEnsureUserTokenTestRouter(h), map[string]string{"username": "alice"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d body=%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

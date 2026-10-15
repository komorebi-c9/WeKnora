package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubEnsureTokenTenantService resolves tenants from an in-memory map so the
// active-workspace assertions do not need a database.
type stubEnsureTokenTenantService struct {
	interfaces.TenantService
	tenants map[uint64]*types.Tenant
}

func (s *stubEnsureTokenTenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	if tenant, ok := s.tenants[id]; ok {
		return tenant, nil
	}
	return nil, errors.New("tenant not found")
}

// newEnsureTokenTestService wires the shared auth stubs for the
// /auth/user-token flow. memberService stays nil: loginResponseForToken then
// degrades to the synthFallbackMembership path and no tenant provisioning is
// involved.
func newEnsureTokenTestService(tokenRepo *stubAuthTokenRepo) *userService {
	svc := newAuthTestUserService(tokenRepo)
	svc.tenantService = &stubEnsureTokenTenantService{
		tenants: map[uint64]*types.Tenant{1: {ID: 1, Name: "home"}},
	}
	return svc
}

func TestEnsureUserTokenAutoRegistersAndIssuesTokens(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newEnsureTokenTestService(tokenRepo)

	resp, err := svc.EnsureUserToken(ctx, "alice", types.TenantProvisioningTenantless)
	if err != nil {
		t.Fatalf("EnsureUserToken() err = %v", err)
	}
	if !resp.Success || resp.Token == "" || resp.RefreshToken == "" {
		t.Fatalf("EnsureUserToken() = %+v, want successful token pair", resp)
	}
	if resp.User == nil || resp.User.Username != "alice" {
		t.Fatalf("EnsureUserToken() user = %+v, want username alice", resp.User)
	}
	if resp.User.Email != "alice"+ensureTokenEmailDomain {
		t.Fatalf("EnsureUserToken() email = %q, want alice%s", resp.User.Email, ensureTokenEmailDomain)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(resp.User.PasswordHash), []byte(ensureTokenPassword)); err != nil {
		t.Fatalf("bootstrap password not stored as a matching hash: %v", err)
	}
	if _, _, err := svc.ValidateToken(ctx, resp.Token); err != nil {
		t.Fatalf("issued access token does not validate: %v", err)
	}

	// The access token is reused, not re-minted, on a second call: the
	// contract is "return the existing valid token when there is one".
	second, err := svc.EnsureUserToken(ctx, "alice", types.TenantProvisioningTenantless)
	if err != nil {
		t.Fatalf("second EnsureUserToken() err = %v", err)
	}
	if second.Token != resp.Token {
		t.Fatalf("second call minted a new access token: %q != %q", second.Token, resp.Token)
	}
}

func TestEnsureUserTokenReusesValidAccessToken(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newEnsureTokenTestService(tokenRepo)

	user := &types.User{ID: "user-1", Username: "bob", TenantID: 1, IsActive: true}
	svc.userRepo.(*stubUserRepoForAuth).users[user.ID] = user

	accessToken, refreshToken, err := svc.GenerateTokens(ctx, user)
	if err != nil {
		t.Fatalf("GenerateTokens() err = %v", err)
	}

	resp, err := svc.EnsureUserToken(ctx, "bob", types.TenantProvisioningTenantless)
	if err != nil {
		t.Fatalf("EnsureUserToken() err = %v", err)
	}
	if resp.Token != accessToken {
		t.Fatalf("EnsureUserToken() token = %q, want existing %q", resp.Token, accessToken)
	}
	if resp.RefreshToken != refreshToken {
		t.Fatalf("EnsureUserToken() refresh token = %q, want existing %q", resp.RefreshToken, refreshToken)
	}
	if resp.ActiveTenant == nil || resp.ActiveTenant.ID != 1 {
		t.Fatalf("EnsureUserToken() active tenant = %+v, want tenant 1", resp.ActiveTenant)
	}
	if len(resp.Memberships) != 1 || resp.Memberships[0].TenantID != 1 {
		t.Fatalf("EnsureUserToken() memberships = %+v, want tenant 1", resp.Memberships)
	}
}

func TestEnsureUserTokenRefreshesExpiredAccessToken(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newEnsureTokenTestService(tokenRepo)

	user := &types.User{ID: "user-1", Username: "carol", TenantID: 1, IsActive: true}
	svc.userRepo.(*stubUserRepoForAuth).users[user.ID] = user

	// A stored-but-expired access token plus a still-usable refresh token:
	// the refresh token must be spent instead of a brand-new session minted.
	expiredAccess := signTestJWT(map[string]any{
		"user_id": user.ID, "type": "access", "tenant_id": 1,
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	usableRefresh := signTestJWT(map[string]any{
		"user_id": user.ID, "type": "refresh",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenRepo.tokens[expiredAccess] = &types.AuthToken{
		UserID: user.ID, Token: expiredAccess, TokenType: authTokenTypeAccess,
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	tokenRepo.tokens[usableRefresh] = &types.AuthToken{
		UserID: user.ID, Token: usableRefresh, TokenType: authTokenTypeRefresh,
		ExpiresAt: time.Now().Add(time.Hour),
	}

	resp, err := svc.EnsureUserToken(ctx, "carol", types.TenantProvisioningTenantless)
	if err != nil {
		t.Fatalf("EnsureUserToken() err = %v", err)
	}
	if resp.Token == "" || resp.Token == expiredAccess {
		t.Fatalf("EnsureUserToken() token = %q, want a freshly refreshed token", resp.Token)
	}
	if _, _, err := svc.ValidateToken(ctx, resp.Token); err != nil {
		t.Fatalf("refreshed access token does not validate: %v", err)
	}
	if resp.ActiveTenant == nil || resp.ActiveTenant.ID != 1 {
		t.Fatalf("EnsureUserToken() active tenant = %+v, want tenant 1", resp.ActiveTenant)
	}
	// RefreshToken rotates the pair, so the old refresh token is revoked.
	if !tokenRepo.tokens[usableRefresh].IsRevoked {
		t.Fatal("old refresh token was not revoked after rotation")
	}
	if resp.RefreshToken == usableRefresh || resp.RefreshToken == "" {
		t.Fatalf("EnsureUserToken() refresh token = %q, want a rotated token", resp.RefreshToken)
	}
}

func TestEnsureUserTokenMintsPairWhenNoTokenIsReusable(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newEnsureTokenTestService(tokenRepo)

	user := &types.User{ID: "user-1", Username: "dave", TenantID: 1, IsActive: true}
	svc.userRepo.(*stubUserRepoForAuth).users[user.ID] = user

	// Both stored tokens are expired and revoked: neither the reuse branch
	// nor the refresh branch can succeed.
	expired := time.Now().Add(-time.Hour)
	revokedAccess := signTestJWT(map[string]any{"user_id": user.ID, "type": "access"})
	revokedRefresh := signTestJWT(map[string]any{"user_id": user.ID, "type": "refresh"})
	tokenRepo.tokens[revokedAccess] = &types.AuthToken{
		UserID: user.ID, Token: revokedAccess, TokenType: authTokenTypeAccess,
		ExpiresAt: expired, IsRevoked: true,
	}
	tokenRepo.tokens[revokedRefresh] = &types.AuthToken{
		UserID: user.ID, Token: revokedRefresh, TokenType: authTokenTypeRefresh,
		ExpiresAt: expired, IsRevoked: true,
	}

	resp, err := svc.EnsureUserToken(ctx, "dave", types.TenantProvisioningTenantless)
	if err != nil {
		t.Fatalf("EnsureUserToken() err = %v", err)
	}
	if resp.Token == "" || resp.RefreshToken == "" {
		t.Fatalf("EnsureUserToken() = %+v, want a fresh token pair", resp)
	}
	if resp.Token == revokedAccess || resp.RefreshToken == revokedRefresh {
		t.Fatal("EnsureUserToken() handed back a revoked token")
	}
	if _, _, err := svc.ValidateToken(ctx, resp.Token); err != nil {
		t.Fatalf("freshly minted access token does not validate: %v", err)
	}
}

func TestEnsureUserTokenRejectsDisabledAccount(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newEnsureTokenTestService(tokenRepo)

	svc.userRepo.(*stubUserRepoForAuth).users["user-1"] = &types.User{
		ID: "user-1", Username: "erin", TenantID: 1, IsActive: false,
	}

	resp, err := svc.EnsureUserToken(ctx, "erin", types.TenantProvisioningTenantless)
	if err != nil {
		t.Fatalf("EnsureUserToken() err = %v", err)
	}
	if resp.Success || resp.Token != "" {
		t.Fatalf("EnsureUserToken() = %+v, want rejection for a disabled account", resp)
	}
}

func TestEnsureUserTokenRejectsEmptyUsername(t *testing.T) {
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newEnsureTokenTestService(tokenRepo)

	if _, err := svc.EnsureUserToken(context.Background(), "   ", types.TenantProvisioningTenantless); err == nil {
		t.Fatal("EnsureUserToken(empty username) err = nil, want an error")
	}
}

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ---- fakes -----------------------------------------------------------------

type orionUserRepo struct {
	interfaces.UserRepository
	byID    map[string]*types.User
	byName  map[string]*types.User
	byEmail map[string]*types.User
}

func newOrionUserRepo() *orionUserRepo {
	return &orionUserRepo{
		byID:    map[string]*types.User{},
		byName:  map[string]*types.User{},
		byEmail: map[string]*types.User{},
	}
}

func (r *orionUserRepo) GetUserByID(_ context.Context, id string) (*types.User, error) {
	return r.byID[id], nil
}

func (r *orionUserRepo) GetUserByUsername(_ context.Context, name string) (*types.User, error) {
	return r.byName[name], nil
}

func (r *orionUserRepo) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	return r.byEmail[email], nil
}

func (r *orionUserRepo) CreateUser(_ context.Context, user *types.User) error {
	if _, exists := r.byID[user.ID]; exists {
		return fmt.Errorf("duplicate user %s", user.ID)
	}
	r.byID[user.ID] = user
	r.byName[user.Username] = user
	r.byEmail[user.Email] = user
	return nil
}

func (r *orionUserRepo) DeleteUser(_ context.Context, id string) error {
	user, ok := r.byID[id]
	if !ok {
		return nil
	}
	delete(r.byID, id)
	delete(r.byName, user.Username)
	delete(r.byEmail, user.Email)
	return nil
}

func (r *orionUserRepo) UpdateUser(_ context.Context, user *types.User) error {
	if _, exists := r.byID[user.ID]; !exists {
		return fmt.Errorf("user %s not found", user.ID)
	}
	delete(r.byName, r.byID[user.ID].Username)
	delete(r.byEmail, r.byID[user.ID].Email)
	r.byID[user.ID] = user
	r.byName[user.Username] = user
	r.byEmail[user.Email] = user
	return nil
}

// orionTenantService mimics the tenants sequence: every created workspace gets
// the next id starting at 10000, never the caller-supplied one.
type orionTenantService struct {
	interfaces.TenantService
	tenants map[uint64]*types.Tenant
	nextID  uint64
	created int
	deleted []uint64
}

func newOrionTenantService() *orionTenantService {
	return &orionTenantService{tenants: map[uint64]*types.Tenant{}, nextID: 10000}
}

func (s *orionTenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	if tenant, ok := s.tenants[id]; ok {
		return tenant, nil
	}
	return nil, fmt.Errorf("tenant %d not found", id)
}

func (s *orionTenantService) CreateTenant(_ context.Context, tenant *types.Tenant) (*types.Tenant, error) {
	tenant.ID = s.nextID
	s.nextID++
	s.tenants[tenant.ID] = tenant
	s.created++
	return tenant, nil
}

func (s *orionTenantService) DeleteTenant(_ context.Context, id uint64) error {
	s.deleted = append(s.deleted, id)
	delete(s.tenants, id)
	return nil
}

type orionMemberService struct {
	interfaces.TenantMemberService
	members   []*types.TenantMember
	ownerFail error
}

func (s *orionMemberService) GetMembership(
	_ context.Context, userID string, tenantID uint64,
) (*types.TenantMember, error) {
	for _, m := range s.members {
		if m.UserID == userID && m.TenantID == tenantID {
			return m, nil
		}
	}
	return nil, nil
}

// EnsureOwner mirrors the production contract: idempotent, and an existing row
// keeps its role.
func (s *orionMemberService) EnsureOwner(
	_ context.Context, userID string, tenantID uint64,
) (*types.TenantMember, error) {
	if s.ownerFail != nil {
		return nil, s.ownerFail
	}
	if existing, _ := s.GetMembership(context.Background(), userID, tenantID); existing != nil {
		return existing, nil
	}
	member := &types.TenantMember{UserID: userID, TenantID: tenantID, Role: types.TenantRoleOwner, Status: "active"}
	s.members = append(s.members, member)
	return member, nil
}

func (s *orionMemberService) ListByUser(_ context.Context, userID string) ([]*types.TenantMember, error) {
	out := make([]*types.TenantMember, 0, len(s.members))
	for _, m := range s.members {
		if m.UserID == userID {
			out = append(out, m)
		}
	}
	return out, nil
}

type orionTokenRepo struct {
	interfaces.AuthTokenRepository
	created int
}

func (r *orionTokenRepo) CreateToken(_ context.Context, _ *types.AuthToken) error {
	r.created++
	return nil
}

// ---- helpers ---------------------------------------------------------------

type orionFixture struct {
	svc     *userService
	users   *orionUserRepo
	tenants *orionTenantService
	members *orionMemberService
}

func newOrionFixture(t *testing.T, handler http.HandlerFunc) *orionFixture {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	f := &orionFixture{
		users:   newOrionUserRepo(),
		tenants: newOrionTenantService(),
		members: &orionMemberService{},
	}
	f.svc = &userService{
		userRepo:      f.users,
		tenantService: f.tenants,
		memberService: f.members,
		tokenRepo:     &orionTokenRepo{},
		config: &config.Config{OrionAuth: &config.OrionAuthConfig{
			Enable:         true,
			BaseURL:        srv.URL,
			MePath:         "/orion/api/auth/current",
			TimeoutSeconds: 2,
		}},
	}
	return f
}

// orionIdentityHandler returns the shape Orion actually serves. orgId and
// orgName are included on purpose: the mirror must ignore them. isAdmin is
// consumed — an Orion admin is mirrored as a WeKnora system administrator.
func orionIdentityHandler(id, orgID int64, username, email string, status int, isAdmin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		emailJSON := "null"
		if email != "" {
			emailJSON = `"` + email + `"`
		}
		fmt.Fprintf(w, `{"code":200,"message":"ok","data":{
			"id":%d,"orgId":%d,"orgName":"默认机构","name":"管理员",
			"username":"%s","email":%s,"avatar":null,"status":%d,"isAdmin":%t}}`,
			id, orgID, username, emailJSON, status, isAdmin)
	}
}

// ---- tests -----------------------------------------------------------------

func TestLoginWithOrionProvisionsPersonalWorkspace(t *testing.T) {
	f := newOrionFixture(t, orionIdentityHandler(100000001, 1, "admin", "", 1, true))

	resp, err := f.svc.LoginWithOrion(context.Background(), "assertion")
	if err != nil {
		t.Fatalf("LoginWithOrion() error = %v", err)
	}

	if resp.Token == "" || resp.RefreshToken == "" {
		t.Fatal("expected a token pair")
	}
	if resp.ActiveTenant == nil {
		t.Fatal("expected an active workspace")
	}
	// The workspace belongs to the user, not to their Orion organisation.
	if resp.ActiveTenant.ID == 1 {
		t.Fatalf("active tenant id = %d, must not be the Orion org id", resp.ActiveTenant.ID)
	}
	if resp.ActiveTenant.Name != "admin's Workspace" {
		t.Fatalf("workspace name = %q, want the registration form", resp.ActiveTenant.Name)
	}
	if resp.ActiveTenant.Description != "Default workspace" {
		t.Fatalf("workspace description = %q, want Default workspace", resp.ActiveTenant.Description)
	}

	user := f.users.byID["100000001"]
	if user == nil {
		t.Fatal("user was not provisioned with the Orion id")
	}
	if user.TenantID != resp.ActiveTenant.ID {
		t.Fatalf("user tenant = %d, want %d", user.TenantID, resp.ActiveTenant.ID)
	}
	if user.Username != "admin" {
		t.Fatalf("username = %q, want admin", user.Username)
	}
	if user.Email != "orion-100000001@orion.invalid" {
		t.Fatalf("email = %q, want the reserved placeholder", user.Email)
	}
	if !user.IsSystemAdmin {
		t.Fatal("an Orion org admin must be mirrored as a WeKnora system administrator")
	}
	if !strings.HasPrefix(user.PasswordHash, orionPasswordPrefix) {
		t.Fatalf("password hash = %q, want the unusable placeholder", user.PasswordHash)
	}

	if len(f.members.members) != 1 {
		t.Fatalf("members = %d, want exactly one", len(f.members.members))
	}
	if f.members.members[0].Role != types.TenantRoleOwner {
		t.Fatalf("role = %s, want owner", f.members.members[0].Role)
	}
	if len(resp.Memberships) != 1 || resp.Memberships[0].Role != types.TenantRoleOwner {
		t.Fatalf("response memberships = %+v, want one owner", resp.Memberships)
	}
}

func TestLoginWithOrionIsIdempotent(t *testing.T) {
	f := newOrionFixture(t, orionIdentityHandler(7, 3, "bob", "bob@example.com", 1, false))
	ctx := context.Background()

	first, err := f.svc.LoginWithOrion(ctx, "a")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	second, err := f.svc.LoginWithOrion(ctx, "a")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}

	if len(f.users.byID) != 1 || len(f.tenants.tenants) != 1 || len(f.members.members) != 1 {
		t.Fatalf("rows grew on re-login: users=%d tenants=%d members=%d",
			len(f.users.byID), len(f.tenants.tenants), len(f.members.members))
	}
	if f.tenants.created != 1 {
		t.Fatalf("workspaces created = %d, want 1", f.tenants.created)
	}
	if first.ActiveTenant.ID != second.ActiveTenant.ID {
		t.Fatalf("workspace changed between logins: %d -> %d",
			first.ActiveTenant.ID, second.ActiveTenant.ID)
	}
}

func TestLoginWithOrionReusesExistingAccount(t *testing.T) {
	f := newOrionFixture(t, orionIdentityHandler(7, 3, "bob", "bob@example.com", 1, false))

	// Pre-existing account in someone else's workspace, with a role the mirror
	// must not touch.
	f.tenants.tenants[42] = &types.Tenant{ID: 42, Name: "shared", Status: "active"}
	f.users.byID["7"] = &types.User{ID: "7", Username: "bob", Email: "bob@example.com", TenantID: 42}
	f.users.byName["bob"] = f.users.byID["7"]
	f.users.byEmail["bob@example.com"] = f.users.byID["7"]
	f.members.members = append(f.members.members,
		&types.TenantMember{UserID: "7", TenantID: 42, Role: types.TenantRoleContributor, Status: "active"})

	resp, err := f.svc.LoginWithOrion(context.Background(), "a")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if resp.ActiveTenant.ID != 42 {
		t.Fatalf("active tenant = %d, want the existing 42", resp.ActiveTenant.ID)
	}
	if f.tenants.created != 0 {
		t.Fatalf("workspaces created = %d, a known account must not get a new one", f.tenants.created)
	}
	if len(f.members.members) != 1 || f.members.members[0].Role != types.TenantRoleContributor {
		t.Fatalf("members = %+v, want the existing contributor row untouched", f.members.members)
	}
	if f.users.byID["7"].IsSystemAdmin {
		t.Fatal("a plain Orion member must not be promoted to system admin")
	}
}

// TestLoginWithOrionGrantsSystemAdminToExistingAccount proves a later login of
// an Orion org admin upgrades the existing account, while their workspace and
// membership role stay untouched.
func TestLoginWithOrionGrantsSystemAdminToExistingAccount(t *testing.T) {
	f := newOrionFixture(t, orionIdentityHandler(7, 3, "bob", "bob@example.com", 1, true))

	f.tenants.tenants[42] = &types.Tenant{ID: 42, Name: "shared", Status: "active"}
	f.users.byID["7"] = &types.User{ID: "7", Username: "bob", Email: "bob@example.com", TenantID: 42}
	f.users.byName["bob"] = f.users.byID["7"]
	f.users.byEmail["bob@example.com"] = f.users.byID["7"]

	resp, err := f.svc.LoginWithOrion(context.Background(), "a")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if !resp.User.IsSystemAdmin {
		t.Fatal("an Orion org admin must be granted WeKnora system admin")
	}
	if !f.users.byID["7"].IsSystemAdmin {
		t.Fatal("the upgrade was not persisted")
	}
	if f.tenants.created != 0 {
		t.Fatalf("workspaces created = %d, the account must keep its own", f.tenants.created)
	}
}

func TestLoginWithOrionRollsBackWhenOwnershipFails(t *testing.T) {
	f := newOrionFixture(t, orionIdentityHandler(7, 3, "bob", "", 1, false))
	f.members.ownerFail = errors.New("boom")

	if _, err := f.svc.LoginWithOrion(context.Background(), "a"); err == nil {
		t.Fatal("expected an error when ownership cannot be established")
	}
	if len(f.users.byID) != 0 {
		t.Fatalf("user was not rolled back: %+v", f.users.byID)
	}
	if len(f.tenants.tenants) != 0 || len(f.tenants.deleted) != 1 {
		t.Fatalf("workspace was not rolled back: alive=%d deleted=%v",
			len(f.tenants.tenants), f.tenants.deleted)
	}
}

func TestLoginWithOrionDisambiguatesUsername(t *testing.T) {
	f := newOrionFixture(t, orionIdentityHandler(9, 4, "taken", "", 1, false))
	other := &types.User{ID: "someone-else", Username: "taken", Email: "other@example.com"}
	f.users.byID[other.ID] = other
	f.users.byName[other.Username] = other

	resp, err := f.svc.LoginWithOrion(context.Background(), "a")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.User.Username != "taken@orion" {
		t.Fatalf("username = %q, want the disambiguated form", resp.User.Username)
	}
}

func TestLoginWithOrionRejectsInactiveAccount(t *testing.T) {
	f := newOrionFixture(t, orionIdentityHandler(9, 4, "bob", "", 3, false))

	_, err := f.svc.LoginWithOrion(context.Background(), "a")
	if !errors.Is(err, ErrOrionAccountInactive) {
		t.Fatalf("err = %v, want ErrOrionAccountInactive", err)
	}
}

func TestLoginWithOrionMapsUpstreamErrors(t *testing.T) {
	t.Run("rejected credential", func(t *testing.T) {
		f := newOrionFixture(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		if _, err := f.svc.LoginWithOrion(context.Background(), "a"); !errors.Is(err, ErrOrionCredentialInvalid) {
			t.Fatalf("err = %v, want ErrOrionCredentialInvalid", err)
		}
	})

	t.Run("unreachable upstream", func(t *testing.T) {
		f := newOrionFixture(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		})
		if _, err := f.svc.LoginWithOrion(context.Background(), "a"); !errors.Is(err, ErrOrionUnavailable) {
			t.Fatalf("err = %v, want ErrOrionUnavailable", err)
		}
	})
}

func TestLoginWithOrionRequiresEnabledConfig(t *testing.T) {
	svc := &userService{config: &config.Config{}}
	if _, err := svc.LoginWithOrion(context.Background(), "a"); !errors.Is(err, ErrOrionDisabled) {
		t.Fatalf("err = %v, want ErrOrionDisabled", err)
	}
}

func TestWithEmailTag(t *testing.T) {
	cases := map[string]string{
		"bob@example.com": "bob+7@example.com",
		"bob":             "bob+7",
		"a@b@example.com": "a@b+7@example.com",
	}
	for input, want := range cases {
		if got := withEmailTag(input, "7"); got != want {
			t.Fatalf("withEmailTag(%q) = %q, want %q", input, got, want)
		}
	}
}

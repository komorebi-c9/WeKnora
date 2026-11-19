package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/infrastructure/orion"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// Errors surfaced by LoginWithOrion. Handlers translate them into HTTP status
// codes; they are intentionally distinct so the SPA can tell "your Orion
// session expired" (re-open from Orion) apart from "Orion is down".
var (
	// ErrOrionDisabled is returned when orion_auth.enable is false.
	ErrOrionDisabled = errors.New("orion sso is not enabled")
	// ErrOrionCredentialInvalid means Orion rejected the assertion.
	ErrOrionCredentialInvalid = errors.New("orion credential is invalid")
	// ErrOrionUnavailable means Orion could not be reached or answered with
	// an error, so the login cannot be decided either way.
	ErrOrionUnavailable = errors.New("orion identity service unavailable")
	// ErrOrionAccountInactive means the Orion account is locked or cancelled.
	ErrOrionAccountInactive = errors.New("orion account is not active")
)

const (
	// orionPlaceholderEmailDomain uses the reserved .invalid TLD so the
	// synthetic address can never route anywhere.
	orionPlaceholderEmailDomain = "orion.invalid"
	// orionPasswordPrefix is never a valid bcrypt hash, so bcrypt comparison
	// always fails. Orion users cannot log in with a local password.
	orionPasswordPrefix = "!orion-sso:"
	orionUsernameMaxLen = 100
)

// LoginWithOrion validates an Orion assertion (NS-TOKEN) and issues a WeKnora
// session for the identity Orion reports.
//
// The user id comes exclusively from the validated identity: the caller cannot
// supply it, otherwise anyone holding a public number could impersonate another
// account. Exactly one upstream call is made.
//
// Every mirrored user owns a personal workspace rather than sharing the one
// their Orion organisation maps to, so orgId/orgName/isAdmin are never read.
func (s *userService) LoginWithOrion(ctx context.Context, assertion string) (*types.LoginResponse, error) {
	cfg := s.config
	if cfg == nil || !cfg.OrionAuth.IsEnabled() {
		return nil, ErrOrionDisabled
	}

	client := orion.NewClient(cfg.OrionAuth.BaseURL, cfg.OrionAuth.MePath, cfg.OrionAuth.Timeout())
	identity, err := client.Me(ctx, assertion)
	if err != nil {
		if errors.Is(err, orion.ErrUnauthorized) {
			logger.Warn(ctx, "Orion rejected the credential presented for SSO")
			return nil, ErrOrionCredentialInvalid
		}
		logger.Errorf(ctx, "Orion identity lookup failed: %v", err)
		return nil, ErrOrionUnavailable
	}
	// Status is optional: Orion builds that do not return it must not reject
	// every login, so an absent value is read as "normal".
	if identity.Status != nil && *identity.Status != 1 {
		logger.Warnf(ctx, "Orion user %d is not active (status=%d)", identity.ID, *identity.Status)
		return nil, ErrOrionAccountInactive
	}

	user, err := s.ensureOrionAccount(ctx, identity)
	if err != nil {
		return nil, err
	}

	accessToken, refreshToken, err := s.generateTokensForTenant(ctx, user, user.TenantID)
	if err != nil {
		logger.Errorf(ctx, "Failed to issue tokens for Orion user %s: %v", user.ID, err)
		return nil, err
	}

	logger.Infof(ctx, "Orion SSO login succeeded: user=%s tenant=%d", user.ID, user.TenantID)
	return s.loginResponseForToken(ctx, user, accessToken, refreshToken, user.TenantID,
		"Login successful"), nil
}

// ensureOrionAccount returns the local account whose id equals the Orion user
// id, provisioning one on first sight.
//
// The provisioning steps are deliberately the same ones Register uses: create a
// personal workspace, create the account inside it, then make the account the
// owner. Existing accounts are returned untouched — no workspace, username,
// role or platform-admin flag is rewritten on a later login.
func (s *userService) ensureOrionAccount(
	ctx context.Context, identity *orion.Identity,
) (*types.User, error) {
	userID := strconv.FormatInt(identity.ID, 10)
	if existing, err := s.userRepo.GetUserByID(ctx, userID); err == nil && existing != nil {
		return existing, nil
	}

	username, err := s.orionAvailableUsername(ctx, identity, userID)
	if err != nil {
		return nil, err
	}
	email, err := s.orionAvailableEmail(ctx, identity)
	if err != nil {
		return nil, err
	}

	tenant, err := s.createPersonalWorkspace(ctx, username)
	if err != nil {
		logger.Errorf(ctx, "Failed to create personal workspace for Orion user %s: %v", userID, err)
		return nil, err
	}

	user := &types.User{
		ID:           userID,
		Username:     username,
		Email:        email,
		PasswordHash: orionPasswordPrefix + uuid.NewString(),
		TenantID:     tenant.ID,
		IsActive:     true,
		// Orion exposes no platform-admin flag WeKnora acts on, so every
		// mirrored account is a plain member of the platform.
		IsSystemAdmin: false,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if identity.Avatar != nil {
		user.Avatar = strings.TrimSpace(*identity.Avatar)
	}

	if createErr := s.userRepo.CreateUser(ctx, user); createErr != nil {
		// A concurrent first login may have inserted the same row between the
		// read and the insert: keep that account and drop the workspace we
		// just created so it does not become an orphan.
		s.discardWorkspace(ctx, tenant.ID)
		if existing, getErr := s.userRepo.GetUserByID(ctx, userID); getErr == nil && existing != nil {
			return existing, nil
		}
		logger.Errorf(ctx, "Failed to create user %s from Orion identity: %v", userID, createErr)
		return nil, createErr
	}

	// Same invariant as Register: the account owns the workspace it was just
	// given. Failure here rolls everything back rather than leaving a
	// workspace nobody can administer.
	if s.memberService != nil {
		if _, err := s.memberService.EnsureOwner(ctx, user.ID, tenant.ID); err != nil {
			logger.Errorf(ctx, "Failed to make Orion user %s the owner of workspace %d: %v",
				user.ID, tenant.ID, err)
			_ = s.userRepo.DeleteUser(ctx, user.ID)
			s.discardWorkspace(ctx, tenant.ID)
			return nil, errors.New("failed to finalise workspace ownership")
		}
	}

	logger.Infof(ctx, "Provisioned Orion user %s with personal workspace %d", userID, tenant.ID)
	return user, nil
}

// discardWorkspace removes a workspace created for an account that then failed
// to materialise, so a failed login does not leave an ownerless tenant behind.
func (s *userService) discardWorkspace(ctx context.Context, tenantID uint64) {
	if tenantID == 0 {
		return
	}
	if err := s.tenantService.DeleteTenant(ctx, tenantID); err != nil {
		logger.Warnf(ctx, "Failed to roll back workspace %d after Orion provisioning failure: %v",
			tenantID, err)
	}
}

// orionAvailableUsername returns the Orion username, disambiguated if the
// same string already belongs to a different local account.
func (s *userService) orionAvailableUsername(
	ctx context.Context, identity *orion.Identity, userID string,
) (string, error) {
	base := strings.TrimSpace(identity.Username)
	if base == "" {
		base = "orion-" + userID
	}

	candidates := []string{base, base + "@orion", base + "@orion" + userID}
	for _, candidate := range candidates {
		candidate = orionTruncateRunes(candidate, orionUsernameMaxLen)
		existing, err := s.userRepo.GetUserByUsername(ctx, candidate)
		if err != nil || existing == nil {
			return candidate, nil
		}
		if existing.ID == userID {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot resolve a unique username for Orion user %s", userID)
}

// orionAvailableEmail returns the Orion email, falling back to a reserved
// placeholder when Orion has none (users.email is NOT NULL UNIQUE) and
// disambiguating when the address already belongs to another account.
func (s *userService) orionAvailableEmail(ctx context.Context, identity *orion.Identity) (string, error) {
	raw := ""
	if identity.Email != nil {
		raw = strings.TrimSpace(*identity.Email)
	}
	if raw == "" {
		return fmt.Sprintf("orion-%d@%s", identity.ID, orionPlaceholderEmailDomain), nil
	}

	candidates := []string{raw, withEmailTag(raw, strconv.FormatInt(identity.ID, 10))}
	for _, candidate := range candidates {
		existing, err := s.userRepo.GetUserByEmail(ctx, candidate)
		if err != nil || existing == nil {
			return candidate, nil
		}
		if existing.ID == strconv.FormatInt(identity.ID, 10) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot resolve a unique email for Orion user %d", identity.ID)
}

// withEmailTag inserts "+<tag>" into the local part of an address.
func withEmailTag(email, tag string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return email + "+" + tag
	}
	return email[:at] + "+" + tag + email[at:]
}

func orionTruncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

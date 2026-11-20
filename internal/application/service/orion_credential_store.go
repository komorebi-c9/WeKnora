package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
)

// OrionAPIKeySettingKey is the system_settings row that holds the
// platform-wide Orion License SK. The value is the ciphertext produced by
// utils.EncryptAESGCM ("enc:v1:..."), JSON-encoded as a string so the
// jsonb column roundtrips it losslessly.
//
// The key is deliberately NOT registered in SystemSettingService.registry:
// the generic settings page would otherwise render the ciphertext (its List
// preserves out-of-band rows), and while Reset rejects unregistered keys, a
// registered string row would invite edits through the wrong UI. Only
// OrionCredentialStore and the Orion model handler touch this row; service
// List additionally hides it from the management UI.
const OrionAPIKeySettingKey = "orion.api_key"

// orionAPIKeyMaxLen bounds the plaintext licence key (sk-...). The users
// column it replaces was varchar(512); keep the same ceiling.
const orionAPIKeyMaxLen = 512

// ErrOrionAPIKeyInvalid is returned when a save payload fails validation.
var ErrOrionAPIKeyInvalid = errors.New("orion api key is invalid")

// OrionCredentialStore owns the single platform-wide Orion credential.
// It hides encryption from every caller: reads return the decrypted,
// trimmed key, writes encrypt before persisting. The key never appears in
// logs, error messages or API responses.
type OrionCredentialStore struct {
	repo interfaces.SystemSettingRepository
}

// NewOrionCredentialStore builds the store.
func NewOrionCredentialStore(repo interfaces.SystemSettingRepository) *OrionCredentialStore {
	return &OrionCredentialStore{repo: repo}
}

// GetAPIKey returns the decrypted, trimmed global key. An empty string
// means "not configured".
func (s *OrionCredentialStore) GetAPIKey(ctx context.Context) (string, error) {
	if s == nil || s.repo == nil {
		return "", nil
	}
	row, err := s.repo.Get(ctx, OrionAPIKeySettingKey)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", nil
	}
	ciphertext, err := row.AsString()
	if err != nil {
		return "", fmt.Errorf("system_setting %q: %w", OrionAPIKeySettingKey, err)
	}
	if ciphertext == "" {
		return "", nil
	}
	plaintext, err := utils.DecryptAESGCM(ciphertext, utils.GetAESKey())
	if err != nil {
		return "", fmt.Errorf("system_setting %q: decrypt failed", OrionAPIKeySettingKey)
	}
	return strings.TrimSpace(plaintext), nil
}

// Configured reports whether a non-empty global key exists. Read failures
// surface as an error so the caller can distinguish "not configured" from
// "storage is broken".
func (s *OrionCredentialStore) Configured(ctx context.Context) (bool, error) {
	key, err := s.GetAPIKey(ctx)
	if err != nil {
		return false, err
	}
	return key != "", nil
}

// SaveAPIKey validates, encrypts and persists the global key. The write is
// synchronous: when this returns nil the key is already effective globally
// (every subsequent sync reads it).
func (s *OrionCredentialStore) SaveAPIKey(ctx context.Context, apiKey string) error {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return fmt.Errorf("%w: empty", ErrOrionAPIKeyInvalid)
	}
	if len(key) > orionAPIKeyMaxLen {
		return fmt.Errorf("%w: exceeds %d bytes", ErrOrionAPIKeyInvalid, orionAPIKeyMaxLen)
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return fmt.Errorf("%w: contains whitespace", ErrOrionAPIKeyInvalid)
	}

	ciphertext, err := utils.EncryptAESGCM(key, utils.GetAESKey())
	if err != nil {
		return fmt.Errorf("encrypt orion api key failed: %w", err)
	}
	encoded, err := json.Marshal(ciphertext)
	if err != nil {
		return fmt.Errorf("encode orion api key failed: %w", err)
	}

	now := time.Now()
	actor, _ := types.UserIDFromContext(ctx)
	row := &types.SystemSetting{
		Key:             OrionAPIKeySettingKey,
		Value:           types.JSON(encoded),
		ValueType:       "string",
		Category:        "orion",
		Description:     "Orion License SK（全局）。由平台管理员在模型管理页面配置，所有工作空间共用。",
		IsSecret:        true,
		RequiresRestart: false,
		LastModifiedBy:  actor,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.repo.Upsert(ctx, row); err != nil {
		return fmt.Errorf("persist orion api key failed: %w", err)
	}
	logger.Infof(ctx, "[orion-model] global orion api key updated by %s", actor)
	return nil
}

// KeyHint renders a display-only mask of the stored key ("sk-***abcd").
// Returns "" when the key is absent. Never returns the plaintext.
func (s *OrionCredentialStore) KeyHint(ctx context.Context) (string, error) {
	key, err := s.GetAPIKey(ctx)
	if err != nil || key == "" {
		return "", err
	}
	return maskOrionKey(key), nil
}

// maskOrionKey keeps the first three characters and the last four, so the
// operator can recognise which licence they configured without any chance
// of reconstructing the secret.
func maskOrionKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:3] + "***" + key[len(key)-4:]
}

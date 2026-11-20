package types

import (
	"database/sql/driver"
	"errors"
	"log"

	"github.com/Tencent/WeKnora/internal/utils"
)

// EncryptedString is a database column whose plaintext value must never be
// stored in the clear. It mirrors the credential handling already used by
// ModelParameters (see model.go): Value encrypts with AES-256-GCM when
// SYSTEM_AES_KEY is configured, Scan decrypts leniently so a rotated or
// missing key degrades the single field to "unconfigured" instead of failing
// the whole row load.
//
// The type deliberately has no JSON representation: MarshalJSON returns an
// empty string so a field that is accidentally serialised (missing the
// `json:"-"` tag) can never leak the secret. Values must only be read in the
// backend that owns the credential.
type EncryptedString string

// Value implements driver.Valuer. Empty values stay empty so the column keeps
// its NOT NULL DEFAULT ” shape and "no credential" is unambiguous.
func (s EncryptedString) Value() (driver.Value, error) {
	plaintext := string(s)
	if plaintext == "" {
		return "", nil
	}
	key := utils.GetAESKey()
	if key == nil {
		// No encryption key configured: keep the legacy plaintext behaviour
		// rather than dropping the credential. EncryptAESGCM already returns
		// the input unchanged for a nil key, this branch only makes it explicit.
		return plaintext, nil
	}
	encrypted, err := utils.EncryptAESGCM(plaintext, key)
	if err != nil {
		return nil, err
	}
	return encrypted, nil
}

// Scan implements sql.Scanner. A value that carries the enc:v1: prefix but
// cannot be decrypted is reported as empty with a warning: the row still
// loads, and the caller treats the credential as unconfigured.
func (s *EncryptedString) Scan(value interface{}) error {
	if value == nil {
		*s = ""
		return nil
	}
	var raw string
	switch v := value.(type) {
	case []byte:
		raw = string(v)
	case string:
		raw = v
	default:
		return errors.New("EncryptedString.Scan: unsupported type")
	}
	if raw == "" {
		*s = ""
		return nil
	}
	plaintext, ok := utils.DecryptStoredSecretLenient(raw)
	if !ok {
		log.Printf("[crypto] encrypted column: decrypt failed (SYSTEM_AES_KEY missing/rotated?), treating as unconfigured")
		*s = ""
		return nil
	}
	*s = EncryptedString(plaintext)
	return nil
}

// String returns the plaintext. Callers must not log the result.
func (s EncryptedString) String() string {
	return string(s)
}

// IsEmpty reports whether the credential is absent.
func (s EncryptedString) IsEmpty() bool {
	return s == ""
}

// MarshalJSON never emits the secret. This is defence in depth for fields that
// forget `json:"-"`.
func (s EncryptedString) MarshalJSON() ([]byte, error) {
	return []byte(`""`), nil
}

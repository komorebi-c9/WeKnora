-- Migration: 000054_users_orion_api_key (SQLite)
-- Mirror of migrations/versioned/000138_users_orion_api_key.up.sql.
-- The value is ciphertext (enc:v1:...) produced by EncryptedString; empty means
-- "no Orion credential configured".

ALTER TABLE users ADD COLUMN orion_api_key VARCHAR(512) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_users_orion_api_key_present
    ON users (tenant_id, created_at)
    WHERE orion_api_key <> '';

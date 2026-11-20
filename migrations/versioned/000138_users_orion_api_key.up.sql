-- Migration: 000138_users_orion_api_key
-- Give every user their own Orion credential.
--
-- Background:
--   * WeKnora now sources its model catalogue from Orion (ORION_MODEL_ENABLE).
--     The catalogue is per-user: each user's Orion License SK decides which
--     models they may see and consume, so the credential has to live on the
--     user row rather than in a process-wide environment variable.
--   * The value is encrypted at rest by the application (EncryptedString,
--     AES-256-GCM with the SYSTEM_AES_KEY, `enc:v1:` prefix), exactly like
--     models.parameters.api_key. The column therefore only needs to be wide
--     enough for the ciphertext.
--   * An empty string means "no Orion credential configured". Those users see
--     no Orion models; the rest of the product keeps working.
--
-- This migration is idempotent (IF NOT EXISTS) and safe to re-run.

DO $$ BEGIN RAISE NOTICE '[Migration 000138] Adding users.orion_api_key column'; END $$;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS orion_api_key VARCHAR(512) NOT NULL DEFAULT '';

-- The synchroniser pages over "users that have a credential". A partial index
-- keeps that scan cheap and ignores the (usually dominant) empty rows.
CREATE INDEX IF NOT EXISTS idx_users_orion_api_key_present
    ON users (tenant_id, created_at)
    WHERE orion_api_key <> '';

DO $$ BEGIN RAISE NOTICE '[Migration 000138] Done'; END $$;

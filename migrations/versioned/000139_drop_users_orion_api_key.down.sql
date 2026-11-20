-- Down migration for 000139_drop_users_orion_api_key.
-- Restores the per-user credential column; values are lost (empty) and must
-- be re-provisioned externally, matching pre-000139 semantics.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS orion_api_key VARCHAR(512) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_users_orion_api_key_present
    ON users (tenant_id, created_at)
    WHERE orion_api_key <> '';

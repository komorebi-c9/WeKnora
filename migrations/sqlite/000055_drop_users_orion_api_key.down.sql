-- Down migration for 000055_drop_users_orion_api_key (SQLite).

ALTER TABLE users ADD COLUMN orion_api_key VARCHAR(512) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_users_orion_api_key_present
    ON users (tenant_id, created_at)
    WHERE orion_api_key <> '';

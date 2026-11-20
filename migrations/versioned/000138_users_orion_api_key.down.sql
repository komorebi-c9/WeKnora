-- Down migration for 000138_users_orion_api_key.

DROP INDEX IF EXISTS idx_users_orion_api_key_present;

ALTER TABLE users DROP COLUMN IF EXISTS orion_api_key;

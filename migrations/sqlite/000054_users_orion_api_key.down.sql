-- Down migration for 000054_users_orion_api_key (SQLite).

DROP INDEX IF EXISTS idx_users_orion_api_key_present;

ALTER TABLE users DROP COLUMN orion_api_key;

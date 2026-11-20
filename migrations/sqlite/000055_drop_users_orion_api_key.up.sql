-- Migration: 000055_drop_users_orion_api_key (SQLite)
-- Mirror of migrations/versioned/000139_drop_users_orion_api_key.up.sql.

DROP INDEX IF EXISTS idx_users_orion_api_key_present;

ALTER TABLE users DROP COLUMN orion_api_key;

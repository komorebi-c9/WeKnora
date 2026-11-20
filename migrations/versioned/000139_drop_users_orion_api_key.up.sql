-- Migration: 000139_drop_users_orion_api_key
-- Drop the per-user Orion credential in favour of one platform-wide key.
--
-- Background:
--   * Model synchronisation no longer reads a credential per user row.
--     Instead a single global Orion License SK is configured by the platform
--     administrator at runtime (stored encrypted in system_settings under
--     the key "orion.api_key") and shared by every workspace.
--   * The per-user column (added by migration 000138) is therefore dead
--     weight and is removed here. Any still-populated values are dropped
--     with the column; operators should re-enter the key once via the
--     model management page after upgrading.
--
-- This migration is idempotent (IF EXISTS) and safe to re-run.

DO $$ BEGIN RAISE NOTICE '[Migration 000139] Dropping users.orion_api_key column'; END $$;

DROP INDEX IF EXISTS idx_users_orion_api_key_present;

ALTER TABLE users
    DROP COLUMN IF EXISTS orion_api_key;

DO $$ BEGIN RAISE NOTICE '[Migration 000139] Done'; END $$;

ALTER TABLE _notifications DROP COLUMN IF EXISTS digested_at;
DROP INDEX IF EXISTS idx__notification_deferred_user_reason;
DROP INDEX IF EXISTS idx__notification_deferred_flush;
DROP TABLE IF EXISTS _notification_deferred;
DROP TABLE IF EXISTS _notification_user_settings;

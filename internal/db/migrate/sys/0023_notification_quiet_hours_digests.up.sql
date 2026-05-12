-- v1.7.34 — Notification quiet hours + digest support (§3.9.1 deferred).
--
-- Two new bits land in this migration:
--
--   1. `_notification_user_settings` — per-user (not per-kind/channel)
--      configuration for quiet hours + digest mode. The existing
--      `_notification_preferences` table is PK'd on (user_id, kind,
--      channel), so each row is a single opt-in toggle — there's no
--      sensible place to put a user-wide setting like "my quiet hours
--      are 22:00-07:00 Europe/Moscow". A new table keyed on user_id
--      alone gives us the right cardinality without overloading the
--      preferences shape. Missing row = defaults (no quiet hours, no
--      digest, send immediately).
--
--   2. `_notification_deferred` — buffer of notifications whose
--      delivery was held back, either because the user was in quiet
--      hours when Send fired, or because their digest mode bundles
--      multiple notifications into a single periodic email. The cron
--      builtin `flush_deferred_notifications` walks this table on a
--      */5 cadence and either re-sends (quiet hours expired) or rolls
--      a digest email and marks the notifications as digested.
--
-- The notification row itself in `_notifications` is ALWAYS persisted
-- on Send so the in-app bell view + admin Notifications screen don't
-- lose visibility while the row sits deferred — the deferral only
-- gates the email/push side-effect channels.

CREATE TABLE _notification_user_settings (
    user_id            UUID         PRIMARY KEY,

    -- Quiet hours window. NULL start OR end = quiet hours disabled.
    -- Wrap-around (e.g. 22:00-07:00) is allowed and handled in code
    -- via withinQuietHours. Stored as TIME (no date component) so the
    -- window is interpreted day-by-day in the user's tz.
    quiet_hours_start  TIME,
    quiet_hours_end    TIME,

    -- IANA timezone identifier (e.g. 'Europe/Moscow'). Validated by
    -- the caller via time.LoadLocation before insert. NULL = UTC.
    quiet_hours_tz     TEXT,

    -- Digest mode. 'off' bypasses the digest path; 'daily'/'weekly'
    -- bundle notifications into a single email at digest_hour (in
    -- digest_tz). digest_dow is consulted only for 'weekly' mode
    -- (0=Sunday … 6=Saturday, ISO-ish — matches Postgres EXTRACT(dow)).
    digest_mode        TEXT         NOT NULL DEFAULT 'off'
                                    CHECK (digest_mode IN ('off','daily','weekly')),
    digest_hour        SMALLINT     NOT NULL DEFAULT 8
                                    CHECK (digest_hour BETWEEN 0 AND 23),
    digest_dow         SMALLINT     NOT NULL DEFAULT 1
                                    CHECK (digest_dow BETWEEN 0 AND 6),

    -- digest_tz separate from quiet_hours_tz: most users want them
    -- aligned, but a traveller / scheduling power-user can decouple.
    -- NULL falls back to quiet_hours_tz, then UTC.
    digest_tz          TEXT,

    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Deferred-delivery queue. One row per (notification, reason) — a
-- single notification can in principle be deferred under multiple
-- reasons (e.g. quiet hours wraps into the next digest window), but
-- v1 keeps it 1:1 by handling quiet-hours-and-digest precedence at
-- Send time (quiet hours wins; see service.go).
CREATE TABLE _notification_deferred (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID        NOT NULL,
    notification_id UUID        NOT NULL REFERENCES _notifications(id) ON DELETE CASCADE,
    deferred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- When this row becomes eligible for processing. For quiet-hours
    -- rows: end-of-window. For digest rows: next_digest_time(mode).
    flush_after     TIMESTAMPTZ NOT NULL,

    reason          TEXT        NOT NULL
                                CHECK (reason IN ('quiet_hours','digest'))
);

-- Hot path: "what's eligible to flush right now?". Partial index on
-- flush_after keeps the scan size proportional to PAST rows (the
-- queue depth at flush time), not the full backlog.
CREATE INDEX idx__notification_deferred_flush
    ON _notification_deferred (flush_after);

-- Per-user grouping for digest assembly — the flush handler groups
-- by user_id when reason='digest'.
CREATE INDEX idx__notification_deferred_user_reason
    ON _notification_deferred (user_id, reason);

-- Mark of having been bundled into a sent digest email. NULL = not
-- yet digested (or never will be — quiet-hours path doesn't touch
-- this column). Operators can audit "what landed in which digest"
-- by joining on the deferred row's deferred_at proximity.
ALTER TABLE _notifications
    ADD COLUMN digested_at TIMESTAMPTZ;

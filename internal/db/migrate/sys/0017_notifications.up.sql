-- v1.5.3 — Notifications system (§3.9.1 / docs/20).
--
-- Two tables:
--   _notifications              — one row per delivered notification.
--                                 Audit + read/unread state. Multi-tenant
--                                 (tenant_id NULL for site-scope).
--   _notification_preferences   — per-user opt-in/out per (kind, channel).
--                                 Missing row = use default (channel-default).
--
-- Channels:
--   - "inapp"  — stored in _notifications (this table); UI fetches via REST
--   - "email"  — sent via core mailer (v1.0); also stored as inapp by default
--   - "push"   — deferred to railbase-push plugin
--
-- Design notes:
--   - kind: opaque string like "payment_approved". Operators define them
--     per their domain; we don't enforce a catalog.
--   - data: JSONB blob; whatever the sender wants the UI/email template
--     to read. Often {entity_id, amount, etc}.
--   - priority: TEXT enum so admin UI can highlight urgent rows.
--   - read_at: NULL when unread; timestamp on mark-read. Indexed for
--     "show me unread for this user" hot path.
--   - expires_at: optional auto-cleanup deadline; the existing v1.4.0
--     jobs framework gets a cleanup_notifications job in v1.5.x.

CREATE TABLE _notifications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Recipient. Foreign key on auth-collection users would be nice
    -- but we don't know the collection name at migration time
    -- (operators set "users" or similar). Soft-key on string UUID.
    user_id         UUID        NOT NULL,
    tenant_id       UUID,

    -- Operator-defined event key. Examples: "payment_approved",
    -- "invite_received", "task_assigned".
    kind            TEXT        NOT NULL,

    -- Display fields. Title + body are rendered at send time so the
    -- UI doesn't need template engines client-side. data is the
    -- structured payload for any per-row UI affordances (deep-link
    -- button, etc).
    title           TEXT        NOT NULL,
    body            TEXT        NOT NULL DEFAULT '',
    data            JSONB       NOT NULL DEFAULT '{}'::jsonb,

    -- Priority drives admin-UI highlight + bypass of quiet hours
    -- (v1.5.x feature, not in this milestone). Default = normal.
    priority        TEXT        NOT NULL DEFAULT 'normal'
                                CHECK (priority IN ('low','normal','high','urgent')),

    -- read_at = NULL means unread.
    read_at         TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Hot path: "fetch unread for user", paginated by created_at DESC.
-- Partial index keeps the index size proportional to UNREAD rows
-- (typically a few percent of total).
CREATE INDEX idx__notifications_unread
    ON _notifications (user_id, created_at DESC)
    WHERE read_at IS NULL;

-- All-rows-for-user query (admin UI "history"): non-partial fallback.
CREATE INDEX idx__notifications_user_created
    ON _notifications (user_id, created_at DESC);

-- Per-user channel opt-in. Missing row = use channel default
-- (channel-default decided in code, currently "enabled").
CREATE TABLE _notification_preferences (
    user_id         UUID        NOT NULL,
    kind            TEXT        NOT NULL,
    channel         TEXT        NOT NULL
                                CHECK (channel IN ('inapp','email','push')),
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, kind, channel)
);

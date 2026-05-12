-- v1.7.34f — Email event log (§3.1.4 plugin-scope deferral, core piece).
--
-- The mailer (internal/mailer) has been able to publish `mailer.after_send`
-- on the eventbus since v1.7.31b, but those events vanish unless something
-- subscribes — and the bus is in-process, so a separate observability tool
-- can't reach them. This migration adds the persistent shadow: one row
-- per (recipient, outcome) so operators can answer "did the password-reset
-- email actually leave?" and "how many bounces did we accrue this week?"
-- without standing up a separate logging pipeline.
--
-- Scope guardrails:
--
--   * `sent`/`failed` ship with the core writer (mailer.EventStore) — every
--     successful or failed Driver.Send call writes ONE row PER RECIPIENT
--     (To + CC + BCC fanned out). This matches the analytical question
--     "did alice@ get their reset email?" — Alice cares about her row, not
--     the message-level aggregate.
--
--   * `bounced`/`opened`/`clicked`/`complained` are accepted by the CHECK
--     constraint but are NEVER written by the core. They land via plugins
--     (different MTAs format bounce notifications differently, and pixel
--     tracking is an opt-in concern). The constraint sits in core so the
--     plugin doesn't need a migration of its own.
--
-- bounce_type is a separate column (not a free-text reason in metadata)
-- so the partial index below can cover `WHERE event='bounced' AND
-- bounce_type='hard'` — the operator query that gates auto-suppression.

CREATE TABLE _email_events (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    occurred_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- Outcome class. CHECK enumerates the full set including plugin-scoped
    -- events so the schema doesn't need to change when a bounce-parser
    -- plugin ships in v1.x.
    event           TEXT         NOT NULL
                                 CHECK (event IN ('sent','failed','bounced','opened','clicked','complained')),

    -- Driver name (mailer.Driver.Name()). 'smtp', 'console', 'ses' (v1.0.1),
    -- 'postmark'/'sendgrid'/etc (plugins).
    driver          TEXT         NOT NULL,

    -- Message-ID header value, when the driver supplies one. Drivers that
    -- don't (console) leave it NULL. Used to correlate bounce-webhook
    -- payloads back to the original send.
    message_id      TEXT,

    -- One row per recipient — see scope notes above. Always populated.
    recipient       TEXT         NOT NULL,
    subject         TEXT,

    -- Template name when sent via SendTemplate; empty/NULL for SendDirect.
    -- Used for "which template bounced most?" reporting.
    template        TEXT,

    -- Plugin-populated. Core writer never sets this.
    bounce_type     TEXT         CHECK (bounce_type IS NULL
                                        OR bounce_type IN ('hard','soft','transient')),

    -- Failure detail. error_code is the SMTP/HTTP response code where
    -- known (e.g. '550', '4.2.2'); error_message is the human string.
    error_code      TEXT,
    error_message   TEXT,

    -- Opaque per-event extras. Reserved for plugins (raw MTA notification
    -- ID, IP of opener, user_agent of clicker, etc.).
    metadata        JSONB
);

-- Time-series scan: "show me the last hour of email activity".
CREATE INDEX _email_events_occurred_at_idx
    ON _email_events (occurred_at DESC);

-- Recipient drill-down: "what happened with alice@ in the last week?".
CREATE INDEX _email_events_recipient_idx
    ON _email_events (recipient, occurred_at DESC);

-- Operator alert scans. Partial index keeps the working set tiny because
-- the happy path (event='sent') dwarfs everything else in a healthy
-- system; the alert query that drives ops dashboards only cares about
-- the failures/bounces/complaints anyway.
CREATE INDEX _email_events_event_idx
    ON _email_events (event)
    WHERE event IN ('failed','bounced','complained');

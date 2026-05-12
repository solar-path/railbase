-- v1.5.0 — Outbound webhooks (§3.9.2 / docs/21).
--
-- Two tables:
--   _webhooks            — operator-configured destinations: where to
--                          send, which events trigger, secret for HMAC.
--   _webhook_deliveries  — one row per delivery attempt: status, HTTP
--                          response code, error if any. Lets operators
--                          inspect failed integrations without scraping
--                          logs. Retries are tracked via `attempt`
--                          across rows that share the same delivery_id
--                          (first attempt's id is reused).
--
-- Design notes:
--   - `events` is JSONB array of strings like "record.created.posts" or
--     "record.*.posts" (glob '*'). Match is server-side in Go; SQL has
--     no role in pattern matching.
--   - `secret_b64` is the raw HMAC key, base64-encoded. NEVER returned
--     by GET endpoints — only set on POST/PATCH. Operators retrieve via
--     CLI / DB only if they explicitly need to share.
--   - `active` toggle lets operators pause a misbehaving integration
--     without losing config.
--   - `headers` is JSONB object {string: string} of custom headers
--     merged with the canonical ones (X-Railbase-Event etc). User
--     headers never override canonical ones — the dispatcher enforces.
--   - Delivery rows are append-only (one per attempt). A
--     `superseded_by` pointer threads a retry chain so admin UI can
--     show "this is attempt 3 of 5" without grouping in app code.

CREATE TABLE _webhooks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Human-readable handle; CLI / admin UI key on this.
    name            TEXT        NOT NULL UNIQUE,

    -- Destination URL. Validated at write time (must be http/https,
    -- public IP unless dev mode).
    url             TEXT        NOT NULL,

    -- Base64-encoded HMAC key. App generates if operator leaves
    -- blank on create.
    secret_b64      TEXT        NOT NULL,

    -- JSONB array of event patterns. Examples:
    --   ["record.created.posts"]
    --   ["record.*.payments"]               — any verb on payments
    --   ["record.*.*"]                      — every record mutation
    events          JSONB       NOT NULL DEFAULT '[]'::jsonb,

    -- Operators flip this to pause a webhook without deleting config.
    active          BOOLEAN     NOT NULL DEFAULT TRUE,

    -- Retry budget. Mirrors _jobs.max_attempts semantics — first
    -- attempt counted, so max_attempts=5 means 1 try + 4 retries.
    max_attempts    INT         NOT NULL DEFAULT 5,

    -- HTTP request timeout in milliseconds. Receiver has this long
    -- to return 2xx; longer = retry.
    timeout_ms      INT         NOT NULL DEFAULT 30000,

    -- Custom headers merged into request (object {k: v}).
    headers         JSONB       NOT NULL DEFAULT '{}'::jsonb,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fast lookup by name from CLI / admin UI.
CREATE INDEX idx__webhooks_active
    ON _webhooks (created_at DESC) WHERE active = TRUE;

CREATE TABLE _webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id      UUID        NOT NULL REFERENCES _webhooks(id) ON DELETE CASCADE,

    -- The original event topic ("record.created.posts").
    event           TEXT        NOT NULL,

    -- Serialised JSON body that was (or will be) POSTed.
    payload         JSONB       NOT NULL,

    -- 1-indexed attempt counter. Each retry inserts a new row with
    -- incremented attempt. Pin the chain via superseded_by.
    attempt         INT         NOT NULL DEFAULT 1,
    superseded_by   UUID,

    -- Lifecycle:
    --   pending — enqueued, not yet sent
    --   success — got 2xx
    --   retry   — sent, got 4xx (retryable) or 5xx / timeout / net err
    --   dead    — gave up (attempt == max_attempts or 4xx non-retryable)
    status          TEXT        NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending','success','retry','dead')),

    response_code   INT,
    response_body   TEXT,        -- truncated to first 1KB to bound storage
    error_msg       TEXT,        -- non-HTTP failures (DNS, timeout, ...)

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

-- Admin UI / CLI: "deliveries for this webhook, newest first".
CREATE INDEX idx__webhook_deliveries_webhook_created
    ON _webhook_deliveries (webhook_id, created_at DESC);

-- Stats query: "failed deliveries in the last hour".
CREATE INDEX idx__webhook_deliveries_status_created
    ON _webhook_deliveries (status, created_at DESC);

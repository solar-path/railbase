-- v2 — Stripe billing integration (subscriptions + one-time sales).
--
-- Six tables, all `_`-prefixed system tables. The catalog half
-- (_stripe_products, _stripe_prices) is the LOCAL source of truth:
-- operators manage products/prices in the Railbase admin UI and the
-- service pushes them up to Stripe, stamping the returned Stripe IDs
-- back here. The mirror half (_stripe_customers, _stripe_subscriptions,
-- _stripe_payments) is the read-side projection: rows are created on
-- demand by the checkout endpoints and kept in sync by the webhook
-- handler. _stripe_events is the webhook idempotency log.
--
-- Stripe credentials (secret/publishable/webhook-signing keys) do NOT
-- live here — they go in `_settings` under the `stripe.*` namespace,
-- same as mailer.* / auth.* config.
--
-- Money is stored the way Stripe represents it: integer minor units
-- (cents) in `amount` / `unit_amount`, never floats.

-- ── catalog: products ────────────────────────────────────────────
-- Locally-authored; pushed to Stripe. stripe_product_id is NULL until
-- the first successful push, then unique.
CREATE TABLE _stripe_products (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_product_id   TEXT UNIQUE,
    name                TEXT        NOT NULL,
    description         TEXT        NOT NULL DEFAULT '',
    -- Mirrors Stripe's `active`. Archiving (active=false) hides a
    -- product from new checkouts without deleting history.
    active              BOOLEAN     NOT NULL DEFAULT TRUE,
    metadata            JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── catalog: prices ──────────────────────────────────────────────
-- A product has one or more prices. `kind` splits one-time sales from
-- subscriptions; the recurring_* columns are NULL for one_time prices.
-- Stripe prices are immutable once created — an edit archives the old
-- price row and creates a new one.
CREATE TABLE _stripe_prices (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id              UUID        NOT NULL REFERENCES _stripe_products(id) ON DELETE CASCADE,
    stripe_price_id         TEXT UNIQUE,
    currency                TEXT        NOT NULL DEFAULT 'usd',
    -- Integer minor units (cents). 1999 = $19.99.
    unit_amount             BIGINT      NOT NULL,
    kind                    TEXT        NOT NULL DEFAULT 'one_time'
                                        CHECK (kind IN ('one_time','recurring')),
    -- Only meaningful when kind = 'recurring'.
    recurring_interval      TEXT        CHECK (recurring_interval IN ('day','week','month','year')),
    recurring_interval_count INT        NOT NULL DEFAULT 1,
    active                  BOOLEAN     NOT NULL DEFAULT TRUE,
    metadata                JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx__stripe_prices_product ON _stripe_prices (product_id);

-- ── mirror: customers ────────────────────────────────────────────
-- Created on demand by the checkout endpoints (one Stripe customer per
-- buyer email) and reconciled by the webhook handler. Not the source
-- of truth — Stripe is.
CREATE TABLE _stripe_customers (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_customer_id  TEXT        NOT NULL UNIQUE,
    email               TEXT        NOT NULL DEFAULT '',
    name                TEXT        NOT NULL DEFAULT '',
    metadata            JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── mirror: subscriptions ────────────────────────────────────────
-- Read-side projection of Stripe subscriptions. status mirrors
-- Stripe's lifecycle verbatim (incomplete / trialing / active /
-- past_due / canceled / unpaid / ...). The webhook handler is the
-- only writer once a row exists.
CREATE TABLE _stripe_subscriptions (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_subscription_id  TEXT        NOT NULL UNIQUE,
    customer_id             UUID        NOT NULL REFERENCES _stripe_customers(id) ON DELETE CASCADE,
    -- The price the subscription is on. NULL-safe: if a price is
    -- archived and removed, the subscription history survives.
    price_id                UUID        REFERENCES _stripe_prices(id) ON DELETE SET NULL,
    status                  TEXT        NOT NULL,
    quantity                INT         NOT NULL DEFAULT 1,
    current_period_start    TIMESTAMPTZ,
    current_period_end      TIMESTAMPTZ,
    cancel_at_period_end    BOOLEAN     NOT NULL DEFAULT FALSE,
    canceled_at             TIMESTAMPTZ,
    metadata                JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx__stripe_subscriptions_customer ON _stripe_subscriptions (customer_id);
CREATE INDEX idx__stripe_subscriptions_status ON _stripe_subscriptions (status);

-- ── mirror: one-time payments ────────────────────────────────────
-- One row per PaymentIntent for a one-time sale. `kind` distinguishes
-- a catalog purchase (a fixed _stripe_prices row) from an ad-hoc
-- charge (caller-specified amount, price_id NULL).
CREATE TABLE _stripe_payments (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_payment_intent_id    TEXT        NOT NULL UNIQUE,
    customer_id                 UUID        REFERENCES _stripe_customers(id) ON DELETE SET NULL,
    price_id                    UUID        REFERENCES _stripe_prices(id) ON DELETE SET NULL,
    kind                        TEXT        NOT NULL DEFAULT 'adhoc'
                                            CHECK (kind IN ('catalog','adhoc')),
    -- Integer minor units, mirrors PaymentIntent.amount.
    amount                      BIGINT      NOT NULL,
    currency                    TEXT        NOT NULL DEFAULT 'usd',
    description                 TEXT        NOT NULL DEFAULT '',
    -- Mirrors Stripe's PaymentIntent.status: requires_payment_method /
    -- requires_confirmation / processing / succeeded / canceled / ...
    status                      TEXT        NOT NULL,
    metadata                    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx__stripe_payments_customer ON _stripe_payments (customer_id);
CREATE INDEX idx__stripe_payments_status ON _stripe_payments (status);

-- ── webhook idempotency log ──────────────────────────────────────
-- Every Stripe event is recorded here keyed by Stripe's event id. The
-- webhook handler INSERTs first (ON CONFLICT DO NOTHING) so a
-- redelivered event is a no-op. `processed` flips true once the local
-- mirror has been updated; `error` captures a failed dispatch so the
-- admin UI can surface stuck events.
CREATE TABLE _stripe_events (
    stripe_event_id     TEXT PRIMARY KEY,
    type                TEXT        NOT NULL,
    processed           BOOLEAN     NOT NULL DEFAULT FALSE,
    processed_at        TIMESTAMPTZ,
    payload             JSONB       NOT NULL,
    error               TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx__stripe_events_type_created ON _stripe_events (type, created_at DESC);
CREATE INDEX idx__stripe_events_unprocessed ON _stripe_events (created_at DESC) WHERE processed = FALSE;

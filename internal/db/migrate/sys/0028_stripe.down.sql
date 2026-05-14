-- Reverses 0028_stripe.up.sql.
--
-- Drops the six Stripe mirror/catalog tables. The `stripe.*` keys in
-- `_settings` are NOT touched here — they're owned by the settings
-- table, not this migration; an operator rolling back is expected to
-- clear them via the admin UI or `railbase config` if desired.
DROP TABLE IF EXISTS _stripe_events;
DROP TABLE IF EXISTS _stripe_payments;
DROP TABLE IF EXISTS _stripe_subscriptions;
DROP TABLE IF EXISTS _stripe_customers;
DROP TABLE IF EXISTS _stripe_prices;
DROP TABLE IF EXISTS _stripe_products;

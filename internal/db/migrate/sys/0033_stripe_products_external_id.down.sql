-- Reverse of 0033_stripe_products_external_id.up.sql.
DROP INDEX IF EXISTS uniq__stripe_products_external_id;
ALTER TABLE _stripe_products DROP COLUMN IF EXISTS external_id;

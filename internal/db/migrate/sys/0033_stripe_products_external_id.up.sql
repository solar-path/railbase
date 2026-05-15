-- v0.4.4 — _stripe_products.external_id + unique index.
--
-- FEEDBACK #23 — embedders that maintain their own `products` table in
-- a downstream collection (shopper-class apps: a `_products`-side row
-- that owns title/description/SKU, plus a Railbase _stripe_products
-- row that owns the pushed-to-Stripe state) need an idempotency key
-- they control. Without it, every UPDATE in their `products` hook
-- duplicates a `_stripe_products` row, because the hand-rolled
-- `INSERT ... ON CONFLICT DO NOTHING` had no constraint to conflict on.
--
-- The right fix is a documented, indexed external_id column on
-- _stripe_products that downstream apps can stamp with their own
-- product key (the shopper-product row's id, an SKU, whatever). The
-- partial unique index allows multiple rows with NULL external_id
-- (the railbase-only path), but enforces uniqueness for stamped rows.
--
-- Embedder usage from a Go hook:
--
--   tx.Exec(ctx, `
--     INSERT INTO _stripe_products (name, external_id, metadata)
--     VALUES ($1, $2, $3)
--     ON CONFLICT (external_id) WHERE external_id IS NOT NULL
--     DO UPDATE SET name = EXCLUDED.name, metadata = EXCLUDED.metadata,
--                   updated_at = now()`,
--     prod.Name, prod.ShopProductID, prod.Metadata)
--
-- This makes the embedder's upsert correctly idempotent across N edits
-- of the same upstream product, instead of accumulating N duplicate
-- rows.

ALTER TABLE _stripe_products
    ADD COLUMN external_id TEXT;

-- Partial unique index: enforces uniqueness only for non-NULL values,
-- so the rows owned exclusively by Railbase's own admin UI (which
-- doesn't set external_id) stay unconstrained.
CREATE UNIQUE INDEX uniq__stripe_products_external_id
    ON _stripe_products (external_id)
    WHERE external_id IS NOT NULL;

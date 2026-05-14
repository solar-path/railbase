// Stripe billing — typed admin endpoint wrappers + wire types.
//
// Self-contained (types live here, not in api/types.ts) because the
// Stripe surface is large and otherwise-unrelated to the core schema
// types. Routes are relative to /api/_admin (the client prefixes it).
//
// The public checkout + webhook endpoints (/api/stripe/*) are NOT
// called from the admin SPA — they're for downstream apps — so they
// have no wrapper here.

import { api } from "./client";

// ── wire types (mirror internal/stripe Go models) ────────────────

export type StripeMode = "test" | "live" | "unset" | "";
export type StripePriceKind = "one_time" | "recurring";
export type StripePaymentKind = "catalog" | "adhoc";

export interface StripeConfigStatus {
  enabled: boolean;
  mode: StripeMode;
  publishable_key: string;
  secret_key_set: boolean;
  secret_key_hint: string;
  webhook_secret_set: boolean;
}

export interface StripeConfigInput {
  secret_key?: string;
  publishable_key?: string;
  webhook_secret?: string;
  enabled?: boolean;
}

export interface StripeProduct {
  id: string;
  stripe_product_id: string;
  name: string;
  description: string;
  active: boolean;
  metadata: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface StripePrice {
  id: string;
  product_id: string;
  stripe_price_id: string;
  currency: string;
  unit_amount: number;
  kind: StripePriceKind;
  recurring_interval?: string;
  recurring_interval_count: number;
  active: boolean;
  metadata: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface StripeCustomer {
  id: string;
  stripe_customer_id: string;
  email: string;
  name: string;
  metadata: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface StripeSubscription {
  id: string;
  stripe_subscription_id: string;
  customer_id: string;
  price_id?: string;
  status: string;
  quantity: number;
  current_period_start?: string;
  current_period_end?: string;
  cancel_at_period_end: boolean;
  canceled_at?: string;
  metadata: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface StripePayment {
  id: string;
  stripe_payment_intent_id: string;
  customer_id?: string;
  price_id?: string;
  kind: StripePaymentKind;
  amount: number;
  currency: string;
  description: string;
  status: string;
  metadata: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface StripeEvent {
  stripe_event_id: string;
  type: string;
  processed: boolean;
  processed_at?: string;
  error?: string;
  created_at: string;
}

export interface StripeProductInput {
  name: string;
  description?: string;
  active?: boolean;
  metadata?: Record<string, string>;
}

export interface StripePriceInput {
  product_id: string;
  currency: string;
  unit_amount: number;
  kind: StripePriceKind;
  recurring_interval?: string;
  recurring_interval_count?: number;
  active?: boolean;
  metadata?: Record<string, string>;
}

// ── client ───────────────────────────────────────────────────────

export const stripeAPI = {
  // config — credential status (redacted) + save (keep-if-empty secrets)
  configGet(): Promise<StripeConfigStatus> {
    return api.request("GET", "/stripe/config");
  },
  configSave(body: StripeConfigInput): Promise<StripeConfigStatus> {
    return api.request("PUT", "/stripe/config", { body });
  },

  // products — local catalog, pushed to Stripe
  productsList(): Promise<{ items: StripeProduct[] }> {
    return api.request("GET", "/stripe/products");
  },
  productCreate(body: StripeProductInput): Promise<{ record: StripeProduct }> {
    return api.request("POST", "/stripe/products", { body });
  },
  productUpdate(id: string, body: StripeProductInput): Promise<{ record: StripeProduct }> {
    return api.request("PATCH", `/stripe/products/${encodeURIComponent(id)}`, { body });
  },
  productDelete(id: string): Promise<{ deleted: boolean }> {
    return api.request("DELETE", `/stripe/products/${encodeURIComponent(id)}`);
  },

  // prices — created under a product, immutable in Stripe (archive only)
  pricesList(): Promise<{ items: StripePrice[] }> {
    return api.request("GET", "/stripe/prices");
  },
  priceCreate(body: StripePriceInput): Promise<{ record: StripePrice }> {
    return api.request("POST", "/stripe/prices", { body });
  },
  priceArchive(id: string): Promise<{ record: StripePrice }> {
    return api.request("POST", `/stripe/prices/${encodeURIComponent(id)}/archive`);
  },
  priceRestore(id: string): Promise<{ record: StripePrice }> {
    return api.request("POST", `/stripe/prices/${encodeURIComponent(id)}/restore`);
  },

  // push-catalog — reconcile any un-pushed catalog rows up to Stripe
  pushCatalog(): Promise<{ products_pushed: number; prices_pushed: number }> {
    return api.request("POST", "/stripe/push-catalog");
  },

  // read-only mirror browsers
  customersList(): Promise<{ items: StripeCustomer[] }> {
    return api.request("GET", "/stripe/customers");
  },
  subscriptionsList(): Promise<{ items: StripeSubscription[] }> {
    return api.request("GET", "/stripe/subscriptions");
  },
  subscriptionCancel(id: string): Promise<{ record: StripeSubscription }> {
    return api.request("POST", `/stripe/subscriptions/${encodeURIComponent(id)}/cancel`);
  },
  paymentsList(): Promise<{ items: StripePayment[] }> {
    return api.request("GET", "/stripe/payments");
  },
  eventsList(): Promise<{ items: StripeEvent[] }> {
    return api.request("GET", "/stripe/events");
  },
};

package ts

import "strings"

// EmitStripe renders stripe.ts: typed wrappers for the public Stripe
// surface (internal/api/stripeapi). Unlike auth.ts / collections this
// emitter is schema-independent — the Stripe endpoints are fixed, not
// derived from CollectionSpec — so EmitStripe takes no arguments.
//
// Surface (the three client-callable endpoints; the webhook is
// server-to-server and deliberately absent from the SDK):
//
//   - GET  /api/stripe/config            → publishable key + mode
//   - POST /api/stripe/payment-intents   → one-time sale (catalog or ad-hoc)
//   - POST /api/stripe/subscriptions     → subscription on a recurring price
//
// The checkout calls return a `client_secret` the caller hands to
// Stripe.js / Elements to confirm the payment — the card never touches
// the Railbase client. Types mirror internal/stripe's JSON tags.
func EmitStripe() string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(`// stripe.ts — typed wrappers for the public Stripe endpoints.

import type { HTTPClient } from "./index.js";

/** GET /api/stripe/config — what a frontend needs to init Stripe.js. */
export interface StripeConfigInfo {
  /** True when Stripe is enabled AND a usable secret key is stored. */
  enabled: boolean;
  /** pk_test_… / pk_live_… — safe to hand to Stripe.js in the browser. */
  publishable_key: string;
  /** "test" | "live" | "unset" — derived from the secret key prefix. */
  mode: string;
}

/** A one-time payment mirror row (subset returned by checkout). */
export interface StripePayment {
  id: string;
  stripe_payment_intent_id: string;
  kind: "catalog" | "adhoc";
  /** Integer minor units (cents). */
  amount: number;
  currency: string;
  description: string;
  status: string;
  created_at: string;
  updated_at: string;
}

/** A subscription mirror row (subset returned by checkout). */
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
  created_at: string;
  updated_at: string;
}

/** Result of a checkout call: the local mirror row + the Elements
 *  client secret to confirm the payment with Stripe.js. */
export interface StripeCheckoutResult {
  payment?: StripePayment;
  subscription?: StripeSubscription;
  /** Pass to stripe.confirmPayment({ clientSecret }) on the frontend. */
  client_secret: string;
  publishable_key: string;
}

/** Input for createPaymentIntent. Supply EITHER price_id (a one-time
 *  catalog price) OR amount+currency (an ad-hoc charge). email/name
 *  seed the Stripe customer; email may be omitted for guest checkout. */
export interface StripePaymentIntentInput {
  /** A one-time catalog price id (uuid). Mutually exclusive with amount. */
  price_id?: string;
  /** Ad-hoc charge amount in integer minor units. Requires currency. */
  amount?: number;
  currency?: string;
  description?: string;
  email?: string;
  name?: string;
}

/** Input for createSubscription. email is required — Stripe
 *  subscriptions need a customer. */
export interface StripeSubscriptionInput {
  /** A recurring catalog price id (uuid). */
  price_id: string;
  quantity?: number;
  email: string;
  name?: string;
}

/** Stripe billing wrappers. Wire to the shared HTTP client:
 *
 *    const rb = createRailbaseClient({ baseURL });
 *    const { publishable_key } = await rb.stripe.config();
 *    const { client_secret } = await rb.stripe.createPaymentIntent({
 *      price_id, email,
 *    });
 *    // → hand client_secret to Stripe.js Elements to confirm.
 *
 * The two checkout calls require an authenticated principal — set the
 * client token via createRailbaseClient({ token }) or setToken().
 */
export function stripeClient(http: HTTPClient) {
  return {
    /** GET /api/stripe/config — publishable key + mode. No auth needed. */
    config(): Promise<StripeConfigInfo> {
      return http.request("GET", "/api/stripe/config");
    },

    /** POST /api/stripe/payment-intents — start a one-time sale.
     *  Returns a client_secret to confirm with Stripe.js Elements. */
    createPaymentIntent(input: StripePaymentIntentInput): Promise<StripeCheckoutResult> {
      return http.request("POST", "/api/stripe/payment-intents", { body: input });
    },

    /** POST /api/stripe/subscriptions — start a subscription on a
     *  recurring catalog price. Returns a client_secret to confirm the
     *  first invoice's payment with Stripe.js Elements. */
    createSubscription(input: StripeSubscriptionInput): Promise<StripeCheckoutResult> {
      return http.request("POST", "/api/stripe/subscriptions", { body: input });
    },
  };
}
`)
	return b.String()
}

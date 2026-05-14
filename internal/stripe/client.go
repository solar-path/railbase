package stripe

import (
	"errors"

	stripesdk "github.com/stripe/stripe-go/v82"
	stripeclient "github.com/stripe/stripe-go/v82/client"
	stripewebhook "github.com/stripe/stripe-go/v82/webhook"
)

// ErrNotConfigured is returned by every Client call when Stripe is
// disabled or has no usable secret key. Callers surface it as a 503 /
// "Stripe not configured" rather than a hard error.
var ErrNotConfigured = errors.New("stripe: not configured")

// Client is a thin wrapper over the stripe-go SDK, constructed from the
// DB-stored Config. It is the ONLY file that touches the SDK directly —
// service.go and the handlers stay SDK-agnostic.
type Client struct {
	api *stripeclient.API
	cfg Config
}

// NewClient builds a Client from cfg. When cfg is not Ready (disabled,
// or no test/live secret key) the returned Client is inert: every
// method returns ErrNotConfigured. Rebuild the Client whenever the
// config changes — it caches nothing beyond the SDK handle.
func NewClient(cfg Config) *Client {
	c := &Client{cfg: cfg}
	if cfg.Ready() {
		c.api = stripeclient.New(cfg.SecretKey, nil)
	}
	return c
}

// Ready reports whether live Stripe calls can be made.
func (c *Client) Ready() bool { return c != nil && c.api != nil }

// ── catalog: products & prices ───────────────────────────────────

// CreateProduct creates a Stripe product.
func (c *Client) CreateProduct(name, description string, active bool, metadata map[string]string) (*stripesdk.Product, error) {
	if !c.Ready() {
		return nil, ErrNotConfigured
	}
	params := &stripesdk.ProductParams{
		Name:     stripesdk.String(name),
		Active:   stripesdk.Bool(active),
		Metadata: metadata,
	}
	if description != "" {
		params.Description = stripesdk.String(description)
	}
	return c.api.Products.New(params)
}

// UpdateProduct rewrites a Stripe product's mutable fields.
func (c *Client) UpdateProduct(stripeID, name, description string, active bool, metadata map[string]string) (*stripesdk.Product, error) {
	if !c.Ready() {
		return nil, ErrNotConfigured
	}
	params := &stripesdk.ProductParams{
		Name:     stripesdk.String(name),
		Active:   stripesdk.Bool(active),
		Metadata: metadata,
	}
	// Stripe rejects an empty-string description; clear it via a space
	// is wrong too, so we only set it when non-empty.
	if description != "" {
		params.Description = stripesdk.String(description)
	}
	return c.api.Products.Update(stripeID, params)
}

// CreatePrice creates a Stripe price under productStripeID. A non-empty
// recurringInterval ("day"/"week"/"month"/"year") makes it a recurring
// price; empty makes it a one-time price.
func (c *Client) CreatePrice(productStripeID, currency string, unitAmount int64, recurringInterval string, intervalCount int, metadata map[string]string) (*stripesdk.Price, error) {
	if !c.Ready() {
		return nil, ErrNotConfigured
	}
	params := &stripesdk.PriceParams{
		Product:    stripesdk.String(productStripeID),
		Currency:   stripesdk.String(currency),
		UnitAmount: stripesdk.Int64(unitAmount),
		Metadata:   metadata,
	}
	if recurringInterval != "" {
		if intervalCount <= 0 {
			intervalCount = 1
		}
		params.Recurring = &stripesdk.PriceRecurringParams{
			Interval:      stripesdk.String(recurringInterval),
			IntervalCount: stripesdk.Int64(int64(intervalCount)),
		}
	}
	return c.api.Prices.New(params)
}

// ArchivePrice flips a Stripe price to active=false. Stripe prices are
// otherwise immutable — an "edit" is archive-old + create-new.
func (c *Client) ArchivePrice(stripeID string, active bool) (*stripesdk.Price, error) {
	if !c.Ready() {
		return nil, ErrNotConfigured
	}
	return c.api.Prices.Update(stripeID, &stripesdk.PriceParams{Active: stripesdk.Bool(active)})
}

// ── customers ────────────────────────────────────────────────────

// CreateCustomer creates a Stripe customer.
func (c *Client) CreateCustomer(email, name string, metadata map[string]string) (*stripesdk.Customer, error) {
	if !c.Ready() {
		return nil, ErrNotConfigured
	}
	params := &stripesdk.CustomerParams{Metadata: metadata}
	if email != "" {
		params.Email = stripesdk.String(email)
	}
	if name != "" {
		params.Name = stripesdk.String(name)
	}
	return c.api.Customers.New(params)
}

// ── one-time payments ────────────────────────────────────────────

// CreatePaymentIntent creates a PaymentIntent for a one-time sale with
// automatic payment methods enabled — the frontend confirms it with
// Stripe Elements using the returned ClientSecret. customerID may be
// empty for a guest checkout.
func (c *Client) CreatePaymentIntent(amount int64, currency, customerID, description string, metadata map[string]string) (*stripesdk.PaymentIntent, error) {
	if !c.Ready() {
		return nil, ErrNotConfigured
	}
	params := &stripesdk.PaymentIntentParams{
		Amount:   stripesdk.Int64(amount),
		Currency: stripesdk.String(currency),
		Metadata: metadata,
		AutomaticPaymentMethods: &stripesdk.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripesdk.Bool(true),
		},
	}
	if customerID != "" {
		params.Customer = stripesdk.String(customerID)
	}
	if description != "" {
		params.Description = stripesdk.String(description)
	}
	return c.api.PaymentIntents.New(params)
}

// ── subscriptions ────────────────────────────────────────────────

// CreateSubscription creates a subscription in `default_incomplete`
// state and expands the latest invoice's confirmation secret. The
// frontend confirms the first payment with Elements using
// sub.LatestInvoice.ConfirmationSecret.ClientSecret; Stripe then drives
// the subscription to `active` (or `past_due`) and the webhook handler
// projects the result into _stripe_subscriptions.
func (c *Client) CreateSubscription(customerID, priceStripeID string, quantity int64, metadata map[string]string) (*stripesdk.Subscription, error) {
	if !c.Ready() {
		return nil, ErrNotConfigured
	}
	if quantity <= 0 {
		quantity = 1
	}
	params := &stripesdk.SubscriptionParams{
		Customer: stripesdk.String(customerID),
		Items: []*stripesdk.SubscriptionItemsParams{
			{Price: stripesdk.String(priceStripeID), Quantity: stripesdk.Int64(quantity)},
		},
		PaymentBehavior: stripesdk.String("default_incomplete"),
		PaymentSettings: &stripesdk.SubscriptionPaymentSettingsParams{
			SaveDefaultPaymentMethod: stripesdk.String("on_subscription"),
		},
		Metadata: metadata,
	}
	params.AddExpand("latest_invoice.confirmation_secret")
	return c.api.Subscriptions.New(params)
}

// CancelSubscription cancels a subscription immediately.
func (c *Client) CancelSubscription(stripeID string) (*stripesdk.Subscription, error) {
	if !c.Ready() {
		return nil, ErrNotConfigured
	}
	return c.api.Subscriptions.Cancel(stripeID, &stripesdk.SubscriptionCancelParams{})
}

// ── webhooks ─────────────────────────────────────────────────────

// ConstructWebhookEvent verifies the Stripe-Signature header against
// the configured webhook signing secret and returns the parsed event.
// Returns ErrNotConfigured when no webhook secret is set.
func (c *Client) ConstructWebhookEvent(payload []byte, sigHeader string) (stripesdk.Event, error) {
	if c == nil || c.cfg.WebhookSecret == "" {
		return stripesdk.Event{}, ErrNotConfigured
	}
	return stripewebhook.ConstructEvent(payload, sigHeader, c.cfg.WebhookSecret)
}

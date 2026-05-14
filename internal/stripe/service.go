package stripe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	stripesdk "github.com/stripe/stripe-go/v82"

	"github.com/railbase/railbase/internal/settings"
)

// Service is the business layer: it ties the Store (local mirror +
// catalog), the SDK Client (built fresh from DB config per call so it
// always reflects the latest settings), and the settings.Manager
// together. Handlers — admin and public — talk only to the Service.
type Service struct {
	store    *Store
	settings *settings.Manager
	log      *slog.Logger
}

// NewService constructs a Service. Hold for process lifetime.
func NewService(store *Store, sm *settings.Manager, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, settings: sm, log: log}
}

// Store exposes the persistence layer for read-only list endpoints.
func (svc *Service) Store() *Store { return svc.store }

// LoadConfig returns the current DB-stored Stripe config.
func (svc *Service) LoadConfig(ctx context.Context) (Config, error) {
	return LoadConfig(ctx, svc.settings)
}

// SaveConfig persists the Stripe config.
func (svc *Service) SaveConfig(ctx context.Context, c Config) error {
	return SaveConfig(ctx, svc.settings, c)
}

// client builds a fresh SDK Client from the current DB config. Cheap
// (no network), and guarantees every call sees the latest credentials.
func (svc *Service) client(ctx context.Context) (*Client, Config, error) {
	cfg, err := svc.LoadConfig(ctx)
	if err != nil {
		return nil, Config{}, err
	}
	return NewClient(cfg), cfg, nil
}

// ── products ─────────────────────────────────────────────────────

// CreateProduct creates a local product row and, when Stripe is
// configured, pushes it up and stamps the returned Stripe id back.
//
// The push is best-effort: the local catalog is the source of truth,
// so a push failure (bad key, Stripe outage) is logged but NOT fatal —
// the row persists in the "not pushed" state, visible as such in the
// admin UI, and the explicit Push-catalog action reconciles it later.
// Failing the request here would orphan the just-written local row.
func (svc *Service) CreateProduct(ctx context.Context, name, description string, active bool, metadata map[string]string) (*Product, error) {
	p, err := svc.store.CreateProduct(ctx, name, description, active, metadata)
	if err != nil {
		return nil, err
	}
	cl, _, err := svc.client(ctx)
	if err != nil {
		return nil, err
	}
	if cl.Ready() {
		sp, perr := cl.CreateProduct(p.Name, p.Description, p.Active, p.Metadata)
		if perr != nil {
			svc.log.Warn("stripe: product push failed (kept local, unpushed)", "product", p.ID, "err", perr)
			return p, nil
		}
		if err := svc.store.SetProductStripeID(ctx, p.ID, sp.ID); err != nil {
			return nil, err
		}
		p.StripeProductID = sp.ID
	}
	return p, nil
}

// UpdateProduct rewrites a local product and mirrors the change to
// Stripe when the product has already been pushed.
func (svc *Service) UpdateProduct(ctx context.Context, id uuid.UUID, name, description string, active bool, metadata map[string]string) (*Product, error) {
	if err := svc.store.UpdateProduct(ctx, id, name, description, active, metadata); err != nil {
		return nil, err
	}
	p, err := svc.store.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.StripeProductID != "" {
		cl, _, err := svc.client(ctx)
		if err != nil {
			return nil, err
		}
		if cl.Ready() {
			if _, perr := cl.UpdateProduct(p.StripeProductID, p.Name, p.Description, p.Active, p.Metadata); perr != nil {
				// Best-effort mirror: the local edit landed; a failed push
				// leaves Stripe stale but recoverable via Push-catalog.
				svc.log.Warn("stripe: product update push failed", "product", p.ID, "err", perr)
			}
		}
	}
	return p, nil
}

// DeleteProduct removes a local product (and its prices). The Stripe
// product is NOT deleted — Stripe forbids deleting products with
// price history; operators archive instead (active=false).
func (svc *Service) DeleteProduct(ctx context.Context, id uuid.UUID) (bool, error) {
	return svc.store.DeleteProduct(ctx, id)
}

// ListProducts returns the local catalog.
func (svc *Service) ListProducts(ctx context.Context) ([]*Product, error) {
	return svc.store.ListProducts(ctx)
}

// ── prices ───────────────────────────────────────────────────────

// CreatePrice creates a local price and pushes it to Stripe when the
// parent product is already pushed and Stripe is configured.
func (svc *Service) CreatePrice(ctx context.Context, in Price) (*Price, error) {
	prod, err := svc.store.GetProduct(ctx, in.ProductID)
	if err != nil {
		return nil, fmt.Errorf("stripe: price parent product: %w", err)
	}
	p, err := svc.store.CreatePrice(ctx, in)
	if err != nil {
		return nil, err
	}
	if prod.StripeProductID != "" {
		cl, _, err := svc.client(ctx)
		if err != nil {
			return nil, err
		}
		if cl.Ready() {
			interval := ""
			if p.Kind == KindRecurring {
				interval = p.RecurringInterval
			}
			sp, perr := cl.CreatePrice(prod.StripeProductID, p.Currency, p.UnitAmount, interval, p.RecurringIntervalCount, p.Metadata)
			if perr != nil {
				// Best-effort: keep the local price unpushed; Push-catalog
				// reconciles it once the parent product is pushed / the
				// credentials are fixed.
				svc.log.Warn("stripe: price push failed (kept local, unpushed)", "price", p.ID, "err", perr)
				return p, nil
			}
			if err := svc.store.SetPriceStripeID(ctx, p.ID, sp.ID); err != nil {
				return nil, err
			}
			p.StripePriceID = sp.ID
		}
	}
	return p, nil
}

// SetPriceActive archives/unarchives a price locally and in Stripe.
func (svc *Service) SetPriceActive(ctx context.Context, id uuid.UUID, active bool) (*Price, error) {
	if err := svc.store.SetPriceActive(ctx, id, active); err != nil {
		return nil, err
	}
	p, err := svc.store.GetPrice(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.StripePriceID != "" {
		cl, _, err := svc.client(ctx)
		if err != nil {
			return nil, err
		}
		if cl.Ready() {
			if _, perr := cl.ArchivePrice(p.StripePriceID, active); perr != nil {
				svc.log.Warn("stripe: price archive push failed", "price", p.ID, "active", active, "err", perr)
			}
		}
	}
	return p, nil
}

// ListPrices returns every local price.
func (svc *Service) ListPrices(ctx context.Context) ([]*Price, error) {
	return svc.store.ListPrices(ctx)
}

// PushCatalog reconciles the local catalog upward: any product/price
// created while Stripe was disabled gets pushed and stamped. Idempotent
// — already-pushed rows are skipped. Returns the count pushed.
func (svc *Service) PushCatalog(ctx context.Context) (products, prices int, err error) {
	cl, _, err := svc.client(ctx)
	if err != nil {
		return 0, 0, err
	}
	if !cl.Ready() {
		return 0, 0, ErrNotConfigured
	}
	prods, err := svc.store.ListProducts(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, p := range prods {
		if p.StripeProductID != "" {
			continue
		}
		sp, err := cl.CreateProduct(p.Name, p.Description, p.Active, p.Metadata)
		if err != nil {
			return products, prices, fmt.Errorf("stripe: push product %s: %w", p.ID, err)
		}
		if err := svc.store.SetProductStripeID(ctx, p.ID, sp.ID); err != nil {
			return products, prices, err
		}
		p.StripeProductID = sp.ID
		products++
	}
	allPrices, err := svc.store.ListPrices(ctx)
	if err != nil {
		return products, prices, err
	}
	for _, pr := range allPrices {
		if pr.StripePriceID != "" {
			continue
		}
		prod, err := svc.store.GetProduct(ctx, pr.ProductID)
		if err != nil || prod.StripeProductID == "" {
			continue // parent not pushed — skip, retry next run
		}
		interval := ""
		if pr.Kind == KindRecurring {
			interval = pr.RecurringInterval
		}
		sp, err := cl.CreatePrice(prod.StripeProductID, pr.Currency, pr.UnitAmount, interval, pr.RecurringIntervalCount, pr.Metadata)
		if err != nil {
			return products, prices, fmt.Errorf("stripe: push price %s: %w", pr.ID, err)
		}
		if err := svc.store.SetPriceStripeID(ctx, pr.ID, sp.ID); err != nil {
			return products, prices, err
		}
		prices++
	}
	return products, prices, nil
}

// ── customers ────────────────────────────────────────────────────

// ListCustomers returns every mirrored customer.
func (svc *Service) ListCustomers(ctx context.Context) ([]*Customer, error) {
	return svc.store.ListCustomers(ctx)
}

// ensureCustomer resolves a local customer for an email, creating a
// Stripe customer + local mirror row on first sight. email may be
// empty for guest one-time checkout, in which case (nil, nil) is
// returned and the caller proceeds customer-less.
func (svc *Service) ensureCustomer(ctx context.Context, cl *Client, email, name string) (*Customer, error) {
	if email == "" {
		return nil, nil
	}
	existing, err := svc.store.GetCustomerByEmail(ctx, email)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	sc, err := cl.CreateCustomer(email, name, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe: create customer: %w", err)
	}
	return svc.store.UpsertCustomer(ctx, sc.ID, email, name, nil)
}

// ── one-time payments ────────────────────────────────────────────

// CheckoutResult is what the public checkout endpoints return to the
// frontend: the local row plus the Elements client secret.
type CheckoutResult struct {
	Payment        *Payment      `json:"payment,omitempty"`
	Subscription   *Subscription `json:"subscription,omitempty"`
	ClientSecret   string        `json:"client_secret"`
	PublishableKey string        `json:"publishable_key"`
}

// CreateCatalogPayment starts a one-time purchase of a fixed catalog
// price. email/name are optional (guest checkout). Returns the local
// payment row + the client secret the frontend confirms with Elements.
func (svc *Service) CreateCatalogPayment(ctx context.Context, priceID uuid.UUID, email, name string) (*CheckoutResult, error) {
	cl, cfg, err := svc.client(ctx)
	if err != nil {
		return nil, err
	}
	if !cl.Ready() {
		return nil, ErrNotConfigured
	}
	price, err := svc.store.GetPrice(ctx, priceID)
	if err != nil {
		return nil, fmt.Errorf("stripe: catalog price: %w", err)
	}
	if price.Kind != KindOneTime {
		return nil, fmt.Errorf("stripe: price %s is not a one-time price", priceID)
	}
	if !price.Active {
		return nil, fmt.Errorf("stripe: price %s is archived", priceID)
	}
	cust, err := svc.ensureCustomer(ctx, cl, email, name)
	if err != nil {
		return nil, err
	}
	var custStripeID string
	var custLocalID *uuid.UUID
	if cust != nil {
		custStripeID = cust.StripeCustomerID
		custLocalID = &cust.ID
	}
	prod, _ := svc.store.GetProduct(ctx, price.ProductID)
	desc := ""
	if prod != nil {
		desc = prod.Name
	}
	pi, err := cl.CreatePaymentIntent(price.UnitAmount, price.Currency, custStripeID, desc, map[string]string{
		"railbase_price_id": price.ID.String(),
		"railbase_kind":     PaymentCatalog,
	})
	if err != nil {
		return nil, fmt.Errorf("stripe: create payment intent: %w", err)
	}
	pid := price.ID
	payment, err := svc.store.UpsertPayment(ctx, Payment{
		StripePaymentIntentID: pi.ID,
		CustomerID:            custLocalID,
		PriceID:               &pid,
		Kind:                  PaymentCatalog,
		Amount:                price.UnitAmount,
		Currency:              price.Currency,
		Description:           desc,
		Status:                string(pi.Status),
	})
	if err != nil {
		return nil, err
	}
	return &CheckoutResult{Payment: payment, ClientSecret: pi.ClientSecret, PublishableKey: cfg.PublishableKey}, nil
}

// CreateAdhocPayment starts a one-time charge for a caller-specified
// amount (minor units) and description — invoices, custom orders, etc.
// — not tied to a catalog price.
func (svc *Service) CreateAdhocPayment(ctx context.Context, amount int64, currency, description, email, name string) (*CheckoutResult, error) {
	cl, cfg, err := svc.client(ctx)
	if err != nil {
		return nil, err
	}
	if !cl.Ready() {
		return nil, ErrNotConfigured
	}
	if amount <= 0 {
		return nil, fmt.Errorf("stripe: amount must be positive")
	}
	if currency == "" {
		currency = "usd"
	}
	cust, err := svc.ensureCustomer(ctx, cl, email, name)
	if err != nil {
		return nil, err
	}
	var custStripeID string
	var custLocalID *uuid.UUID
	if cust != nil {
		custStripeID = cust.StripeCustomerID
		custLocalID = &cust.ID
	}
	pi, err := cl.CreatePaymentIntent(amount, currency, custStripeID, description, map[string]string{
		"railbase_kind": PaymentAdhoc,
	})
	if err != nil {
		return nil, fmt.Errorf("stripe: create payment intent: %w", err)
	}
	payment, err := svc.store.UpsertPayment(ctx, Payment{
		StripePaymentIntentID: pi.ID,
		CustomerID:            custLocalID,
		Kind:                  PaymentAdhoc,
		Amount:                amount,
		Currency:              currency,
		Description:           description,
		Status:                string(pi.Status),
	})
	if err != nil {
		return nil, err
	}
	return &CheckoutResult{Payment: payment, ClientSecret: pi.ClientSecret, PublishableKey: cfg.PublishableKey}, nil
}

// ListPayments returns every one-time payment.
func (svc *Service) ListPayments(ctx context.Context) ([]*Payment, error) {
	return svc.store.ListPayments(ctx)
}

// ── subscriptions ────────────────────────────────────────────────

// CreateSubscription starts a subscription on a recurring catalog
// price. email is REQUIRED — Stripe subscriptions need a customer.
// Returns the local mirror row + the client secret for confirming the
// first invoice's payment with Elements.
func (svc *Service) CreateSubscription(ctx context.Context, priceID uuid.UUID, email, name string, quantity int) (*CheckoutResult, error) {
	cl, cfg, err := svc.client(ctx)
	if err != nil {
		return nil, err
	}
	if !cl.Ready() {
		return nil, ErrNotConfigured
	}
	if email == "" {
		return nil, fmt.Errorf("stripe: subscription requires a customer email")
	}
	price, err := svc.store.GetPrice(ctx, priceID)
	if err != nil {
		return nil, fmt.Errorf("stripe: subscription price: %w", err)
	}
	if price.Kind != KindRecurring {
		return nil, fmt.Errorf("stripe: price %s is not a recurring price", priceID)
	}
	if !price.Active {
		return nil, fmt.Errorf("stripe: price %s is archived", priceID)
	}
	if price.StripePriceID == "" {
		return nil, fmt.Errorf("stripe: price %s has not been pushed to Stripe yet", priceID)
	}
	cust, err := svc.ensureCustomer(ctx, cl, email, name)
	if err != nil {
		return nil, err
	}
	sub, err := cl.CreateSubscription(cust.StripeCustomerID, price.StripePriceID, int64(quantity), map[string]string{
		"railbase_price_id": price.ID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("stripe: create subscription: %w", err)
	}
	local, err := svc.localSubFromStripe(ctx, sub)
	if err != nil {
		return nil, err
	}
	saved, err := svc.store.UpsertSubscription(ctx, local)
	if err != nil {
		return nil, err
	}
	clientSecret := ""
	if sub.LatestInvoice != nil && sub.LatestInvoice.ConfirmationSecret != nil {
		clientSecret = sub.LatestInvoice.ConfirmationSecret.ClientSecret
	}
	return &CheckoutResult{Subscription: saved, ClientSecret: clientSecret, PublishableKey: cfg.PublishableKey}, nil
}

// CancelSubscription cancels a subscription immediately, both in Stripe
// and in the local mirror.
func (svc *Service) CancelSubscription(ctx context.Context, id uuid.UUID) (*Subscription, error) {
	cl, _, err := svc.client(ctx)
	if err != nil {
		return nil, err
	}
	if !cl.Ready() {
		return nil, ErrNotConfigured
	}
	subs, err := svc.store.ListSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	var target *Subscription
	for _, s := range subs {
		if s.ID == id {
			target = s
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("stripe: subscription %s not found", id)
	}
	sub, err := cl.CancelSubscription(target.StripeSubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("stripe: cancel subscription: %w", err)
	}
	local, err := svc.localSubFromStripe(ctx, sub)
	if err != nil {
		return nil, err
	}
	return svc.store.UpsertSubscription(ctx, local)
}

// ListSubscriptions returns every mirrored subscription.
func (svc *Service) ListSubscriptions(ctx context.Context) ([]*Subscription, error) {
	return svc.store.ListSubscriptions(ctx)
}

// localSubFromStripe projects a stripe-go Subscription into the local
// model, resolving (and creating if needed) the mirror customer and
// resolving the catalog price by Stripe id.
func (svc *Service) localSubFromStripe(ctx context.Context, sub *stripesdk.Subscription) (Subscription, error) {
	if sub == nil || sub.Customer == nil {
		return Subscription{}, fmt.Errorf("stripe: subscription has no customer")
	}
	cust, err := svc.store.GetCustomerByStripeID(ctx, sub.Customer.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Webhook-delivered subscriptions can reference a customer we
		// haven't mirrored yet; create a minimal row from whatever the
		// payload carries.
		cust, err = svc.store.UpsertCustomer(ctx, sub.Customer.ID, sub.Customer.Email, sub.Customer.Name, nil)
	}
	if err != nil {
		return Subscription{}, err
	}

	out := Subscription{
		StripeSubscriptionID: sub.ID,
		CustomerID:           cust.ID,
		Status:               string(sub.Status),
		Quantity:             1,
		CancelAtPeriodEnd:    sub.CancelAtPeriodEnd,
		CanceledAt:           unixPtr(sub.CanceledAt),
		Metadata:             sub.Metadata,
	}
	if sub.Items != nil && len(sub.Items.Data) > 0 {
		item := sub.Items.Data[0]
		if item.Quantity > 0 {
			out.Quantity = int(item.Quantity)
		}
		out.CurrentPeriodStart = unixPtr(item.CurrentPeriodStart)
		out.CurrentPeriodEnd = unixPtr(item.CurrentPeriodEnd)
		if item.Price != nil && item.Price.ID != "" {
			if lp, perr := svc.store.GetPriceByStripeID(ctx, item.Price.ID); perr == nil {
				out.PriceID = &lp.ID
			}
		}
	}
	return out, nil
}

// unixPtr converts a Stripe unix timestamp (0 = unset) to *time.Time.
func unixPtr(ts int64) *time.Time {
	if ts == 0 {
		return nil
	}
	t := time.Unix(ts, 0).UTC()
	return &t
}

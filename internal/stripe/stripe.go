package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the minimal pgx surface the Store needs. Both
// *pgxpool.Pool and pgx.Tx satisfy it.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Price kinds.
const (
	KindOneTime   = "one_time"
	KindRecurring = "recurring"
)

// Payment kinds — a catalog purchase (fixed price row) vs an ad-hoc
// caller-specified charge.
const (
	PaymentCatalog = "catalog"
	PaymentAdhoc   = "adhoc"
)

// ── models ───────────────────────────────────────────────────────

// Product is a locally-authored catalog entry. StripeProductID is
// empty until the first successful push to Stripe.
type Product struct {
	ID              uuid.UUID         `json:"id"`
	StripeProductID string            `json:"stripe_product_id"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Active          bool              `json:"active"`
	Metadata        map[string]string `json:"metadata"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// Price is one price point on a Product. RecurringInterval is empty
// for one-time prices.
type Price struct {
	ID                     uuid.UUID         `json:"id"`
	ProductID              uuid.UUID         `json:"product_id"`
	StripePriceID          string            `json:"stripe_price_id"`
	Currency               string            `json:"currency"`
	UnitAmount             int64             `json:"unit_amount"`
	Kind                   string            `json:"kind"`
	RecurringInterval      string            `json:"recurring_interval,omitempty"`
	RecurringIntervalCount int               `json:"recurring_interval_count"`
	Active                 bool              `json:"active"`
	Metadata               map[string]string `json:"metadata"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

// Customer mirrors a Stripe customer.
type Customer struct {
	ID               uuid.UUID         `json:"id"`
	StripeCustomerID string            `json:"stripe_customer_id"`
	Email            string            `json:"email"`
	Name             string            `json:"name"`
	Metadata         map[string]string `json:"metadata"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

// Subscription mirrors a Stripe subscription. Status is Stripe's
// lifecycle string verbatim.
type Subscription struct {
	ID                   uuid.UUID         `json:"id"`
	StripeSubscriptionID string            `json:"stripe_subscription_id"`
	CustomerID           uuid.UUID         `json:"customer_id"`
	PriceID              *uuid.UUID        `json:"price_id,omitempty"`
	Status               string            `json:"status"`
	Quantity             int               `json:"quantity"`
	CurrentPeriodStart   *time.Time        `json:"current_period_start,omitempty"`
	CurrentPeriodEnd     *time.Time        `json:"current_period_end,omitempty"`
	CancelAtPeriodEnd    bool              `json:"cancel_at_period_end"`
	CanceledAt           *time.Time        `json:"canceled_at,omitempty"`
	Metadata             map[string]string `json:"metadata"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
}

// Payment mirrors a Stripe PaymentIntent for a one-time sale.
type Payment struct {
	ID                    uuid.UUID         `json:"id"`
	StripePaymentIntentID string            `json:"stripe_payment_intent_id"`
	CustomerID            *uuid.UUID        `json:"customer_id,omitempty"`
	PriceID               *uuid.UUID        `json:"price_id,omitempty"`
	Kind                  string            `json:"kind"`
	Amount                int64             `json:"amount"`
	Currency              string            `json:"currency"`
	Description           string            `json:"description"`
	Status                string            `json:"status"`
	Metadata              map[string]string `json:"metadata"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

// Event is one row of the webhook idempotency log.
type Event struct {
	StripeEventID string          `json:"stripe_event_id"`
	Type          string          `json:"type"`
	Processed     bool            `json:"processed"`
	ProcessedAt   *time.Time      `json:"processed_at,omitempty"`
	Payload       json.RawMessage `json:"-"`
	Error         string          `json:"error,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// Store is the persistence layer over the six _stripe_* tables.
type Store struct {
	q Querier
}

// NewStore wraps a Querier. Hold for process lifetime.
func NewStore(q Querier) *Store { return &Store{q: q} }

// ── products ─────────────────────────────────────────────────────

// CreateProduct inserts a local product with no Stripe ID yet. Call
// SetProductStripeID after the push succeeds.
func (s *Store) CreateProduct(ctx context.Context, name, description string, active bool, metadata map[string]string) (*Product, error) {
	if name == "" {
		return nil, fmt.Errorf("stripe: product name required")
	}
	p := &Product{
		ID:          uuid.Must(uuid.NewV7()),
		Name:        name,
		Description: description,
		Active:      active,
		Metadata:    orEmptyMap(metadata),
	}
	meta, err := json.Marshal(p.Metadata)
	if err != nil {
		return nil, fmt.Errorf("stripe: marshal metadata: %w", err)
	}
	err = s.q.QueryRow(ctx, `
		INSERT INTO _stripe_products (id, name, description, active, metadata)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at, updated_at`,
		p.ID, p.Name, p.Description, p.Active, meta,
	).Scan(&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("stripe: insert product: %w", err)
	}
	return p, nil
}

// UpdateProduct rewrites the mutable product fields.
func (s *Store) UpdateProduct(ctx context.Context, id uuid.UUID, name, description string, active bool, metadata map[string]string) error {
	meta, err := json.Marshal(orEmptyMap(metadata))
	if err != nil {
		return fmt.Errorf("stripe: marshal metadata: %w", err)
	}
	_, err = s.q.Exec(ctx, `
		UPDATE _stripe_products
		SET name = $1, description = $2, active = $3, metadata = $4, updated_at = now()
		WHERE id = $5`,
		name, description, active, meta, id,
	)
	if err != nil {
		return fmt.Errorf("stripe: update product: %w", err)
	}
	return nil
}

// SetProductStripeID stamps the Stripe product id back onto the local
// row after a successful push.
func (s *Store) SetProductStripeID(ctx context.Context, id uuid.UUID, stripeID string) error {
	_, err := s.q.Exec(ctx, `
		UPDATE _stripe_products SET stripe_product_id = $1, updated_at = now() WHERE id = $2`,
		stripeID, id,
	)
	if err != nil {
		return fmt.Errorf("stripe: set product stripe id: %w", err)
	}
	return nil
}

// GetProduct loads one product by local uuid.
func (s *Store) GetProduct(ctx context.Context, id uuid.UUID) (*Product, error) {
	return scanProduct(s.q.QueryRow(ctx, productSelect+` WHERE id = $1`, id))
}

// GetProductByStripeID loads one product by its Stripe id.
func (s *Store) GetProductByStripeID(ctx context.Context, stripeID string) (*Product, error) {
	return scanProduct(s.q.QueryRow(ctx, productSelect+` WHERE stripe_product_id = $1`, stripeID))
}

// ListProducts returns every product, newest first.
func (s *Store) ListProducts(ctx context.Context) ([]*Product, error) {
	rows, err := s.q.Query(ctx, productSelect+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("stripe: list products: %w", err)
	}
	defer rows.Close()
	var out []*Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProduct removes a local product row (cascades to its prices).
// Returns false if no row matched.
func (s *Store) DeleteProduct(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM _stripe_products WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("stripe: delete product: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

const productSelect = `
	SELECT id, COALESCE(stripe_product_id, ''), name, description, active, metadata, created_at, updated_at
	FROM _stripe_products`

func scanProduct(row interface{ Scan(...any) error }) (*Product, error) {
	p := &Product{}
	var meta []byte
	if err := row.Scan(&p.ID, &p.StripeProductID, &p.Name, &p.Description, &p.Active, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalMeta(meta, &p.Metadata); err != nil {
		return nil, err
	}
	return p, nil
}

// ── prices ───────────────────────────────────────────────────────

// CreatePrice inserts a local price with no Stripe ID yet.
func (s *Store) CreatePrice(ctx context.Context, in Price) (*Price, error) {
	if in.ProductID == uuid.Nil {
		return nil, fmt.Errorf("stripe: price product_id required")
	}
	if in.Kind != KindOneTime && in.Kind != KindRecurring {
		return nil, fmt.Errorf("stripe: price kind must be one_time or recurring")
	}
	if in.Currency == "" {
		in.Currency = "usd"
	}
	if in.RecurringIntervalCount <= 0 {
		in.RecurringIntervalCount = 1
	}
	p := in
	p.ID = uuid.Must(uuid.NewV7())
	p.Metadata = orEmptyMap(in.Metadata)
	meta, err := json.Marshal(p.Metadata)
	if err != nil {
		return nil, fmt.Errorf("stripe: marshal metadata: %w", err)
	}
	var interval *string
	if p.Kind == KindRecurring && p.RecurringInterval != "" {
		interval = &p.RecurringInterval
	}
	err = s.q.QueryRow(ctx, `
		INSERT INTO _stripe_prices
			(id, product_id, currency, unit_amount, kind, recurring_interval, recurring_interval_count, active, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at, updated_at`,
		p.ID, p.ProductID, p.Currency, p.UnitAmount, p.Kind, interval, p.RecurringIntervalCount, p.Active, meta,
	).Scan(&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("stripe: insert price: %w", err)
	}
	return &p, nil
}

// SetPriceStripeID stamps the Stripe price id back after a push.
func (s *Store) SetPriceStripeID(ctx context.Context, id uuid.UUID, stripeID string) error {
	_, err := s.q.Exec(ctx, `
		UPDATE _stripe_prices SET stripe_price_id = $1, updated_at = now() WHERE id = $2`,
		stripeID, id,
	)
	if err != nil {
		return fmt.Errorf("stripe: set price stripe id: %w", err)
	}
	return nil
}

// SetPriceActive archives/unarchives a price. Stripe prices are
// immutable apart from their active flag.
func (s *Store) SetPriceActive(ctx context.Context, id uuid.UUID, active bool) error {
	_, err := s.q.Exec(ctx, `
		UPDATE _stripe_prices SET active = $1, updated_at = now() WHERE id = $2`,
		active, id,
	)
	if err != nil {
		return fmt.Errorf("stripe: set price active: %w", err)
	}
	return nil
}

// GetPrice loads one price by local uuid.
func (s *Store) GetPrice(ctx context.Context, id uuid.UUID) (*Price, error) {
	return scanPrice(s.q.QueryRow(ctx, priceSelect+` WHERE id = $1`, id))
}

// GetPriceByStripeID loads one price by its Stripe id.
func (s *Store) GetPriceByStripeID(ctx context.Context, stripeID string) (*Price, error) {
	return scanPrice(s.q.QueryRow(ctx, priceSelect+` WHERE stripe_price_id = $1`, stripeID))
}

// ListPrices returns every price, newest first.
func (s *Store) ListPrices(ctx context.Context) ([]*Price, error) {
	return s.queryPrices(ctx, priceSelect+` ORDER BY created_at DESC`)
}

// ListPricesByProduct returns the prices of one product.
func (s *Store) ListPricesByProduct(ctx context.Context, productID uuid.UUID) ([]*Price, error) {
	return s.queryPrices(ctx, priceSelect+` WHERE product_id = $1 ORDER BY created_at DESC`, productID)
}

func (s *Store) queryPrices(ctx context.Context, sql string, args ...any) ([]*Price, error) {
	rows, err := s.q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("stripe: list prices: %w", err)
	}
	defer rows.Close()
	var out []*Price
	for rows.Next() {
		p, err := scanPrice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const priceSelect = `
	SELECT id, product_id, COALESCE(stripe_price_id, ''), currency, unit_amount, kind,
	       COALESCE(recurring_interval, ''), recurring_interval_count, active, metadata, created_at, updated_at
	FROM _stripe_prices`

func scanPrice(row interface{ Scan(...any) error }) (*Price, error) {
	p := &Price{}
	var meta []byte
	if err := row.Scan(&p.ID, &p.ProductID, &p.StripePriceID, &p.Currency, &p.UnitAmount, &p.Kind,
		&p.RecurringInterval, &p.RecurringIntervalCount, &p.Active, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalMeta(meta, &p.Metadata); err != nil {
		return nil, err
	}
	return p, nil
}

// ── customers ────────────────────────────────────────────────────

// UpsertCustomer inserts-or-updates a customer keyed by Stripe id.
// Used both by the checkout path (after creating a Stripe customer)
// and the webhook handler (customer.updated).
func (s *Store) UpsertCustomer(ctx context.Context, stripeID, email, name string, metadata map[string]string) (*Customer, error) {
	if stripeID == "" {
		return nil, fmt.Errorf("stripe: customer stripe id required")
	}
	meta, err := json.Marshal(orEmptyMap(metadata))
	if err != nil {
		return nil, fmt.Errorf("stripe: marshal metadata: %w", err)
	}
	c := &Customer{}
	var rawMeta []byte
	err = s.q.QueryRow(ctx, `
		INSERT INTO _stripe_customers (id, stripe_customer_id, email, name, metadata)
		VALUES (gen_random_uuid(), $1, $2, $3, $4)
		ON CONFLICT (stripe_customer_id) DO UPDATE
			SET email = EXCLUDED.email, name = EXCLUDED.name,
			    metadata = EXCLUDED.metadata, updated_at = now()
		RETURNING id, stripe_customer_id, email, name, metadata, created_at, updated_at`,
		stripeID, email, name, meta,
	).Scan(&c.ID, &c.StripeCustomerID, &c.Email, &c.Name, &rawMeta, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("stripe: upsert customer: %w", err)
	}
	if err := unmarshalMeta(rawMeta, &c.Metadata); err != nil {
		return nil, err
	}
	return c, nil
}

// GetCustomerByStripeID loads one customer by Stripe id.
func (s *Store) GetCustomerByStripeID(ctx context.Context, stripeID string) (*Customer, error) {
	return scanCustomer(s.q.QueryRow(ctx, customerSelect+` WHERE stripe_customer_id = $1`, stripeID))
}

// GetCustomerByEmail loads the most recent customer for an email, or
// pgx.ErrNoRows if none exists.
func (s *Store) GetCustomerByEmail(ctx context.Context, email string) (*Customer, error) {
	return scanCustomer(s.q.QueryRow(ctx, customerSelect+` WHERE email = $1 ORDER BY created_at DESC LIMIT 1`, email))
}

// ListCustomers returns every customer, newest first.
func (s *Store) ListCustomers(ctx context.Context) ([]*Customer, error) {
	rows, err := s.q.Query(ctx, customerSelect+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("stripe: list customers: %w", err)
	}
	defer rows.Close()
	var out []*Customer
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const customerSelect = `
	SELECT id, stripe_customer_id, email, name, metadata, created_at, updated_at
	FROM _stripe_customers`

func scanCustomer(row interface{ Scan(...any) error }) (*Customer, error) {
	c := &Customer{}
	var meta []byte
	if err := row.Scan(&c.ID, &c.StripeCustomerID, &c.Email, &c.Name, &meta, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalMeta(meta, &c.Metadata); err != nil {
		return nil, err
	}
	return c, nil
}

// ── subscriptions ────────────────────────────────────────────────

// UpsertSubscription inserts-or-updates a subscription keyed by Stripe
// id. The webhook handler is the primary caller.
func (s *Store) UpsertSubscription(ctx context.Context, sub Subscription) (*Subscription, error) {
	if sub.StripeSubscriptionID == "" {
		return nil, fmt.Errorf("stripe: subscription stripe id required")
	}
	if sub.CustomerID == uuid.Nil {
		return nil, fmt.Errorf("stripe: subscription customer_id required")
	}
	if sub.Quantity <= 0 {
		sub.Quantity = 1
	}
	meta, err := json.Marshal(orEmptyMap(sub.Metadata))
	if err != nil {
		return nil, fmt.Errorf("stripe: marshal metadata: %w", err)
	}
	out := &Subscription{}
	var rawMeta []byte
	err = s.q.QueryRow(ctx, `
		INSERT INTO _stripe_subscriptions
			(id, stripe_subscription_id, customer_id, price_id, status, quantity,
			 current_period_start, current_period_end, cancel_at_period_end, canceled_at, metadata)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (stripe_subscription_id) DO UPDATE SET
			price_id = EXCLUDED.price_id,
			status = EXCLUDED.status,
			quantity = EXCLUDED.quantity,
			current_period_start = EXCLUDED.current_period_start,
			current_period_end = EXCLUDED.current_period_end,
			cancel_at_period_end = EXCLUDED.cancel_at_period_end,
			canceled_at = EXCLUDED.canceled_at,
			metadata = EXCLUDED.metadata,
			updated_at = now()
		RETURNING `+subscriptionCols,
		sub.StripeSubscriptionID, sub.CustomerID, sub.PriceID, sub.Status, sub.Quantity,
		sub.CurrentPeriodStart, sub.CurrentPeriodEnd, sub.CancelAtPeriodEnd, sub.CanceledAt, meta,
	).Scan(scanSubscriptionDest(out, &rawMeta)...)
	if err != nil {
		return nil, fmt.Errorf("stripe: upsert subscription: %w", err)
	}
	if err := unmarshalMeta(rawMeta, &out.Metadata); err != nil {
		return nil, err
	}
	return out, nil
}

// GetSubscriptionByStripeID loads one subscription by Stripe id.
func (s *Store) GetSubscriptionByStripeID(ctx context.Context, stripeID string) (*Subscription, error) {
	out := &Subscription{}
	var rawMeta []byte
	err := s.q.QueryRow(ctx, `SELECT `+subscriptionCols+` FROM _stripe_subscriptions WHERE stripe_subscription_id = $1`, stripeID).
		Scan(scanSubscriptionDest(out, &rawMeta)...)
	if err != nil {
		return nil, err
	}
	if err := unmarshalMeta(rawMeta, &out.Metadata); err != nil {
		return nil, err
	}
	return out, nil
}

// ListSubscriptions returns every subscription, newest first.
func (s *Store) ListSubscriptions(ctx context.Context) ([]*Subscription, error) {
	rows, err := s.q.Query(ctx, `SELECT `+subscriptionCols+` FROM _stripe_subscriptions ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("stripe: list subscriptions: %w", err)
	}
	defer rows.Close()
	var out []*Subscription
	for rows.Next() {
		sub := &Subscription{}
		var rawMeta []byte
		if err := rows.Scan(scanSubscriptionDest(sub, &rawMeta)...); err != nil {
			return nil, err
		}
		if err := unmarshalMeta(rawMeta, &sub.Metadata); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

const subscriptionCols = `id, stripe_subscription_id, customer_id, price_id, status, quantity,
	current_period_start, current_period_end, cancel_at_period_end, canceled_at, metadata, created_at, updated_at`

func scanSubscriptionDest(sub *Subscription, rawMeta *[]byte) []any {
	return []any{
		&sub.ID, &sub.StripeSubscriptionID, &sub.CustomerID, &sub.PriceID, &sub.Status, &sub.Quantity,
		&sub.CurrentPeriodStart, &sub.CurrentPeriodEnd, &sub.CancelAtPeriodEnd, &sub.CanceledAt,
		rawMeta, &sub.CreatedAt, &sub.UpdatedAt,
	}
}

// ── payments ─────────────────────────────────────────────────────

// UpsertPayment inserts-or-updates a one-time payment keyed by
// PaymentIntent id. Called by the checkout path (on create) and the
// webhook handler (on status transitions).
func (s *Store) UpsertPayment(ctx context.Context, p Payment) (*Payment, error) {
	if p.StripePaymentIntentID == "" {
		return nil, fmt.Errorf("stripe: payment intent id required")
	}
	if p.Kind != PaymentCatalog && p.Kind != PaymentAdhoc {
		return nil, fmt.Errorf("stripe: payment kind must be catalog or adhoc")
	}
	if p.Currency == "" {
		p.Currency = "usd"
	}
	meta, err := json.Marshal(orEmptyMap(p.Metadata))
	if err != nil {
		return nil, fmt.Errorf("stripe: marshal metadata: %w", err)
	}
	out := &Payment{}
	var rawMeta []byte
	err = s.q.QueryRow(ctx, `
		INSERT INTO _stripe_payments
			(id, stripe_payment_intent_id, customer_id, price_id, kind, amount, currency, description, status, metadata)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (stripe_payment_intent_id) DO UPDATE SET
			customer_id = EXCLUDED.customer_id,
			status = EXCLUDED.status,
			amount = EXCLUDED.amount,
			description = EXCLUDED.description,
			metadata = EXCLUDED.metadata,
			updated_at = now()
		RETURNING `+paymentCols,
		p.StripePaymentIntentID, p.CustomerID, p.PriceID, p.Kind, p.Amount, p.Currency, p.Description, p.Status, meta,
	).Scan(scanPaymentDest(out, &rawMeta)...)
	if err != nil {
		return nil, fmt.Errorf("stripe: upsert payment: %w", err)
	}
	if err := unmarshalMeta(rawMeta, &out.Metadata); err != nil {
		return nil, err
	}
	return out, nil
}

// GetPaymentByStripeID loads one payment by PaymentIntent id.
func (s *Store) GetPaymentByStripeID(ctx context.Context, stripeID string) (*Payment, error) {
	out := &Payment{}
	var rawMeta []byte
	err := s.q.QueryRow(ctx, `SELECT `+paymentCols+` FROM _stripe_payments WHERE stripe_payment_intent_id = $1`, stripeID).
		Scan(scanPaymentDest(out, &rawMeta)...)
	if err != nil {
		return nil, err
	}
	if err := unmarshalMeta(rawMeta, &out.Metadata); err != nil {
		return nil, err
	}
	return out, nil
}

// ListPayments returns every one-time payment, newest first.
func (s *Store) ListPayments(ctx context.Context) ([]*Payment, error) {
	rows, err := s.q.Query(ctx, `SELECT `+paymentCols+` FROM _stripe_payments ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("stripe: list payments: %w", err)
	}
	defer rows.Close()
	var out []*Payment
	for rows.Next() {
		p := &Payment{}
		var rawMeta []byte
		if err := rows.Scan(scanPaymentDest(p, &rawMeta)...); err != nil {
			return nil, err
		}
		if err := unmarshalMeta(rawMeta, &p.Metadata); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const paymentCols = `id, stripe_payment_intent_id, customer_id, price_id, kind, amount, currency,
	description, status, metadata, created_at, updated_at`

func scanPaymentDest(p *Payment, rawMeta *[]byte) []any {
	return []any{
		&p.ID, &p.StripePaymentIntentID, &p.CustomerID, &p.PriceID, &p.Kind, &p.Amount, &p.Currency,
		&p.Description, &p.Status, rawMeta, &p.CreatedAt, &p.UpdatedAt,
	}
}

// ── events ───────────────────────────────────────────────────────

// InsertEvent records a Stripe event in the idempotency log. Returns
// (false, nil) when the event id was already present — the caller
// should then treat the delivery as a duplicate and skip processing.
func (s *Store) InsertEvent(ctx context.Context, id, eventType string, payload []byte) (bool, error) {
	tag, err := s.q.Exec(ctx, `
		INSERT INTO _stripe_events (stripe_event_id, type, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT (stripe_event_id) DO NOTHING`,
		id, eventType, payload,
	)
	if err != nil {
		return false, fmt.Errorf("stripe: insert event: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkEventProcessed flips an event to processed. A non-empty errMsg
// records a failed dispatch (processed stays false so the admin UI can
// surface it as stuck).
func (s *Store) MarkEventProcessed(ctx context.Context, id, errMsg string) error {
	if errMsg != "" {
		_, err := s.q.Exec(ctx, `UPDATE _stripe_events SET error = $1 WHERE stripe_event_id = $2`, errMsg, id)
		if err != nil {
			return fmt.Errorf("stripe: mark event error: %w", err)
		}
		return nil
	}
	_, err := s.q.Exec(ctx, `
		UPDATE _stripe_events SET processed = TRUE, processed_at = now(), error = '' WHERE stripe_event_id = $1`, id)
	if err != nil {
		return fmt.Errorf("stripe: mark event processed: %w", err)
	}
	return nil
}

// ListEvents returns the most recent webhook events.
func (s *Store) ListEvents(ctx context.Context, limit int) ([]*Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.Query(ctx, `
		SELECT stripe_event_id, type, processed, processed_at, error, created_at
		FROM _stripe_events ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("stripe: list events: %w", err)
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		e := &Event{}
		if err := rows.Scan(&e.StripeEventID, &e.Type, &e.Processed, &e.ProcessedAt, &e.Error, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── helpers ──────────────────────────────────────────────────────

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func unmarshalMeta(raw []byte, dst *map[string]string) error {
	if len(raw) == 0 || string(raw) == "null" {
		*dst = map[string]string{}
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("stripe: unmarshal metadata: %w", err)
	}
	if *dst == nil {
		*dst = map[string]string{}
	}
	return nil
}

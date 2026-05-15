package adminapi

// v2 — admin endpoints for the Stripe billing integration
// (internal/stripe). Companion to the public checkout + webhook
// surface in internal/api/stripeapi.
//
// Routes (all under /api/_admin, gated by RequireAdmin upstream):
//
//	GET    /stripe/config                  redacted credential status
//	PUT    /stripe/config                  save credentials (keep-if-empty secrets)
//	GET    /stripe/products                list local catalog
//	POST   /stripe/products                create + push to Stripe
//	PATCH  /stripe/products/{id}            update + mirror to Stripe
//	DELETE /stripe/products/{id}            delete local row
//	GET    /stripe/prices                  list local prices
//	POST   /stripe/prices                  create + push to Stripe
//	POST   /stripe/prices/{id}/archive      active=false (local + Stripe)
//	POST   /stripe/prices/{id}/restore      active=true  (local + Stripe)
//	POST   /stripe/push-catalog             reconcile un-pushed catalog upward
//	GET    /stripe/customers               list mirrored customers
//	GET    /stripe/subscriptions           list mirrored subscriptions
//	POST   /stripe/subscriptions/{id}/cancel  cancel immediately
//	GET    /stripe/payments                list one-time payments
//	GET    /stripe/events                  recent webhook events
//
// Credential redaction: GET /stripe/config never returns the secret or
// webhook-signing key — only "<key>_set" booleans plus a short hint
// (prefix…suffix). PUT treats an empty/omitted secret field as
// "keep current" so re-saving the form doesn't wipe stored keys.
// Same display-once discipline as the mailer config.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/stripe"
)

// mountStripe registers the Stripe admin surface when the Service is
// wired. Nil d.Stripe (bare test Deps) skips every route — clean 404
// instead of a nil-deref.
func (d *Deps) mountStripe(r chi.Router) {
	if d.Stripe == nil {
		return
	}
	r.Get("/stripe/config", d.stripeConfigGetHandler)
	r.Put("/stripe/config", d.stripeConfigPutHandler)

	r.Get("/stripe/products", d.stripeProductsListHandler)
	r.Post("/stripe/products", d.stripeProductsCreateHandler)
	r.Patch("/stripe/products/{id}", d.stripeProductsUpdateHandler)
	r.Delete("/stripe/products/{id}", d.stripeProductsDeleteHandler)

	r.Get("/stripe/prices", d.stripePricesListHandler)
	r.Post("/stripe/prices", d.stripePricesCreateHandler)
	r.Post("/stripe/prices/{id}/archive", d.stripePriceArchiveHandler)
	r.Post("/stripe/prices/{id}/restore", d.stripePriceRestoreHandler)
	r.Post("/stripe/push-catalog", d.stripePushCatalogHandler)

	r.Get("/stripe/customers", d.stripeCustomersListHandler)
	r.Get("/stripe/subscriptions", d.stripeSubscriptionsListHandler)
	r.Post("/stripe/subscriptions/{id}/cancel", d.stripeSubscriptionCancelHandler)
	r.Get("/stripe/payments", d.stripePaymentsListHandler)
	r.Get("/stripe/events", d.stripeEventsListHandler)
}

// ── config ───────────────────────────────────────────────────────

type stripeConfigStatus struct {
	Enabled          bool   `json:"enabled"`
	Mode             string `json:"mode"` // test | live | unset
	PublishableKey   string `json:"publishable_key"`
	SecretKeySet     bool   `json:"secret_key_set"`
	SecretKeyHint    string `json:"secret_key_hint"`
	WebhookSecretSet bool   `json:"webhook_secret_set"`
	// Warnings is a list of structured "fix me" messages the admin
	// UI renders as a banner above the Stripe settings page.
	// FEEDBACK #15 — without this, an operator with `stripe.enabled=true`
	// and an empty `stripe.webhook_secret` had no visible cue that
	// webhook delivery was rejecting events with 503.
	Warnings []stripeWarning `json:"warnings,omitempty"`
}

// stripeWarning is a single actionable issue with the Stripe config.
// Stable code so the SPA can map it to a localised string.
type stripeWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// computeStripeWarnings derives the warning list from the current
// config. Pure function so a test can hit every branch without
// touching the database.
func computeStripeWarnings(cfg stripeConfigStatus) []stripeWarning {
	var out []stripeWarning
	if cfg.Enabled && !cfg.WebhookSecretSet {
		out = append(out, stripeWarning{
			Code: "webhook_secret_missing",
			Message: "Stripe is enabled but no webhook signing secret is configured — " +
				"incoming /api/stripe/webhook calls are rejected with 503. " +
				"Run `stripe listen --forward-to localhost:8095/api/stripe/webhook` " +
				"and paste the printed `whsec_…` value into the field below.",
		})
	}
	if cfg.Enabled && !cfg.SecretKeySet {
		out = append(out, stripeWarning{
			Code:    "secret_key_missing",
			Message: "Stripe is enabled but no API secret key is configured — outbound calls will fail.",
		})
	}
	return out
}

// keyHint redacts a secret to "<first 11>…<last 4>" — enough for an
// operator to recognise which key is stored without revealing it.
func keyHint(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 15 {
		return "…"
	}
	return s[:11] + "…" + s[len(s)-4:]
}

func (d *Deps) stripeConfigGetHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	cfg, err := d.Stripe.LoadConfig(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load stripe config"))
		return
	}
	status := stripeConfigStatus{
		Enabled:          cfg.Enabled,
		Mode:             string(cfg.Mode()),
		PublishableKey:   cfg.PublishableKey,
		SecretKeySet:     cfg.SecretKey != "",
		SecretKeyHint:    keyHint(cfg.SecretKey),
		WebhookSecretSet: cfg.WebhookSecret != "",
	}
	status.Warnings = computeStripeWarnings(status)
	writeJSON(w, http.StatusOK, status)
}

type stripeConfigRequest struct {
	SecretKey      *string `json:"secret_key"`
	PublishableKey *string `json:"publishable_key"`
	WebhookSecret  *string `json:"webhook_secret"`
	Enabled        *bool   `json:"enabled"`
}

func (d *Deps) stripeConfigPutHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	var req stripeConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	cur, err := d.Stripe.LoadConfig(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load stripe config"))
		return
	}
	// Keep-if-empty for the two secrets: a present-but-blank field
	// means "leave the stored value alone". Publishable key + enabled
	// flag are not secret, so a present field is applied verbatim.
	if req.SecretKey != nil && strings.TrimSpace(*req.SecretKey) != "" {
		cur.SecretKey = strings.TrimSpace(*req.SecretKey)
	}
	if req.WebhookSecret != nil && strings.TrimSpace(*req.WebhookSecret) != "" {
		cur.WebhookSecret = strings.TrimSpace(*req.WebhookSecret)
	}
	if req.PublishableKey != nil {
		cur.PublishableKey = strings.TrimSpace(*req.PublishableKey)
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if err := d.Stripe.SaveConfig(r.Context(), cur); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "save stripe config"))
		return
	}
	status := stripeConfigStatus{
		Enabled:          cur.Enabled,
		Mode:             string(cur.Mode()),
		PublishableKey:   cur.PublishableKey,
		SecretKeySet:     cur.SecretKey != "",
		SecretKeyHint:    keyHint(cur.SecretKey),
		WebhookSecretSet: cur.WebhookSecret != "",
	}
	status.Warnings = computeStripeWarnings(status)
	writeJSON(w, http.StatusOK, status)
}

// ── products ─────────────────────────────────────────────────────

func (d *Deps) stripeProductsListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	rows, err := d.Stripe.ListProducts(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list products"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": orEmptyProducts(rows)})
}

type stripeProductRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Active      *bool             `json:"active"`
	Metadata    map[string]string `json:"metadata"`
}

func (d *Deps) stripeProductsCreateHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	var req stripeProductRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name is required"))
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	p, err := d.Stripe.CreateProduct(r.Context(), strings.TrimSpace(req.Name), req.Description, active, req.Metadata)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create product"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"record": p})
}

func (d *Deps) stripeProductsUpdateHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	id, ok := parseStripeID(w, r)
	if !ok {
		return
	}
	var req stripeProductRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name is required"))
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	p, err := d.Stripe.UpdateProduct(r.Context(), id, strings.TrimSpace(req.Name), req.Description, active, req.Metadata)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "update product"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": p})
}

func (d *Deps) stripeProductsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	id, ok := parseStripeID(w, r)
	if !ok {
		return
	}
	deleted, err := d.Stripe.DeleteProduct(r.Context(), id)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete product"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// ── prices ───────────────────────────────────────────────────────

func (d *Deps) stripePricesListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	rows, err := d.Stripe.ListPrices(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list prices"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": orEmptyPrices(rows)})
}

type stripePriceRequest struct {
	ProductID              string            `json:"product_id"`
	Currency               string            `json:"currency"`
	UnitAmount             int64             `json:"unit_amount"`
	Kind                   string            `json:"kind"`
	RecurringInterval      string            `json:"recurring_interval"`
	RecurringIntervalCount int               `json:"recurring_interval_count"`
	Active                 *bool             `json:"active"`
	Metadata               map[string]string `json:"metadata"`
}

func (d *Deps) stripePricesCreateHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	var req stripePriceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	productID, err := uuid.Parse(strings.TrimSpace(req.ProductID))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "product_id must be a valid UUID"))
		return
	}
	if req.UnitAmount <= 0 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "unit_amount must be positive (minor units)"))
		return
	}
	if req.Kind != stripe.KindOneTime && req.Kind != stripe.KindRecurring {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "kind must be 'one_time' or 'recurring'"))
		return
	}
	if req.Kind == stripe.KindRecurring {
		switch req.RecurringInterval {
		case "day", "week", "month", "year":
		default:
			rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "recurring_interval must be day|week|month|year"))
			return
		}
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	p, err := d.Stripe.CreatePrice(r.Context(), stripe.Price{
		ProductID:              productID,
		Currency:               strings.TrimSpace(req.Currency),
		UnitAmount:             req.UnitAmount,
		Kind:                   req.Kind,
		RecurringInterval:      req.RecurringInterval,
		RecurringIntervalCount: req.RecurringIntervalCount,
		Active:                 active,
		Metadata:               req.Metadata,
	})
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create price"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"record": p})
}

func (d *Deps) stripePriceArchiveHandler(w http.ResponseWriter, r *http.Request) {
	d.stripePriceSetActive(w, r, false)
}

func (d *Deps) stripePriceRestoreHandler(w http.ResponseWriter, r *http.Request) {
	d.stripePriceSetActive(w, r, true)
}

func (d *Deps) stripePriceSetActive(w http.ResponseWriter, r *http.Request, active bool) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	id, ok := parseStripeID(w, r)
	if !ok {
		return
	}
	p, err := d.Stripe.SetPriceActive(r.Context(), id, active)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "set price active"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": p})
}

func (d *Deps) stripePushCatalogHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	products, prices, err := d.Stripe.PushCatalog(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "push catalog"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"products_pushed": products,
		"prices_pushed":   prices,
	})
}

// ── customers / subscriptions / payments / events ────────────────

func (d *Deps) stripeCustomersListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	rows, err := d.Stripe.ListCustomers(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list customers"))
		return
	}
	if rows == nil {
		rows = []*stripe.Customer{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (d *Deps) stripeSubscriptionsListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	rows, err := d.Stripe.ListSubscriptions(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list subscriptions"))
		return
	}
	if rows == nil {
		rows = []*stripe.Subscription{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (d *Deps) stripeSubscriptionCancelHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	id, ok := parseStripeID(w, r)
	if !ok {
		return
	}
	sub, err := d.Stripe.CancelSubscription(r.Context(), id)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "cancel subscription"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": sub})
}

func (d *Deps) stripePaymentsListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	rows, err := d.Stripe.ListPayments(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list payments"))
		return
	}
	if rows == nil {
		rows = []*stripe.Payment{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (d *Deps) stripeEventsListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Stripe == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	limit := parseIntParam(r, "limit", 100)
	rows, err := d.Stripe.Store().ListEvents(r.Context(), limit)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list events"))
		return
	}
	if rows == nil {
		rows = []*stripe.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// ── helpers ──────────────────────────────────────────────────────

// parseStripeID is the shared {id} URL-param parser for the Stripe
// admin routes. Writes a typed 400 on malformed input.
func parseStripeID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "id must be a valid UUID"))
		return uuid.Nil, false
	}
	return id, true
}

func orEmptyProducts(rows []*stripe.Product) []*stripe.Product {
	if rows == nil {
		return []*stripe.Product{}
	}
	return rows
}

func orEmptyPrices(rows []*stripe.Price) []*stripe.Price {
	if rows == nil {
		return []*stripe.Price{}
	}
	return rows
}

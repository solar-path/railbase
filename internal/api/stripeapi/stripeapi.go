// Package stripeapi is the public / app-facing half of the Stripe
// billing integration. Mount under /api/stripe.
//
// Endpoints:
//
//	POST /api/stripe/webhook           Stripe → us; signature-verified, NO auth
//	GET  /api/stripe/config            publishable key + mode (public — pk is public)
//	POST /api/stripe/payment-intents   start a one-time sale (catalog or ad-hoc)
//	POST /api/stripe/subscriptions     start a subscription
//
// The webhook + config endpoints are intentionally unauthenticated:
// Stripe can't carry a Railbase token, and the publishable key is
// designed to ship to browsers. The two checkout endpoints DO require
// an authenticated principal — they create real Stripe charges, so
// they must not be open to anonymous callers (card-testing abuse).
//
// The admin half (credential config, catalog management, read-only
// browsers) lives in internal/api/adminapi/stripe.go.
package stripeapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/stripe"
)

// maxWebhookBody caps the webhook request body. Stripe events are well
// under this; the limit just stops a malicious caller streaming
// gigabytes at the unauthenticated endpoint.
const maxWebhookBody = 1 << 20 // 1 MiB

// Deps wires the handlers to the Stripe service + logger.
type Deps struct {
	Service *stripe.Service
	Log     *slog.Logger
}

// Mount registers the public Stripe endpoints. When d or d.Service is
// nil every route is skipped — a deployment with Stripe un-wired gets
// a clean 404 instead of a nil-deref. Caller installs auth middleware
// on the router before this group (the checkout handlers read the
// principal from context; webhook/config ignore it).
func Mount(r chi.Router, d *Deps) {
	if d == nil || d.Service == nil {
		return
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	r.Route("/api/stripe", func(r chi.Router) {
		r.Post("/webhook", d.webhook)
		r.Get("/config", d.config)
		r.Post("/payment-intents", d.createPaymentIntent)
		r.Post("/subscriptions", d.createSubscription)
	})
}

// webhook — POST /api/stripe/webhook. Stripe calls this; the body is
// verified against the configured signing secret inside the service.
// A verification failure answers 400; everything else (including
// dispatch failures, which are recorded on the event row) answers 200
// so Stripe doesn't retry a non-transient projection bug forever.
func (d *Deps) webhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeValidation, "read webhook body"))
		return
	}
	sig := r.Header.Get("Stripe-Signature")
	if err := d.Service.HandleWebhook(r.Context(), payload, sig); err != nil {
		if errors.Is(err, stripe.ErrNotConfigured) {
			writeErr(w, rerr.New(rerr.CodeUnavailable, "stripe webhooks not configured"))
			return
		}
		d.Log.Warn("stripe: webhook rejected", "err", err)
		writeErr(w, rerr.Wrap(err, rerr.CodeValidation, "webhook verification failed"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

// config — GET /api/stripe/config. Returns the publishable key + mode
// so a frontend can initialise Stripe.js / Elements. No secrets here.
func (d *Deps) config(w http.ResponseWriter, r *http.Request) {
	cfg, err := d.Service.LoadConfig(r.Context())
	if err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeInternal, "load stripe config"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":         cfg.Ready(),
		"publishable_key": cfg.PublishableKey,
		"mode":            string(cfg.Mode()),
	})
}

// paymentIntentRequest accepts either a catalog purchase (price_id set)
// or an ad-hoc charge (amount + currency set). email/name seed the
// Stripe customer; email may be empty for guest one-time checkout.
type paymentIntentRequest struct {
	PriceID     string `json:"price_id"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Description string `json:"description"`
	Email       string `json:"email"`
	Name        string `json:"name"`
}

// createPaymentIntent — POST /api/stripe/payment-intents. Starts a
// one-time sale and returns the Elements client secret.
func (d *Deps) createPaymentIntent(w http.ResponseWriter, r *http.Request) {
	if !authmw.PrincipalFrom(r.Context()).Authenticated() {
		writeErr(w, rerr.New(rerr.CodeUnauthorized, "auth required"))
		return
	}
	var req paymentIntentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	var (
		res *stripe.CheckoutResult
		err error
	)
	if strings.TrimSpace(req.PriceID) != "" {
		priceID, perr := uuid.Parse(strings.TrimSpace(req.PriceID))
		if perr != nil {
			writeErr(w, rerr.New(rerr.CodeValidation, "price_id must be a valid UUID"))
			return
		}
		res, err = d.Service.CreateCatalogPayment(r.Context(), priceID, req.Email, req.Name)
	} else {
		res, err = d.Service.CreateAdhocPayment(r.Context(), req.Amount, req.Currency, req.Description, req.Email, req.Name)
	}
	if err != nil {
		d.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// subscriptionRequest starts a subscription on a recurring catalog
// price. email is required — Stripe subscriptions need a customer.
type subscriptionRequest struct {
	PriceID  string `json:"price_id"`
	Quantity int    `json:"quantity"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

// createSubscription — POST /api/stripe/subscriptions. Starts a
// subscription and returns the Elements client secret for confirming
// the first invoice's payment.
func (d *Deps) createSubscription(w http.ResponseWriter, r *http.Request) {
	if !authmw.PrincipalFrom(r.Context()).Authenticated() {
		writeErr(w, rerr.New(rerr.CodeUnauthorized, "auth required"))
		return
	}
	var req subscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	priceID, err := uuid.Parse(strings.TrimSpace(req.PriceID))
	if err != nil {
		writeErr(w, rerr.New(rerr.CodeValidation, "price_id must be a valid UUID"))
		return
	}
	res, err := d.Service.CreateSubscription(r.Context(), priceID, req.Email, req.Name, req.Quantity)
	if err != nil {
		d.writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// writeServiceErr maps a Service-layer error onto the right HTTP
// envelope: ErrNotConfigured → 503, everything else → 422 (the caller
// asked for something the current state can't satisfy: bad price,
// archived price, missing email, …).
func (d *Deps) writeServiceErr(w http.ResponseWriter, err error) {
	if errors.Is(err, stripe.ErrNotConfigured) {
		writeErr(w, rerr.New(rerr.CodeUnavailable, "stripe not configured"))
		return
	}
	d.Log.Warn("stripe: checkout failed", "err", err)
	writeErr(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
}

// ── local response helpers ───────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, e *rerr.Error) {
	rerr.WriteJSON(w, e)
}

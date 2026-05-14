package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	stripesdk "github.com/stripe/stripe-go/v82"
)

// HandleWebhook verifies a Stripe webhook delivery against the
// configured signing secret, records it in the _stripe_events
// idempotency log, projects it onto the mirror tables, and marks it
// processed. A redelivered event (same Stripe event id) is a no-op.
//
// Dispatch failures are recorded on the event row (processed stays
// false, error is set) but do NOT propagate to the HTTP layer: we 200
// the webhook so Stripe doesn't retry forever on a non-transient
// projection bug — the stuck event is visible in the admin UI instead.
// A *verification* failure, by contrast, does return an error so the
// handler can answer 400.
func (svc *Service) HandleWebhook(ctx context.Context, payload []byte, sigHeader string) error {
	cl, _, err := svc.client(ctx)
	if err != nil {
		return err
	}
	event, err := cl.ConstructWebhookEvent(payload, sigHeader)
	if err != nil {
		return fmt.Errorf("stripe: verify webhook: %w", err)
	}

	inserted, err := svc.store.InsertEvent(ctx, event.ID, string(event.Type), payload)
	if err != nil {
		return err
	}
	if !inserted {
		return nil // duplicate delivery — already handled
	}

	if derr := svc.dispatchEvent(ctx, event); derr != nil {
		_ = svc.store.MarkEventProcessed(ctx, event.ID, derr.Error())
		svc.log.Error("stripe: webhook dispatch failed",
			"event", event.ID, "type", event.Type, "err", derr)
		return nil
	}
	return svc.store.MarkEventProcessed(ctx, event.ID, "")
}

// dispatchEvent routes one verified event to the right mirror-table
// update. Unhandled types are accepted as no-ops — the local catalog
// is the source of truth for products/prices, so product.* / price.* /
// invoice.* are deliberately not synced downward.
func (svc *Service) dispatchEvent(ctx context.Context, event stripesdk.Event) error {
	if event.Data == nil {
		return nil
	}
	switch event.Type {
	case "payment_intent.succeeded",
		"payment_intent.payment_failed",
		"payment_intent.processing",
		"payment_intent.canceled",
		"payment_intent.requires_action":
		var pi stripesdk.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			return fmt.Errorf("decode payment_intent: %w", err)
		}
		return svc.applyPaymentIntent(ctx, &pi)

	case "customer.created", "customer.updated":
		var c stripesdk.Customer
		if err := json.Unmarshal(event.Data.Raw, &c); err != nil {
			return fmt.Errorf("decode customer: %w", err)
		}
		_, err := svc.store.UpsertCustomer(ctx, c.ID, c.Email, c.Name, c.Metadata)
		return err

	case "customer.subscription.created",
		"customer.subscription.updated",
		"customer.subscription.deleted":
		var sub stripesdk.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			return fmt.Errorf("decode subscription: %w", err)
		}
		local, err := svc.localSubFromStripe(ctx, &sub)
		if err != nil {
			return err
		}
		_, err = svc.store.UpsertSubscription(ctx, local)
		return err

	default:
		return nil
	}
}

// applyPaymentIntent projects a PaymentIntent status change onto the
// local _stripe_payments row. A PaymentIntent we never created (no
// local row) is ignored rather than synthesised — we only mirror our
// own checkouts.
func (svc *Service) applyPaymentIntent(ctx context.Context, pi *stripesdk.PaymentIntent) error {
	existing, err := svc.store.GetPaymentByStripeID(ctx, pi.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	existing.Status = string(pi.Status)
	existing.Amount = pi.Amount
	if pi.Description != "" {
		existing.Description = pi.Description
	}
	_, err = svc.store.UpsertPayment(ctx, *existing)
	return err
}

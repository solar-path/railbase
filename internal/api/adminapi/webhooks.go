package adminapi

// v1.7.17 §3.11 — admin endpoints for the v1.5.0 outbound webhooks
// subsystem. Companion to the `railbase webhooks ...` CLI; same data
// model, web-ergonomic surface.
//
// Routes (all under /api/_admin, gated by RequireAdmin upstream):
//
//	GET    /webhooks                                 list (no pagination)
//	POST   /webhooks                                 create + display-once secret
//	POST   /webhooks/{id}/pause                      flip active=false
//	POST   /webhooks/{id}/resume                     flip active=true
//	DELETE /webhooks/{id}                            idempotent delete
//	GET    /webhooks/{id}/deliveries?limit=          recent attempts
//	POST   /webhooks/{id}/deliveries/{did}/replay    re-enqueue a failed event
//
// Display-once contract (mirrors api_tokens.go): the raw HMAC secret
// is emitted exactly once, from the Create response. List / Get never
// include it — webhooks.Webhook tags SecretB64 as json:"-" so the
// struct can be serialised verbatim everywhere else.
//
// Nil-guard discipline: mountWebhooks skips route registration when
// d.Webhooks is nil, AND each handler nil-guards defensively for the
// "called directly in a test" case. Same shape as mountRealtime /
// the apitoken handlers.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/webhooks"
)

// mountWebhooks registers the webhooks admin surface when a Store is
// wired. When d.Webhooks is nil every route is skipped — test Deps
// constructing a bare struct gets a clean 404 instead of a nil-deref.
func (d *Deps) mountWebhooks(r chi.Router) {
	if d.Webhooks == nil {
		return
	}
	r.Get("/webhooks", d.webhooksListHandler)
	r.Post("/webhooks", d.webhooksCreateHandler)
	r.Post("/webhooks/{id}/pause", d.webhooksPauseHandler)
	r.Post("/webhooks/{id}/resume", d.webhooksResumeHandler)
	r.Delete("/webhooks/{id}", d.webhooksDeleteHandler)
	r.Get("/webhooks/{id}/deliveries", d.webhooksDeliveriesHandler)
	r.Post("/webhooks/{id}/deliveries/{deliveryID}/replay", d.webhooksReplayHandler)
}

// webhooksListHandler — GET /api/_admin/webhooks.
//
// Returns every configured webhook (active + paused). No pagination:
// the operator-sized table rarely exceeds a few dozen rows in
// production. SecretB64 is json:"-" on the Webhook struct, so the
// serialised rows never leak the HMAC key.
func (d *Deps) webhooksListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Webhooks == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "webhooks not configured"))
		return
	}
	rows, err := d.Webhooks.List(r.Context())
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list webhooks"))
		return
	}
	if rows == nil {
		rows = []*webhooks.Webhook{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items": rows,
	})
}

// webhooksCreateRequest is the wire shape for POST. Field names match
// the CLI's flag names so operators switching surfaces don't relearn.
type webhooksCreateRequest struct {
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	Events      []string          `json:"events"`
	Description string            `json:"description"` // unused by Store today; kept for API parity / forward compat
	SecretB64   string            `json:"secret_b64"`  // optional override; usually auto-generated
	Active      *bool             `json:"active"`      // default true
	MaxAttempts int               `json:"max_attempts"`
	TimeoutMS   int               `json:"timeout_ms"`
	Headers     map[string]string `json:"headers"`
}

// webhooksCreateHandler — POST /api/_admin/webhooks.
//
// Returns the freshly-created record plus the raw HMAC secret in a
// sibling `secret` field exactly once. Reads of the same row via
// List / Deliveries never surface the secret again. Subsequent
// retrieval is only possible via `railbase webhooks reveal-secret`.
func (d *Deps) webhooksCreateHandler(w http.ResponseWriter, r *http.Request) {
	if d.Webhooks == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "webhooks not configured"))
		return
	}

	var req webhooksCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid JSON body"))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "name is required"))
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "url is required"))
		return
	}
	// Trim + drop empty event entries; reject the all-empty case so the
	// webhook can't be created with an unmatched-by-design event list.
	events := make([]string, 0, len(req.Events))
	for _, e := range req.Events {
		if t := strings.TrimSpace(e); t != "" {
			events = append(events, t)
		}
	}
	if len(events) == 0 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "events is required (at least one pattern)"))
		return
	}

	in := webhooks.CreateInput{
		Name:        req.Name,
		URL:         req.URL,
		SecretB64:   req.SecretB64,
		Events:      events,
		Active:      req.Active,
		MaxAttempts: req.MaxAttempts,
		TimeoutMS:   req.TimeoutMS,
		Headers:     req.Headers,
	}
	rec, err := d.Webhooks.Create(r.Context(), in)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create webhook"))
		return
	}

	// Display-once: emit SecretB64 as a sibling field. The Webhook
	// struct itself tags SecretB64 as json:"-" so the embedded record
	// keeps it hidden — only the explicit top-level "secret" exposes it.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"record": rec,
		"secret": rec.SecretB64,
	})
}

// webhooksPauseHandler — POST /api/_admin/webhooks/{id}/pause.
//
// SetActive(false) + re-read so the response carries the now-paused
// row. Idempotent: pausing an already-paused webhook is a no-op.
func (d *Deps) webhooksPauseHandler(w http.ResponseWriter, r *http.Request) {
	d.webhooksSetActive(w, r, false)
}

// webhooksResumeHandler — POST /api/_admin/webhooks/{id}/resume.
//
// SetActive(true) + re-read so the response mirrors the pause path.
func (d *Deps) webhooksResumeHandler(w http.ResponseWriter, r *http.Request) {
	d.webhooksSetActive(w, r, true)
}

// webhooksSetActive is the shared body of pause/resume. Both routes
// resolve {id} the same way, toggle the flag, then re-fetch so the
// response carries the canonical post-update row.
func (d *Deps) webhooksSetActive(w http.ResponseWriter, r *http.Request, active bool) {
	if d.Webhooks == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "webhooks not configured"))
		return
	}
	id, ok := parseWebhookID(w, r)
	if !ok {
		return
	}
	if err := d.Webhooks.SetActive(r.Context(), id, active); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "set active"))
		return
	}
	rec, err := d.Webhooks.GetByID(r.Context(), id)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeNotFound, "webhook not found"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"record": rec,
	})
}

// webhooksDeleteHandler — DELETE /api/_admin/webhooks/{id}.
//
// Idempotent per the Store contract. We surface the {deleted: bool}
// hint so the UI can distinguish "actually removed" from "already
// gone" without changing the status code.
func (d *Deps) webhooksDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if d.Webhooks == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "webhooks not configured"))
		return
	}
	id, ok := parseWebhookID(w, r)
	if !ok {
		return
	}
	deleted, err := d.Webhooks.Delete(r.Context(), id)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "delete webhook"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"deleted": deleted,
	})
}

// webhooksDeliveriesHandler — GET /api/_admin/webhooks/{id}/deliveries.
//
// `limit` query param: default 50, max 200. The Store's own cap is
// 1000 but we tighten it here so a runaway admin client can't pull a
// huge response.
func (d *Deps) webhooksDeliveriesHandler(w http.ResponseWriter, r *http.Request) {
	if d.Webhooks == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "webhooks not configured"))
		return
	}
	id, ok := parseWebhookID(w, r)
	if !ok {
		return
	}
	limit := parseIntParam(r, "limit", 50)
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := d.Webhooks.ListDeliveries(r.Context(), id, limit)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "list deliveries"))
		return
	}
	if rows == nil {
		rows = []*webhooks.Delivery{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items": rows,
	})
}

// webhooksReplayHandler — POST /api/_admin/webhooks/{id}/deliveries/{deliveryID}/replay.
//
// Dead-letter replay: looks up the original failed delivery, then
// enqueues a fresh delivery row (attempt=1, status=pending) carrying
// the same event + payload. The dispatcher job framework picks the
// new row up on the next sweep.
//
// We do NOT have a Store helper that returns one Delivery by id, so
// we List(limit=200) and filter client-side. That's bounded by the
// per-webhook ListDeliveries cap; if the failed delivery is older
// than the most recent 200, the replay returns not_found (operators
// would already be using the CLI in that scenario).
func (d *Deps) webhooksReplayHandler(w http.ResponseWriter, r *http.Request) {
	if d.Webhooks == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "webhooks not configured"))
		return
	}
	id, ok := parseWebhookID(w, r)
	if !ok {
		return
	}
	deliveryIDStr := chi.URLParam(r, "deliveryID")
	deliveryID, err := uuid.Parse(deliveryIDStr)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "deliveryID must be a valid UUID"))
		return
	}

	// Locate the original delivery within the recent window. The Store
	// caps ListDeliveries at 1000 internally; 200 is plenty for replay
	// use (operators chase recent failures).
	rows, err := d.Webhooks.ListDeliveries(r.Context(), id, 200)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "load deliveries"))
		return
	}
	var orig *webhooks.Delivery
	for _, row := range rows {
		if row.ID == deliveryID {
			orig = row
			break
		}
	}
	if orig == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeNotFound, "delivery not found within replay window (most recent 200)"))
		return
	}

	// Enqueue a fresh attempt=1 row. The dispatcher's job framework
	// will pick it up; we don't try to drive the HTTP POST inline.
	rec, err := d.Webhooks.InsertDelivery(r.Context(), id, orig.Event, orig.Payload, 1)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "enqueue replay"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"record": rec,
	})
}

// parseWebhookID is the shared {id} URL-param parser. Writes a typed
// 400 envelope on malformed input and returns ok=false so the caller
// can short-circuit. Mirrors the apitoken handlers' inline pattern but
// extracted because six handlers reuse it.
func parseWebhookID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "id must be a valid UUID"))
		return uuid.Nil, false
	}
	return id, true
}

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	rerr "github.com/railbase/railbase/internal/errors"
	"github.com/railbase/railbase/internal/realtime"
	"github.com/railbase/railbase/internal/schema/builder"
)

// batchRequest is the request body for POST /api/collections/{name}/records/batch.
//
// Atomic mode (default): all ops succeed or none do — SQL runs inside
// a single Postgres transaction; any per-op error rolls back the lot
// and returns 400 with the failed-op index.
//
// Non-atomic mode (atomic=false): each op runs independently; the
// response is 207 Multi-Status with per-op status codes. Use for
// "best-effort sync" patterns where partial success is acceptable.
//
// Each op has a discriminator (`action`: "create" | "update" | "delete")
// and the relevant payload. Cross-collection batches aren't supported
// in v1.4.13 — the URL path fixes the target collection for every op.
type batchRequest struct {
	Atomic *bool      `json:"atomic"` // nil → defaults to true
	Ops    []batchOp  `json:"ops"`
}

type batchOp struct {
	Action string         `json:"action"`        // "create" | "update" | "delete"
	ID     string         `json:"id,omitempty"`  // required for update/delete
	Data   map[string]any `json:"data,omitempty"` // required for create/update
}

// batchResultItem is one element of the response's `results` array.
// On success we return the row body (create + update) or 204 (delete).
// On per-op failure (non-atomic mode) we return the error envelope.
type batchResultItem struct {
	Action string          `json:"action"`
	Status int             `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  *rerr.Error     `json:"error,omitempty"`
}

// batchHandler implements POST /api/collections/{name}/records/batch.
//
// Hooks + realtime semantics in v1.4.13:
//   - Hooks (Before/After Create/Update/Delete) are NOT fired per op.
//     Batch is a "bulk SQL" primitive; hooks fire on individual ops
//     called outside batch. Restoring hook coverage in batch is a
//     follow-up — needs design for "what does atomic rollback mean
//     when AfterCreate has already side-effected emails / webhooks?".
//   - Realtime: events are buffered during the run and published
//     AFTER commit (atomic mode). For non-atomic mode events publish
//     per-op as the op succeeds.
//   - Rules: each op composes its own rule fragment (Create/Update/
//     Delete) via composeRowExtras — same paths as individual handlers.
//   - Soft-delete: buildDelete switches automatically; buildUpdate /
//     buildView refuse tombstones.
func (d *handlerDeps) batchHandler(w http.ResponseWriter, r *http.Request) {
	spec, err := resolveCollection(chi.URLParam(r, "name"))
	if err != nil {
		rerr.WriteJSON(w, err)
		return
	}

	body, ioErr := io.ReadAll(io.LimitReader(r.Body, 4<<20)) // 4 MiB ceiling for batches
	if ioErr != nil {
		rerr.WriteJSON(w, rerr.Wrap(ioErr, rerr.CodeValidation, "read body failed"))
		return
	}
	defer r.Body.Close()

	var req batchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "invalid batch body: %s", err.Error()))
		return
	}
	if len(req.Ops) == 0 {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation, "batch must have at least one op"))
		return
	}
	// Cap batch size — 200 ops per request is plenty for form-save /
	// bulk-import scenarios; larger batches should chunk client-side.
	const maxOpsPerBatch = 200
	if len(req.Ops) > maxOpsPerBatch {
		rerr.WriteJSON(w, rerr.New(rerr.CodeValidation,
			"batch exceeds max ops %d (got %d) — chunk client-side", maxOpsPerBatch, len(req.Ops)))
		return
	}

	atomic := true
	if req.Atomic != nil {
		atomic = *req.Atomic
	}

	q, qErr := d.queryFor(r.Context(), spec)
	if qErr != nil {
		rerr.WriteJSON(w, qErr)
		return
	}

	if atomic {
		d.batchAtomic(w, r, spec, q, req.Ops)
	} else {
		d.batchNonAtomic(w, r, spec, q, req.Ops)
	}
}

// batchAtomic runs all ops in a single transaction. On the first error
// the tx is rolled back; the response is 400 with `failedIndex` so the
// client can correlate. Realtime publish runs only after commit.
func (d *handlerDeps) batchAtomic(w http.ResponseWriter, r *http.Request, spec builder.CollectionSpec, q pgQuerier, ops []batchOp) {
	ctx := r.Context()
	tx, err := q.Begin(ctx)
	if err != nil {
		d.log.Error("rest: batch tx begin failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "batch begin failed"))
		return
	}
	// Guard rollback. Commit happens at the bottom of the success path.
	defer func() { _ = tx.Rollback(ctx) }()

	results := make([]batchResultItem, 0, len(ops))
	// Realtime events are buffered until commit — we never want subscribers
	// to see a row that got rolled back.
	type pendingEvent struct {
		verb realtime.Verb
		row  map[string]any
	}
	var pending []pendingEvent

	for i, op := range ops {
		item, row, verb, opErr := d.applyOp(ctx, r, spec, tx, op)
		if opErr != nil {
			// Atomic failure — surface failed index + error.
			full := rerr.New(rerr.CodeValidation, "batch op %d (%s) failed", i, op.Action).
				WithDetail("failedIndex", i).
				WithDetail("failedError", opErr.Code)
			// Attach the original message for actionability.
			full = full.WithDetail("failedMessage", opErr.Message)
			if opErr.Details != nil {
				full = full.WithDetail("failedDetails", opErr.Details)
			}
			rerr.WriteJSON(w, full)
			return
		}
		results = append(results, item)
		if row != nil {
			pending = append(pending, pendingEvent{verb: verb, row: row})
		}
	}

	if err := tx.Commit(ctx); err != nil {
		d.log.Error("rest: batch commit failed", "collection", spec.Name, "err", err)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "batch commit failed"))
		return
	}

	// Now safe to publish. Each event uses the same pattern as individual
	// handlers — same eventbus, same topic shape, same SDK contract.
	for _, ev := range pending {
		d.publishRecord(r, spec, ev.verb, ev.row)
	}

	resp := map[string]any{"results": results}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// batchNonAtomic runs ops independently. Each result reports its own
// status code; the overall response is 207 Multi-Status so the client
// knows to inspect per-op results.
func (d *handlerDeps) batchNonAtomic(w http.ResponseWriter, r *http.Request, spec builder.CollectionSpec, q pgQuerier, ops []batchOp) {
	ctx := r.Context()
	results := make([]batchResultItem, 0, len(ops))
	for _, op := range ops {
		item, row, verb, opErr := d.applyOp(ctx, r, spec, q, op)
		if opErr != nil {
			results = append(results, batchResultItem{
				Action: op.Action,
				Status: rerr.HTTPStatus(opErr.Code),
				Error:  opErr,
			})
			continue
		}
		results = append(results, item)
		// Publish immediately — non-atomic mode has no rollback so
		// subscribers seeing the row before the next op is safe.
		if row != nil {
			d.publishRecord(r, spec, verb, row)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusMultiStatus) // 207
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
}

// applyOp runs a single op against the given querier (which can be the
// raw pool/conn for non-atomic, or a tx for atomic). Returns:
//
//   - item: the batch-response entry for this op
//   - row: the row map (for realtime publish); nil for delete or error
//   - verb: realtime verb (create/update/delete); zero for delete
//   - err: non-nil when the op failed
func (d *handlerDeps) applyOp(ctx context.Context, r *http.Request, spec builder.CollectionSpec, q pgQuerier, op batchOp) (batchResultItem, map[string]any, realtime.Verb, *rerr.Error) {
	fctx := filterCtx(authmw.PrincipalFrom(ctx))

	switch op.Action {
	case "create":
		if op.Data == nil {
			return batchResultItem{}, nil, "", rerr.New(rerr.CodeValidation, "create op requires data")
		}
		fields, _, perr := validateInputForBatch(spec, op.Data, true)
		if perr != nil {
			return batchResultItem{}, nil, "", perr
		}
		for k, v := range tenantInsertExtras(ctx, spec) {
			fields[k] = v
		}
		sql, args, buildErr := buildInsert(spec, fields)
		if buildErr != nil {
			return batchResultItem{}, nil, "", rerr.Wrap(buildErr, rerr.CodeValidation, "%s", buildErr.Error())
		}
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return batchResultItem{}, nil, "", classifyPgErr(err, "insert")
		}
		defer rows.Close()
		if !rows.Next() {
			if iterErr := rows.Err(); iterErr != nil {
				return batchResultItem{}, nil, "", classifyPgErr(iterErr, "insert")
			}
			return batchResultItem{}, nil, "", rerr.New(rerr.CodeInternal, "insert returned no row")
		}
		row, err := scanRow(rows, spec)
		if err != nil {
			return batchResultItem{}, nil, "", rerr.Wrap(err, rerr.CodeInternal, "scan failed")
		}
		buf, err := marshalRecordLoc(spec, row, d.recordURLFn(spec, row), requestLocaleFor(ctx, spec))
		if err != nil {
			return batchResultItem{}, nil, "", rerr.Wrap(err, rerr.CodeInternal, "marshal failed")
		}
		return batchResultItem{Action: "create", Status: 200, Data: buf}, row, realtime.VerbCreate, nil

	case "update":
		if op.ID == "" {
			return batchResultItem{}, nil, "", rerr.New(rerr.CodeValidation, "update op requires id")
		}
		if op.Data == nil {
			return batchResultItem{}, nil, "", rerr.New(rerr.CodeValidation, "update op requires data")
		}
		fields, _, perr := validateInputForBatch(spec, op.Data, false)
		if perr != nil {
			return batchResultItem{}, nil, "", perr
		}
		extras, err := composeRowExtras(ctx, spec, fctx, spec.Rules.Update, len(fields)+2)
		if err != nil {
			return batchResultItem{}, nil, "", rerr.Wrap(err, rerr.CodeInternal, "rule compile failed")
		}
		sql, args, buildErr := buildUpdate(spec, op.ID, fields, extras.Where, extras.Args)
		if buildErr != nil {
			return batchResultItem{}, nil, "", rerr.Wrap(buildErr, rerr.CodeValidation, "%s", buildErr.Error())
		}
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			if isInvalidUUID(err) {
				return batchResultItem{}, nil, "", rerr.New(rerr.CodeNotFound, "record not found")
			}
			return batchResultItem{}, nil, "", classifyPgErr(err, "update")
		}
		defer rows.Close()
		if !rows.Next() {
			if iterErr := rows.Err(); iterErr != nil {
				return batchResultItem{}, nil, "", classifyPgErr(iterErr, "update")
			}
			return batchResultItem{}, nil, "", rerr.New(rerr.CodeNotFound, "record not found")
		}
		row, err := scanRow(rows, spec)
		if err != nil {
			return batchResultItem{}, nil, "", rerr.Wrap(err, rerr.CodeInternal, "scan failed")
		}
		buf, err := marshalRecordLoc(spec, row, d.recordURLFn(spec, row), requestLocaleFor(ctx, spec))
		if err != nil {
			return batchResultItem{}, nil, "", rerr.Wrap(err, rerr.CodeInternal, "marshal failed")
		}
		return batchResultItem{Action: "update", Status: 200, Data: buf}, row, realtime.VerbUpdate, nil

	case "delete":
		if op.ID == "" {
			return batchResultItem{}, nil, "", rerr.New(rerr.CodeValidation, "delete op requires id")
		}
		extras, err := composeRowExtras(ctx, spec, fctx, spec.Rules.Delete, 2)
		if err != nil {
			return batchResultItem{}, nil, "", rerr.Wrap(err, rerr.CodeInternal, "rule compile failed")
		}
		sql, args := buildDelete(spec, op.ID, extras.Where, extras.Args)
		var returned string
		scanErr := q.QueryRow(ctx, sql, args...).Scan(&returned)
		if scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return batchResultItem{}, nil, "", rerr.New(rerr.CodeNotFound, "record not found")
			}
			if isInvalidUUID(scanErr) {
				return batchResultItem{}, nil, "", rerr.New(rerr.CodeNotFound, "record not found")
			}
			return batchResultItem{}, nil, "", classifyPgErr(scanErr, "delete")
		}
		// Realtime publish carries id only.
		return batchResultItem{Action: "delete", Status: 204}, map[string]any{"id": op.ID}, realtime.VerbDelete, nil

	default:
		return batchResultItem{}, nil, "", rerr.New(rerr.CodeValidation, "unknown action %q (want create|update|delete)", op.Action)
	}
}

// validateInputForBatch is a tiny wrapper around parseInput that turns
// the *parseErr into a *rerr.Error. Same shape as individual handlers
// but reusable from the batch loop. The third return value is reserved
// for future use (signature compat with possible enriched return).
func validateInputForBatch(spec builder.CollectionSpec, raw map[string]any, create bool) (map[string]any, any, *rerr.Error) {
	// parseInput takes []byte; marshal back and forth keeps the call
	// site simple at the cost of one extra JSON round-trip per op.
	// 200-op batches → 200 marshals; negligible vs. SQL latency.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, rerr.Wrap(err, rerr.CodeValidation, "encode op data: %s", err.Error())
	}
	fields, perr := parseInput(spec, b, create)
	if perr != nil {
		e := rerr.New(rerr.CodeValidation, "%s", perr.Message)
		for k, v := range perr.Details {
			e = e.WithDetail(k, v)
		}
		return nil, nil, e
	}
	return fields, nil, nil
}

// classifyPgErr is a thin wrapper around pgErrorFor that always returns
// a non-nil *rerr.Error — falls back to CodeInternal when the err
// isn't a known pgError shape.
func classifyPgErr(err error, op string) *rerr.Error {
	if pgErr := pgErrorFor(err); pgErr != nil {
		return pgErr
	}
	return rerr.Wrap(err, rerr.CodeInternal, "%s failed: %s", op, err.Error())
}


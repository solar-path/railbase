// Package audit is the append-only event log with a SHA-256 hash chain.
//
// Spec: docs/14-observability.md "Audit log".
//
// The single most important invariant is that audit writes happen
// OUTSIDE the request transaction:
//
//	Critical правило (из rail): audit пишется через bare pool, не
//	через request-tx. Иначе rollback бизнес-транзакции стирает запись
//	о денае.
//
// In Go terms: Writer.Write does not accept a *pgx.Tx. It always
// acquires its own connection from the pool. A handler can refuse a
// request, log "rbac.deny", and the deny record persists even though
// the rest of the handler's work rolls back.
//
// Hash chain: every row carries `prev_hash` (the previous row's
// hash) and `hash = sha256(prev_hash || canonical_json(row_minus_hash))`.
// `Verify` walks the chain from the start and returns the row index
// where it first breaks — admin UI and `railbase audit verify` use
// the result to highlight tampering.
//
// What v0.6 ships:
//
//   - Writer.Write — single-event insertion under a per-Writer mutex
//     (writes serialize so prev_hash is always current)
//   - Verify — full-chain integrity check
//   - PII redaction allow-list — passwords / tokens are stripped
//     from `before`/`after` payloads before persist
//
// What's deferred:
//
//   - Ed25519 chain sealer (v1.1, requires the audit retention job)
//   - Per-event-source bulk insert / sharding (v1.2 if profiling shows
//     contention on the global Writer mutex)
//   - Granular `_document_access_log` (v1, lands with documents)
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Outcome is the small enum of what an event resolved to.
// Adding values is fine; renaming breaks audit verifiers in the wild.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeDenied  Outcome = "denied"
	OutcomeFailed  Outcome = "failed"
	OutcomeError   Outcome = "error"
)

// Event captures one audit row before persistence. Caller fills
// what's relevant; Writer fills id, seq, at, prev_hash, hash.
type Event struct {
	UserID         uuid.UUID
	UserCollection string
	TenantID       uuid.UUID
	Event          string  // dotted name: "auth.signin", "rbac.deny"
	Outcome        Outcome
	Before         any     // optional structured payload (will be redacted)
	After          any     // optional structured payload (will be redacted)
	ErrorCode      string
	IP             string
	UserAgent      string
}

// Writer is the persistence handle. Use NewWriter once on boot and
// share for the lifetime of the process. Goroutine-safe.
type Writer struct {
	pool *pgxpool.Pool

	mu       sync.Mutex // serialises hash-chain advancement
	prevHash []byte     // last row's hash; 32 zero bytes before first write
}

// NewWriter constructs a Writer. The Bootstrap call (next) loads the
// most recent row's hash so a process restart resumes the chain
// correctly.
func NewWriter(pool *pgxpool.Pool) *Writer {
	return &Writer{
		pool:     pool,
		prevHash: make([]byte, 32), // genesis: all zeros
	}
}

// Bootstrap reads the last row's hash so subsequent writes link onto
// the existing chain. Called once on app boot — idempotent on an
// empty table (prev_hash stays as zero bytes).
func (w *Writer) Bootstrap(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var hash []byte
	err := w.pool.QueryRow(ctx,
		`SELECT hash FROM _audit_log ORDER BY seq DESC LIMIT 1`).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// First write will use the all-zeros prev_hash.
		return nil
	}
	if err != nil {
		return fmt.Errorf("audit: bootstrap: %w", err)
	}
	w.prevHash = hash
	return nil
}

// Write persists e. Returns the assigned row id; callers usually
// ignore it (audit is fire-and-forget) but tests want it to fetch
// the row back for assertions.
//
// The pool used here is the same pool the rest of the app uses,
// but the call is NOT wrapped in any caller-supplied transaction —
// see package doc on bare-pool rule.
func (w *Writer) Write(ctx context.Context, e Event) (uuid.UUID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	id := uuid.Must(uuid.NewV7())
	// Postgres TIMESTAMPTZ stores microseconds. We must hash the
	// truncated value, otherwise the row read back from the DB
	// produces a different canonical-JSON byte sequence and the
	// chain verifier reports false positives.
	at := time.Now().UTC().Truncate(time.Microsecond)

	beforeJSON, err := redactJSON(e.Before)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit: encode before: %w", err)
	}
	afterJSON, err := redactJSON(e.After)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit: encode after: %w", err)
	}

	// Build the canonical hash input. We hash the columns we're about
	// to write, so the chain remains stable across schema additions —
	// new columns mean new chain, old verifiers still work.
	hash := computeHash(w.prevHash, canonicalRow{
		ID:             id,
		At:             at,
		UserID:         nilToZeroUUID(e.UserID),
		UserCollection: e.UserCollection,
		TenantID:       nilToZeroUUID(e.TenantID),
		Event:          e.Event,
		Outcome:        string(e.Outcome),
		Before:         json.RawMessage(beforeJSON),
		After:          json.RawMessage(afterJSON),
		ErrorCode:      e.ErrorCode,
		IP:             e.IP,
		UserAgent:      e.UserAgent,
	})

	const q = `
        INSERT INTO _audit_log
            (id, at,
             user_id, user_collection, tenant_id,
             event, outcome,
             before, after,
             error_code, ip, user_agent,
             prev_hash, hash)
        VALUES
            ($1, $2,
             $3, $4, $5,
             $6, $7,
             $8, $9,
             $10, $11, $12,
             $13, $14)
    `
	if _, err := w.pool.Exec(ctx, q,
		id, at,
		nullableUUID(e.UserID), nullableText(e.UserCollection), nullableUUID(e.TenantID),
		e.Event, string(e.Outcome),
		nullableJSON(beforeJSON), nullableJSON(afterJSON),
		nullableText(e.ErrorCode), nullableText(e.IP), nullableText(e.UserAgent),
		w.prevHash, hash,
	); err != nil {
		return uuid.Nil, fmt.Errorf("audit: insert: %w", err)
	}
	w.prevHash = hash
	return id, nil
}

// Verify walks the chain from seq=1 forward and returns ErrChainBroken
// at the first row whose hash doesn't match the recomputed value.
// Returns (rows verified, error).
func (w *Writer) Verify(ctx context.Context) (int64, error) {
	rows, err := w.pool.Query(ctx, `
        SELECT id, at,
               COALESCE(user_id::text, ''),
               COALESCE(user_collection, ''),
               COALESCE(tenant_id::text, ''),
               event, outcome,
               COALESCE(before::text, 'null'),
               COALESCE(after::text, 'null'),
               COALESCE(error_code, ''),
               COALESCE(ip, ''),
               COALESCE(user_agent, ''),
               prev_hash, hash, seq
          FROM _audit_log
         ORDER BY seq ASC
    `)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	expected := make([]byte, 32) // genesis = zeros
	var n int64
	for rows.Next() {
		var row verifyRow
		if err := rows.Scan(
			&row.ID, &row.At,
			&row.UserID, &row.UserCollection, &row.TenantID,
			&row.Event, &row.Outcome,
			&row.Before, &row.After,
			&row.ErrorCode, &row.IP, &row.UserAgent,
			&row.PrevHash, &row.Hash, &row.Seq,
		); err != nil {
			return n, err
		}
		if !bytesEqual(row.PrevHash, expected) {
			return n, &ChainError{Seq: row.Seq, Reason: "prev_hash mismatch"}
		}
		gotHash := computeHashFromVerifyRow(row.PrevHash, row)
		if !bytesEqual(row.Hash, gotHash) {
			return n, &ChainError{Seq: row.Seq, Reason: "hash mismatch"}
		}
		expected = row.Hash
		n++
	}
	return n, rows.Err()
}

// ChainError reports verification failure.
type ChainError struct {
	Seq    int64
	Reason string
}

func (e *ChainError) Error() string {
	return fmt.Sprintf("audit: chain broken at seq=%d: %s", e.Seq, e.Reason)
}

// ListFilter constrains a ListFiltered query. All fields optional;
// zero values disable the corresponding filter. The chain semantics
// are unaffected — this is a read-only convenience for the admin UI.
//
// The Postgres column the timestamp bounds run against is `at`, the
// wall-clock time the row was written (see migration 0006).
type ListFilter struct {
	// Event is a case-insensitive substring match on the `event`
	// column, e.g. "auth.signin" or "admin.". Empty = no filter.
	Event string
	// Outcome is an exact match against the `outcome` column. Empty =
	// no filter. The audit.Outcome enum is the legal set.
	Outcome Outcome
	// UserID is an exact match against the `user_id` column. Nil
	// (uuid.Nil) = no filter; the canonical "no user" rows store NULL
	// in that column so this never accidentally matches them.
	UserID uuid.UUID
	// Since lower-bounds the `at` column. Zero = no lower bound.
	Since time.Time
	// Until upper-bounds the `at` column. Zero = no upper bound.
	Until time.Time
	// ErrorCode is a case-insensitive substring match on the
	// `error_code` column. Empty = no filter.
	ErrorCode string
}

// ListFiltered returns audit rows matching f, newest-first, truncated
// to limit. Read-only — does not touch the hash chain. Use Verify for
// integrity checks.
//
// The shipped Event struct on the writer side is the input shape; this
// returns the same shape minus the persistence-internal fields
// (prev_hash, hash, seq) plus the assigned id + at. The admin endpoint
// re-flattens the result into its wire JSON.
func (w *Writer) ListFiltered(ctx context.Context, f ListFilter, limit int) ([]*ListedEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	where, args := buildAuditWhere(f)
	q := `SELECT seq, id, at,
                 COALESCE(user_id::text, ''),
                 COALESCE(user_collection, ''),
                 COALESCE(tenant_id::text, ''),
                 event, outcome,
                 COALESCE(error_code, ''),
                 COALESCE(ip, ''),
                 COALESCE(user_agent, '')
            FROM _audit_log` + where + fmt.Sprintf(" ORDER BY seq DESC LIMIT %d", limit)
	rows, err := w.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer rows.Close()
	out := make([]*ListedEvent, 0, limit)
	for rows.Next() {
		var (
			seq                                                int64
			id                                                 uuid.UUID
			at                                                 time.Time
			userID, userColl, tenantID, event, outcome         string
			errorCode, ip, userAgent                           string
		)
		if err := rows.Scan(&seq, &id, &at, &userID, &userColl, &tenantID,
			&event, &outcome, &errorCode, &ip, &userAgent); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		out = append(out, &ListedEvent{
			Seq:            seq,
			ID:             id,
			At:             at,
			UserID:         userID,
			UserCollection: userColl,
			TenantID:       tenantID,
			Event:          event,
			Outcome:        outcome,
			ErrorCode:      errorCode,
			IP:             ip,
			UserAgent:      userAgent,
		})
	}
	return out, rows.Err()
}

// Count returns the total rows matching f. Used by the admin endpoint
// for the totalItems pagination header.
func (w *Writer) Count(ctx context.Context, f ListFilter) (int64, error) {
	where, args := buildAuditWhere(f)
	q := `SELECT count(*) FROM _audit_log` + where
	var c int64
	if err := w.pool.QueryRow(ctx, q, args...).Scan(&c); err != nil {
		return 0, fmt.Errorf("audit: count: %w", err)
	}
	return c, nil
}

// ListedEvent is the read-shape returned by ListFiltered. String
// fields are empty (not nil) when the underlying column is NULL —
// the admin endpoint coerces "" to JSON null on the wire to match the
// existing v0.8 response shape.
type ListedEvent struct {
	Seq            int64
	ID             uuid.UUID
	At             time.Time
	UserID         string
	UserCollection string
	TenantID       string
	Event          string
	Outcome        string
	ErrorCode      string
	IP             string
	UserAgent      string
}

// buildAuditWhere returns the WHERE clause (including the leading
// " WHERE " when non-empty) and the positional argument slice for
// the filter. Centralised so ListFiltered + Count share the exact
// same predicate construction; tests assert on the result.
func buildAuditWhere(f ListFilter) (string, []any) {
	var (
		clauses []string
		args    []any
		argN    int
	)
	addArg := func(v any) string {
		argN++
		args = append(args, v)
		return fmt.Sprintf("$%d", argN)
	}
	if f.Event != "" {
		clauses = append(clauses, "event ILIKE "+addArg("%"+f.Event+"%"))
	}
	if f.Outcome != "" {
		clauses = append(clauses, "outcome = "+addArg(string(f.Outcome)))
	}
	if f.UserID != uuid.Nil {
		clauses = append(clauses, "user_id = "+addArg(f.UserID))
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "at >= "+addArg(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "at <= "+addArg(f.Until))
	}
	if f.ErrorCode != "" {
		clauses = append(clauses, "error_code ILIKE "+addArg("%"+f.ErrorCode+"%"))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + joinStrings(clauses, " AND "), args
}

// joinStrings is strings.Join inlined to avoid adding the import.
func joinStrings(parts []string, sep string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	n := len(sep) * (len(parts) - 1)
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 0, n)
	b = append(b, parts[0]...)
	for _, p := range parts[1:] {
		b = append(b, sep...)
		b = append(b, p...)
	}
	return string(b)
}

// canonicalRow is the input to computeHash. JSON marshalling of this
// struct with sorted keys gives the stable byte sequence the chain
// is computed over.
//
// Before/After are json.RawMessage (NOT []byte) so they're embedded
// verbatim — not base64-encoded — when marshalled. The outer
// canonicalJSON then re-parses and sorts, so any whitespace or key-
// order differences between the Write-time bytes (Go json.Marshal)
// and Verify-time bytes (Postgres JSONB ::text) get normalised away.
// Critical for chain stability across persistence round-trip.
type canonicalRow struct {
	ID             uuid.UUID       `json:"id"`
	At             time.Time       `json:"at"`
	UserID         uuid.UUID       `json:"user_id"`
	UserCollection string          `json:"user_collection"`
	TenantID       uuid.UUID       `json:"tenant_id"`
	Event          string          `json:"event"`
	Outcome        string          `json:"outcome"`
	Before         json.RawMessage `json:"before"`
	After          json.RawMessage `json:"after"`
	ErrorCode      string          `json:"error_code"`
	IP             string          `json:"ip"`
	UserAgent      string          `json:"user_agent"`
}

// computeHash is sha256(prev || canonical_json(row)). The canonical
// form here is a deterministic encoding of canonicalRow: marshal,
// re-decode into a generic map, sort keys, re-marshal. Three steps
// because Go's json package doesn't sort map keys when marshalling
// a struct (struct fields stay in declaration order, map keys are
// sorted automatically).
func computeHash(prev []byte, row canonicalRow) []byte {
	body, err := canonicalJSON(row)
	if err != nil {
		// computeHash takes Go-controlled types only — encoding errors
		// imply a programming bug, not user input.
		panic("audit: canonicalJSON: " + err.Error())
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(body)
	return h.Sum(nil)
}

// canonicalJSON emits a deterministic byte sequence: encode-decode-
// sort-encode. Struct → generic any → map[string]any (already sorted
// on marshal) → bytes.
func canonicalJSON(row canonicalRow) ([]byte, error) {
	raw, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return marshalSorted(generic)
}

// marshalSorted writes a JSON object with keys in lexicographic order.
// Recursive: nested maps are also sorted. Arrays are emitted in input
// order.
func marshalSorted(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []byte
		out = append(out, '{')
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			out = append(out, kb...)
			out = append(out, ':')
			vb, err := marshalSorted(t[k])
			if err != nil {
				return nil, err
			}
			out = append(out, vb...)
		}
		out = append(out, '}')
		return out, nil
	case []any:
		var out []byte
		out = append(out, '[')
		for i, item := range t {
			if i > 0 {
				out = append(out, ',')
			}
			ib, err := marshalSorted(item)
			if err != nil {
				return nil, err
			}
			out = append(out, ib...)
		}
		out = append(out, ']')
		return out, nil
	default:
		return json.Marshal(v)
	}
}

// computeHashFromVerifyRow reconstructs the canonical row using
// the verifier's read-back fields. UUIDs are passed as strings here
// because Postgres returns them stringly when we COALESCE for null;
// we re-parse them so the canonical bytes match.
func computeHashFromVerifyRow(prev []byte, r verifyRow) []byte {
	cr := canonicalRow{
		ID:             r.ID,
		At:             r.At.UTC().Truncate(time.Microsecond),
		UserCollection: r.UserCollection,
		Event:          r.Event,
		Outcome:        r.Outcome,
		Before:         json.RawMessage(r.Before),
		After:          json.RawMessage(r.After),
		ErrorCode:      r.ErrorCode,
		IP:             r.IP,
		UserAgent:      r.UserAgent,
	}
	if r.UserID != "" {
		if u, err := uuid.Parse(r.UserID); err == nil {
			cr.UserID = u
		}
	}
	if r.TenantID != "" {
		if u, err := uuid.Parse(r.TenantID); err == nil {
			cr.TenantID = u
		}
	}
	return computeHash(prev, cr)
}

// verifyRow is the row shape Verify scans into. Strings instead of
// nullable types so the COALESCE in the SELECT can fold NULL into
// the empty string consistently — matters because the canonical
// row's empty-string fields hash differently than a real null.
type verifyRow struct {
	ID             uuid.UUID
	At             time.Time
	UserID         string
	UserCollection string
	TenantID       string
	Event          string
	Outcome        string
	Before         string
	After          string
	ErrorCode      string
	IP             string
	UserAgent      string
	PrevHash       []byte
	Hash           []byte
	Seq            int64
}

// redactJSON marshals a value to JSON with PII fields replaced. The
// allow-list approach is conservative: when in doubt, the value
// passes through. Future versions can tighten by tagging fields
// with `rb:"secret"` (requires reflection).
//
// v0.6 redacts top-level keys named "password", "password_hash",
// "token", "token_key", "secret_key", "totp_secret", "secret".
// Bearer tokens get the prefix-only treatment via separate logic
// upstream when they're already extracted.
func redactJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	redact(generic)
	return json.Marshal(generic)
}

func redact(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		// arrays containing maps still get recursed
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				redact(item)
			}
		}
		return
	}
	for k, vv := range m {
		switch k {
		case "password", "password_hash", "token", "token_key",
			"secret_key", "totp_secret", "secret":
			m[k] = "[REDACTED]"
		default:
			redact(vv)
		}
	}
}

// nilToZeroUUID returns u — but our canonical hash treats uuid.Nil
// (all zeros) the same as "not set", which is what we want.
func nilToZeroUUID(u uuid.UUID) uuid.UUID { return u }

// nullableUUID returns nil for the zero UUID so Postgres stores NULL
// rather than the all-zeros uuid (more honest in audit output).
func nullableUUID(u uuid.UUID) any {
	if u == uuid.Nil {
		return nil
	}
	return u
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// bytesEqual is bytes.Equal inlined to avoid the import.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

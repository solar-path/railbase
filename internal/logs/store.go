package logs

// Read-side: List + Filter for the admin endpoint. Lives alongside
// the writer-side Sink because they share the same row shape +
// the JSONB attrs decode.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Record is the read-shape of `_logs` rows. Mirrors the writer's
// `entry` but de-serialises attrs back to a map for JSON-encoding
// in the admin response.
type Record struct {
	ID        uuid.UUID      `json:"id"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Attrs     map[string]any `json:"attrs"`
	Source    *string        `json:"source,omitempty"`
	RequestID *string        `json:"request_id,omitempty"`
	UserID    *uuid.UUID     `json:"user_id,omitempty"`
	Created   time.Time      `json:"created"`
}

// Store is the read handle. Wired separately from Sink because tests
// (and the admin endpoint) want to read without instantiating the
// writer + its background flusher.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ListFilter constrains a List query. All fields optional; zero
// values disable the corresponding filter.
type ListFilter struct {
	// Level filters to >= the given level. Empty = no filter.
	Level string
	// Since lower-bounds `created`. Zero = no lower bound.
	Since time.Time
	// Until upper-bounds `created`. Zero = no upper bound.
	Until time.Time
	// RequestID exact match. Empty = no filter.
	RequestID string
	// UserID exact match. Nil = no filter.
	UserID *uuid.UUID
	// Search is a case-insensitive substring match on message.
	// Empty = no filter.
	Search string
	// Limit bounds the result set. Default 100, max 1000.
	Limit int
	// Cursor is an opaque "list older than X" pagination key — the
	// `created` timestamp of the last row from the previous page.
	// Zero = first page.
	Cursor time.Time
}

// List returns rows matching f, sorted newest-first. Truncates to
// Limit (default 100, max 1000).
func (s *Store) List(ctx context.Context, f ListFilter) ([]*Record, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 1000 {
		f.Limit = 1000
	}

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

	if f.Level != "" {
		// Level rank ordering: debug < info < warn < error.
		// SQL: filter to >= the requested level via a CASE.
		clauses = append(clauses, fmt.Sprintf(
			`CASE level WHEN 'debug' THEN 0 WHEN 'info' THEN 1 WHEN 'warn' THEN 2 WHEN 'error' THEN 3 ELSE 4 END
            >= CASE %s WHEN 'debug' THEN 0 WHEN 'info' THEN 1 WHEN 'warn' THEN 2 WHEN 'error' THEN 3 ELSE 4 END`,
			addArg(f.Level)))
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "created >= "+addArg(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "created <= "+addArg(f.Until))
	}
	if f.RequestID != "" {
		clauses = append(clauses, "request_id = "+addArg(f.RequestID))
	}
	if f.UserID != nil {
		clauses = append(clauses, "user_id = "+addArg(*f.UserID))
	}
	if f.Search != "" {
		clauses = append(clauses, "message ILIKE "+addArg("%"+f.Search+"%"))
	}
	if !f.Cursor.IsZero() {
		clauses = append(clauses, "created < "+addArg(f.Cursor))
	}

	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}

	q := `SELECT id, level, message, attrs, source, request_id, user_id, created
            FROM _logs` + where + ` ORDER BY created DESC LIMIT ` + fmt.Sprintf("%d", f.Limit)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("logs: list: %w", err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		var r Record
		var attrsBytes []byte
		if err := rows.Scan(&r.ID, &r.Level, &r.Message, &attrsBytes, &r.Source, &r.RequestID, &r.UserID, &r.Created); err != nil {
			return nil, fmt.Errorf("logs: scan: %w", err)
		}
		if len(attrsBytes) > 0 {
			_ = json.Unmarshal(attrsBytes, &r.Attrs)
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// Count returns the total row count matching f (without LIMIT).
// Used by the admin endpoint for "total rows" header.
func (s *Store) Count(ctx context.Context, f ListFilter) (int64, error) {
	// Reuse the WHERE clause from List by building it the same way.
	// Quick + dirty: re-implement to avoid threading an internal
	// helper. Same algorithm.
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
	if f.Level != "" {
		clauses = append(clauses, fmt.Sprintf(
			`CASE level WHEN 'debug' THEN 0 WHEN 'info' THEN 1 WHEN 'warn' THEN 2 WHEN 'error' THEN 3 ELSE 4 END
            >= CASE %s WHEN 'debug' THEN 0 WHEN 'info' THEN 1 WHEN 'warn' THEN 2 WHEN 'error' THEN 3 ELSE 4 END`,
			addArg(f.Level)))
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "created >= "+addArg(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "created <= "+addArg(f.Until))
	}
	if f.RequestID != "" {
		clauses = append(clauses, "request_id = "+addArg(f.RequestID))
	}
	if f.UserID != nil {
		clauses = append(clauses, "user_id = "+addArg(*f.UserID))
	}
	if f.Search != "" {
		clauses = append(clauses, "message ILIKE "+addArg("%"+f.Search+"%"))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	q := `SELECT count(*) FROM _logs` + where
	var c int64
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&c); err != nil {
		return 0, fmt.Errorf("logs: count: %w", err)
	}
	return c, nil
}

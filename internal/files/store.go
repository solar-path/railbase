package files

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the minimal pgx subset Store needs. Matches the pattern
// used by every other store/* in the codebase so tests can plug in
// nopPool / mock connections without dragging a running Postgres in.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Store persists file metadata to the `_files` table.
type Store struct {
	q Querier
}

// NewStore constructs a Store atop any Querier (pgxpool.Pool, pgx.Tx).
func NewStore(q Querier) *Store { return &Store{q: q} }

// Insert persists a new file row. Returns the assigned ID + created_at.
func (s *Store) Insert(ctx context.Context, f *File) error {
	if f == nil {
		return errors.New("files: Insert nil file")
	}
	if f.Collection == "" || f.Filename == "" || f.MIME == "" || f.StorageKey == "" {
		return errors.New("files: Insert missing required fields")
	}
	if f.ID == uuid.Nil {
		f.ID = uuid.Must(uuid.NewV7())
	}
	var ownerUser any
	if f.OwnerUser != nil {
		ownerUser = *f.OwnerUser
	}
	var tenantID any
	if f.TenantID != nil {
		tenantID = *f.TenantID
	}
	err := s.q.QueryRow(ctx, `
		INSERT INTO _files
		    (id, collection, record_id, field, owner_user, tenant_id,
		     filename, mime, size, sha256, storage_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING created_at`,
		f.ID, f.Collection, f.RecordID, f.Field, ownerUser, tenantID,
		f.Filename, f.MIME, f.Size, f.SHA256, f.StorageKey,
	).Scan(&f.CreatedAt)
	if err != nil {
		return fmt.Errorf("files: insert: %w", err)
	}
	return nil
}

// GetByKey looks up a file by (collection, record_id, field, filename).
// Used by the download handler which receives URL params in that shape.
// Returns ErrNotFound when no row matches.
func (s *Store) GetByKey(ctx context.Context, collection string, recordID uuid.UUID, field, filename string) (*File, error) {
	row := s.q.QueryRow(ctx, `
		SELECT id, collection, record_id, field, owner_user, tenant_id,
		       filename, mime, size, sha256, storage_key, created_at
		FROM _files
		WHERE collection = $1 AND record_id = $2 AND field = $3 AND filename = $4
		ORDER BY created_at DESC
		LIMIT 1`,
		collection, recordID, field, filename,
	)
	return scanFile(row)
}

// GetByID looks up by primary key. Used by admin / delete paths.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*File, error) {
	row := s.q.QueryRow(ctx, `
		SELECT id, collection, record_id, field, owner_user, tenant_id,
		       filename, mime, size, sha256, storage_key, created_at
		FROM _files
		WHERE id = $1`,
		id,
	)
	return scanFile(row)
}

// Delete removes the metadata row. Caller is responsible for deleting
// the underlying blob via Driver.Delete.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.q.Exec(ctx, `DELETE FROM _files WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// scanFile decodes a SELECT row into File. Shared between GetByKey
// and GetByID so the column order stays canonical.
func scanFile(row pgx.Row) (*File, error) {
	var f File
	var ownerUser, tenantID *uuid.UUID
	err := row.Scan(&f.ID, &f.Collection, &f.RecordID, &f.Field,
		&ownerUser, &tenantID, &f.Filename, &f.MIME, &f.Size,
		&f.SHA256, &f.StorageKey, &f.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	f.OwnerUser = ownerUser
	f.TenantID = tenantID
	return &f, nil
}

// HashingReader wraps a reader and computes a running SHA-256 +
// size. Used by upload handlers so a single io.Copy through the
// driver fills in both metadata columns.
type HashingReader struct {
	r    io.Reader
	h    hash.Hash
	size int64
}

// NewHashingReader wraps r.
func NewHashingReader(r io.Reader) *HashingReader {
	return &HashingReader{r: r, h: sha256.New()}
}

// Read consumes from the underlying reader and feeds the hasher.
func (h *HashingReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	if n > 0 {
		h.h.Write(p[:n])
		h.size += int64(n)
	}
	return n, err
}

// Sum returns the finished SHA-256 digest. Call once, after the
// upload reader is exhausted.
func (h *HashingReader) Sum() []byte { return h.h.Sum(nil) }

// Size reports the bytes seen so far.
func (h *HashingReader) Size() int64 { return h.size }

// Stamp returns the current UTC time truncated to microseconds —
// matches the audit-log convention so created_at round-trips through
// PG TIMESTAMPTZ losslessly.
func Stamp() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// Package settings is the runtime-mutable configuration store.
//
// docs/14-observability.md "Settings model" defines the four-layer
// precedence:
//
//	defaults  → config.go static defaults
//	config    → railbase.yaml (loaded at boot, immutable at runtime)
//	env       → RAILBASE_* environment variables (immutable at runtime)
//	cli       → flags passed on the command line (immutable at runtime)
//	ui        → admin UI / API writes (this package)
//
// The first four collapse into a `Defaults` struct passed to New;
// the fifth lives in the `_settings` Postgres table this package
// manages. A read consults Postgres first, then Defaults — so any
// admin override transparently shadows the boot configuration.
//
// Settings changes emit `settings.{key}.changed` on the eventbus so
// caches, mailer template hot-reload, etc. can react without polling.
//
// Wire format: keys are lower-case dotted (`site.name`,
// `auth.password_min_len`); values are JSONB so each can carry a
// scalar, an object, or a list without per-key migration. Callers
// type-assert via the typed accessors (GetString, GetInt, …).
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/railbase/railbase/internal/cache"
	"github.com/railbase/railbase/internal/eventbus"
)

// TopicChanged is the eventbus topic published on every Set/Delete.
// The Payload is a Change struct describing what moved.
const TopicChanged = "settings.changed"

// Change is what the eventbus publishes when a setting moves.
type Change struct {
	Key      string
	OldValue any // nil if the key was newly created
	NewValue any // nil if the key was deleted
}

// Manager is the package's main type. Construct via New, hold for
// process lifetime; Manager is goroutine-safe.
type Manager struct {
	pool     *pgxpool.Pool
	bus      *eventbus.Bus
	log      *slog.Logger
	defaults map[string]any

	// Cache holds the JSONB values from the last Postgres read. We
	// invalidate the cache on Set/Delete; reads consult cache first,
	// fall through to Postgres on miss.
	mu    sync.RWMutex
	cache map[string]json.RawMessage

	// v1.7.32 — hit/miss counters expose this cache via the
	// internal/cache.Registry so the admin Cache inspector lists it
	// alongside filter.ast / rbac.resolver / i18n.bundles. Atomic
	// because Get is read-mostly + frequently concurrent.
	hits   atomic.Int64
	misses atomic.Int64
}

// Stats / Clear satisfy `cache.StatsProvider` so the v1.7.24b admin
// Cache inspector renders settings cache state. The fields here are
// a subset of cache.Stats — Loads/LoadFails/Evictions are zero
// because the settings cache is hand-rolled (no LRU eviction, no
// singleflight loader). Operators care primarily about size + hit
// rate.
func (m *Manager) Stats() cache.Stats {
	m.mu.RLock()
	size := len(m.cache)
	m.mu.RUnlock()
	return cache.Stats{
		Hits:   m.hits.Load(),
		Misses: m.misses.Load(),
		Size:   size,
	}
}

// Clear drops every cached entry + zeros the hit/miss counters. The
// admin UI's "Clear" button on the Cache inspector lands here.
// Subsequent Gets re-read from Postgres on first access; defaults
// continue to serve as the third-tier fallback.
func (m *Manager) Clear() {
	m.mu.Lock()
	m.cache = map[string]json.RawMessage{}
	m.mu.Unlock()
	m.hits.Store(0)
	m.misses.Store(0)
}

// Options bundles Manager construction inputs so adding a new field
// later doesn't break callers.
type Options struct {
	Pool     *pgxpool.Pool
	Bus      *eventbus.Bus
	Log      *slog.Logger
	Defaults map[string]any
}

// New constructs a Manager but does NOT pre-populate the cache —
// the first Get for each key triggers a Postgres lookup. Add
// Manager.Preload() if eager warm-up is needed.
func New(opts Options) *Manager {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Defaults == nil {
		opts.Defaults = map[string]any{}
	}
	m := &Manager{
		pool:     opts.Pool,
		bus:      opts.Bus,
		log:      opts.Log,
		defaults: opts.Defaults,
		cache:    map[string]json.RawMessage{},
	}
	// v1.7.32 — surface the settings cache in the admin Cache inspector.
	// Register on construction so a process with no Manager (atypical;
	// tests, embedded callers) doesn't show a phantom entry.
	cache.Register("settings", m)
	return m
}

// Get returns the JSON-decoded value for key. Order:
//
//  1. cached Postgres value (after first read)
//  2. Postgres `_settings` lookup → cache it
//  3. Defaults map
//
// Returns (nil, false) when the key isn't set anywhere.
func (m *Manager) Get(ctx context.Context, key string) (any, bool, error) {
	m.mu.RLock()
	if raw, ok := m.cache[key]; ok {
		m.mu.RUnlock()
		m.hits.Add(1)
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, fmt.Errorf("settings: decode %q: %w", key, err)
		}
		return v, true, nil
	}
	m.mu.RUnlock()
	m.misses.Add(1)

	var raw []byte
	err := m.pool.QueryRow(ctx,
		`SELECT value FROM _settings WHERE key = $1`, key).Scan(&raw)
	switch {
	case err == nil:
		m.mu.Lock()
		m.cache[key] = json.RawMessage(raw)
		m.mu.Unlock()
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, fmt.Errorf("settings: decode %q: %w", key, err)
		}
		return v, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		// Fall through to defaults below.
	default:
		return nil, false, fmt.Errorf("settings: read %q: %w", key, err)
	}

	if v, ok := m.defaults[key]; ok {
		return v, true, nil
	}
	return nil, false, nil
}

// GetString is sugar for `Get` + cast. Returns ("", false, nil) when
// the key is absent or non-string. Caller decides whether to treat
// that as an error.
func (m *Manager) GetString(ctx context.Context, key string) (string, bool, error) {
	v, ok, err := m.Get(ctx, key)
	if err != nil || !ok {
		return "", ok, err
	}
	s, ok := v.(string)
	return s, ok, nil
}

// GetInt returns the numeric value as int64. JSON numbers always
// come back as float64; we narrow to int64 here, rejecting fractional
// values so callers don't get surprise truncation.
func (m *Manager) GetInt(ctx context.Context, key string) (int64, bool, error) {
	v, ok, err := m.Get(ctx, key)
	if err != nil || !ok {
		return 0, ok, err
	}
	switch n := v.(type) {
	case float64:
		if n != float64(int64(n)) {
			return 0, false, fmt.Errorf("settings: %q is not an integer (%v)", key, n)
		}
		return int64(n), true, nil
	case int:
		return int64(n), true, nil
	case int64:
		return n, true, nil
	}
	return 0, false, fmt.Errorf("settings: %q is not numeric (%T)", key, v)
}

// GetBool returns the boolean value.
func (m *Manager) GetBool(ctx context.Context, key string) (bool, bool, error) {
	v, ok, err := m.Get(ctx, key)
	if err != nil || !ok {
		return false, ok, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, false, fmt.Errorf("settings: %q is not bool (%T)", key, v)
	}
	return b, true, nil
}

// Set upserts the JSON-encoded value into `_settings` and invalidates
// the cache. Publishes Change on the eventbus so subscribers can
// react.
func (m *Manager) Set(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("settings: encode %q: %w", key, err)
	}
	// Read the previous value so the eventbus payload carries it —
	// audit subscribers can record both states.
	prev, _, _ := m.Get(ctx, key)

	if _, err := m.pool.Exec(ctx, `
        INSERT INTO _settings (key, value, updated_at)
        VALUES ($1, $2, now())
        ON CONFLICT (key) DO UPDATE
            SET value = EXCLUDED.value, updated_at = now()
    `, key, raw); err != nil {
		return fmt.Errorf("settings: write %q: %w", key, err)
	}

	m.mu.Lock()
	m.cache[key] = json.RawMessage(raw)
	m.mu.Unlock()

	if m.bus != nil {
		m.bus.Publish(eventbus.Event{
			Topic:   TopicChanged,
			Payload: Change{Key: key, OldValue: prev, NewValue: value},
		})
	}
	return nil
}

// Delete removes the key from `_settings`. Subsequent reads fall
// back to defaults. Publishes Change with NewValue=nil.
func (m *Manager) Delete(ctx context.Context, key string) error {
	prev, _, _ := m.Get(ctx, key)

	if _, err := m.pool.Exec(ctx, `DELETE FROM _settings WHERE key = $1`, key); err != nil {
		return fmt.Errorf("settings: delete %q: %w", key, err)
	}
	m.mu.Lock()
	delete(m.cache, key)
	m.mu.Unlock()

	if m.bus != nil {
		m.bus.Publish(eventbus.Event{
			Topic:   TopicChanged,
			Payload: Change{Key: key, OldValue: prev, NewValue: nil},
		})
	}
	return nil
}

// List returns every key currently set in Postgres plus every key in
// Defaults that hasn't been overridden. Useful for `railbase config
// list` and the admin UI settings panel.
//
// Values are returned decoded (any) so the caller can render or
// re-encode as needed.
func (m *Manager) List(ctx context.Context) (map[string]any, error) {
	rows, err := m.pool.Query(ctx, `SELECT key, value FROM _settings`)
	if err != nil {
		return nil, fmt.Errorf("settings: list: %w", err)
	}
	defer rows.Close()

	out := map[string]any{}
	for k, v := range m.defaults {
		out[k] = v
	}
	for rows.Next() {
		var k string
		var raw []byte
		if err := rows.Scan(&k, &raw); err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("settings: decode %q: %w", k, err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

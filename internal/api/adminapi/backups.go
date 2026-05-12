package adminapi

// v1.7.7 §3.11 deferred — admin endpoints for browsing and triggering
// database backup archives. Companion to the `railbase backup` CLI
// (pkg/railbase/cli/backup.go): same on-disk layout
// (<DataDir>/backups/*.tar.gz), same Manifest shape, same producer
// (internal/backup.Backup). The admin UI is the read-only "what
// backups do we have" pane plus a "create one now" button.
//
// Scope: deliberately NO restore endpoint. Restoring from an HTTP
// surface is too dangerous in v1; operators use the CLI for that
// (railbase backup restore <archive> --force). The spec explicitly
// pins this constraint.
//
// DataDir: not on Deps yet. We mirror the CLI's loadConfigOnly path
// (RAILBASE_DATA_DIR env with `pb_data` fallback) so this slice doesn't
// have to widen the Deps surface. If a future slice needs DataDir on
// Deps for other reasons it can land additively; for backups alone
// the env read is sufficient and matches the CLI exactly.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/railbase/railbase/internal/backup"
	"github.com/railbase/railbase/internal/buildinfo"
	rerr "github.com/railbase/railbase/internal/errors"
)

// backupItem is the response shape for one archive in the listing.
// `path` is the relative path from DataDir (e.g. "backups/backup-
// 20260511-180000.tar.gz") so the UI can reference an archive without
// the absolute path leaking into the browser. `created` is the file's
// modification time in RFC3339 — the manifest's CreatedAt would be
// more authoritative but reading every manifest on a list call would
// force decompressing N archives. The mtime is set when the file was
// written, which is close enough for the UI's "2 hours ago" label.
type backupItem struct {
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_bytes"`
	Created   time.Time `json:"created"`
	Path      string    `json:"path"`
}

// backupManifestSummary trims internal/backup.Manifest to the few
// fields the create response actually needs. Tables_count + rows_count
// + schema_head let the UI render the success banner without
// shipping a full table list (which can be large + isn't needed for
// the listing flow).
type backupManifestSummary struct {
	TablesCount int    `json:"tables_count"`
	RowsCount   int64  `json:"rows_count"`
	SchemaHead  string `json:"schema_head"`
}

// dataDirFromEnv mirrors pkg/railbase/cli/backup.go's loadConfigOnly:
// RAILBASE_DATA_DIR env, fallback to "pb_data", resolved to an
// absolute path. We keep the two implementations side-by-side rather
// than extract a shared helper — the call sites are tiny and pulling
// a helper across the cli/adminapi boundary would force a package
// dependency that doesn't otherwise exist.
func dataDirFromEnv() (string, error) {
	dir := os.Getenv("RAILBASE_DATA_DIR")
	if dir == "" {
		dir = "pb_data"
	}
	return filepath.Abs(dir)
}

// backupsListHandler — GET /api/_admin/backups.
//
// Reads <DataDir>/backups/ and returns the *.tar.gz entries sorted
// newest-first. Returns `{items: []}` (not 404) when the directory is
// missing — an empty state is normal on a fresh deploy. No pagination:
// operators typically have < 30 daily archives before retention sweeps.
func (d *Deps) backupsListHandler(w http.ResponseWriter, r *http.Request) {
	dataDir, err := dataDirFromEnv()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "resolve data dir"))
		return
	}
	items, err := listBackupItems(dataDir)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "read backups dir"))
		return
	}
	writeBackupsList(w, items)
}

// listBackupItems is the shared scanner for the backups directory.
// Returns a possibly-empty list sorted newest-first; the not-exist
// case maps to an empty list rather than an error so callers don't
// have to special-case fresh deploys. Shared between the backups
// listing handler and the v1.7.x §3.11 health dashboard's backup
// stats collector.
func listBackupItems(dataDir string) ([]backupItem, error) {
	backupsDir := filepath.Join(dataDir, "backups")
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []backupItem{}, nil
		}
		return nil, err
	}
	items := make([]backupItem, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			// Skip stat-failed entries rather than fail the whole list —
			// a single stale dirent shouldn't blank the screen.
			continue
		}
		items = append(items, backupItem{
			Name:      e.Name(),
			SizeBytes: info.Size(),
			Created:   info.ModTime().UTC(),
			Path:      filepath.ToSlash(filepath.Join("backups", e.Name())),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Created.After(items[j].Created)
	})
	return items, nil
}

// writeBackupsList emits the canonical {items: [...]} envelope. We
// keep this in a helper so the empty-dir short-circuit and the
// happy-path both serialize identically.
func writeBackupsList(w http.ResponseWriter, items []backupItem) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items": items,
	})
}

// backupsCreateHandler — POST /api/_admin/backups.
//
// Triggers backup.Backup into a freshly-named file under
// <DataDir>/backups/. Returns 201 with the new item's metadata plus a
// trimmed manifest summary so the UI can flash a "Backup created: N
// tables, M rows" banner.
//
// Production confirm gate: deliberately NOT implemented. An operator
// reaching this endpoint via the admin UI has already authenticated
// and accepted the irreversible-ish nature of writing a snapshot to
// disk; an extra confirm checkbox would be noise, not safety.
func (d *Deps) backupsCreateHandler(w http.ResponseWriter, r *http.Request) {
	if d.Pool == nil {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnavailable, "backups not configured"))
		return
	}

	dataDir, err := dataDirFromEnv()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "resolve data dir"))
		return
	}
	backupsDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "mkdir backups dir"))
		return
	}

	// Same name convention as the CLI's `backup create`:
	// backup-<UTC-stamp>.tar.gz. UTC keeps timezone-suffixed deploys
	// from producing colliding names across regions.
	name := "backup-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	absPath := filepath.Join(backupsDir, name)

	f, err := os.Create(absPath)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "create archive file"))
		return
	}
	// We close f explicitly below so we can stat it after; defer is a
	// safety net for early-return paths.
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	manifest, err := backup.Backup(r.Context(), d.Pool, f, backup.Options{
		RailbaseVersion: buildinfo.String(),
	})
	if err != nil {
		// Remove the (likely half-written) archive so `ls` doesn't show
		// partial files — matches the CLI's behaviour on failure.
		_ = f.Close()
		closed = true
		_ = os.Remove(absPath)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "backup"))
		return
	}
	if err := f.Close(); err != nil {
		closed = true
		_ = os.Remove(absPath)
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "close archive"))
		return
	}
	closed = true

	info, err := os.Stat(absPath)
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "stat archive"))
		return
	}

	var rows int64
	for _, t := range manifest.Tables {
		rows += t.Rows
	}

	item := backupItem{
		Name:      name,
		SizeBytes: info.Size(),
		Created:   info.ModTime().UTC(),
		Path:      filepath.ToSlash(filepath.Join("backups", name)),
	}
	summary := backupManifestSummary{
		TablesCount: len(manifest.Tables),
		RowsCount:   rows,
		SchemaHead:  manifest.MigrationHead,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":       item.Name,
		"size_bytes": item.SizeBytes,
		"created":    item.Created,
		"path":       item.Path,
		"manifest":   summary,
	})

	// Belt-and-braces log so an operator running tail-f on the server
	// logs sees the admin-triggered backup line up with the CLI's.
	if d.Log != nil {
		d.Log.Info("admin backup created",
			"name", item.Name,
			"size_bytes", item.SizeBytes,
			"tables", summary.TablesCount,
			"rows", summary.RowsCount,
			"schema_head", summary.SchemaHead,
		)
	}
}


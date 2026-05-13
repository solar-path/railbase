package adminapi

// v1.7.39 §3.x — first-run "Database configuration" wizard step.
//
// Three PUBLIC endpoints (no RequireAdmin guard):
//
//	GET  /api/_admin/_setup/detect     — list detected local PG sockets +
//	                                     suggested defaults
//	POST /api/_admin/_setup/probe-db   — try a candidate DSN, no migrations
//	POST /api/_admin/_setup/save-db    — persist DSN to <DataDir>/.dsn so
//	                                     the next boot picks it up
//
// Why public: the operator cannot have an admin account yet — bootstrap
// admin creation runs AFTER db config. The security model is:
//
//   - In cold-boot setup mode the server is reachable on its admin port
//     (typically :8095 on localhost). Operator-grade access to the
//     running process is assumed; a hostile observer on the local
//     network would also be able to read pb_data/ directly.
//   - The save endpoint writes to <DataDir>/.dsn at 0600 (operator-owned).
//   - The probe endpoint never persists state and is rate-limited by
//     the package-wide security.Limiter wired in app.go.
//
// Setup mode itself is implicit: when no DSN is provided, config.Load()
// auto-flips to embedded postgres (v1.4.3 "zero-config UX"). The wizard
// runs from that embedded instance; "save and restart" writes .dsn,
// and the next boot bypasses embedded in favour of the persisted DSN.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/railbase/railbase/internal/config"
	"github.com/railbase/railbase/internal/db/embedded"
	rerr "github.com/railbase/railbase/internal/errors"
)

// setupProbeTimeout bounds the connect+SELECT-version round-trip on
// /probe-db and /save-db. 5s is generous for a local socket and a
// reasonable upper bound for an external host on the same VPC; longer
// than that and the operator is staring at the wizard wondering if
// they typed the host wrong.
const setupProbeTimeout = 5 * time.Second

// setupDSNFilename is the file under DataDir that holds the persisted
// DSN written by /save-db and read back by config.readPersistedDSN on
// the next boot. Mode 0600; operator-owned.
const setupDSNFilename = ".dsn"

// setupSocketInfo mirrors config.LocalPostgresSocket but with json
// tags shaped for the wizard UI. The admin frontend expects snake_case
// keys throughout — we don't ship the raw config struct because that
// commits to Go-shape field names in the wire contract.
type setupSocketInfo struct {
	Dir    string `json:"dir"`
	Path   string `json:"path"`
	Distro string `json:"distro"`
}

// setupDetectResponse is the GET /_setup/detect envelope.
type setupDetectResponse struct {
	Configured        bool              `json:"configured"`
	CurrentMode       string            `json:"current_mode"`
	Sockets           []setupSocketInfo `json:"sockets"`
	SuggestedUsername string            `json:"suggested_username"`
}

// setupDBBody is the request body for /probe-db AND /save-db. Save adds
// CreateDatabase; the probe handler ignores it.
type setupDBBody struct {
	Driver      string `json:"driver"`         // local-socket | external | embedded
	SocketDir   string `json:"socket_dir"`     // when driver=local-socket
	Username    string `json:"username"`       // when driver=local-socket
	Password    string `json:"password"`       // optional (trust/peer auth)
	Database    string `json:"database"`       // when driver=local-socket
	SSLMode     string `json:"sslmode"`        // when driver=local-socket
	ExternalDSN string `json:"external_dsn"`   // when driver=external
	// CreateDatabase, when true, triggers /save-db to CREATE DATABASE
	// against the postgres admin db on the same server if the target
	// db doesn't already exist. Ignored by /probe-db.
	CreateDatabase bool `json:"create_database"`
}

// setupProbeResponse is the success envelope for /probe-db. On
// failure the same fields appear with ok=false; the wizard renders
// `hint` inline so the operator can self-correct.
//
// v1.7.42 added the foreign-DB safety pair:
//
//   - PublicTableCount: count of non-system tables in `public` schema.
//     0 = pristine; >0 = some content. We don't peek at row counts —
//     just table presence — because that's cheap (pg_tables is a view
//     over pg_class) and sufficient for the UX gate.
//   - IsExistingRailbase: true iff `_migrations` is among those tables.
//     If true, the DB is either an existing Railbase install OR a
//     hostile DB where someone manually created a `_migrations` table.
//     We trust the marker — collision is implausible enough that we
//     pay the price (Liquibase / Alembic both do the same).
//
// The UI uses these to decide between "green: existing Railbase" /
// "yellow: foreign non-empty DB, click to proceed anyway" / "neutral:
// empty DB, continue normally".
type setupProbeResponse struct {
	OK                 bool   `json:"ok"`
	DSN                string `json:"dsn,omitempty"`
	Version            string `json:"version,omitempty"`
	DBExists           bool   `json:"db_exists,omitempty"`
	CanCreateDB        bool   `json:"can_create_db,omitempty"`
	PublicTableCount   int    `json:"public_table_count"`
	IsExistingRailbase bool   `json:"is_existing_railbase"`
	Error              string `json:"error,omitempty"`
	Hint               string `json:"hint,omitempty"`
}

// setupSaveResponse is the envelope for /save-db on success. We do
// NOT auto-restart — restarting the Go process mid-request is fragile
// and the operator's terminal is the right place to Ctrl-C; restart.
type setupSaveResponse struct {
	OK              bool   `json:"ok"`
	DSN             string `json:"dsn"`
	RestartRequired bool   `json:"restart_required"`
	Note            string `json:"note"`
}

// mountSetupDB wires the three setup endpoints onto r. PUBLIC: no
// RequireAdmin guard. The caller is expected to mount this OUTSIDE
// the admin-auth (RequireAdmin) sub-group so an unauthenticated
// operator can reach it during cold-boot setup. See setup_db.go
// header for the security model.
//
// Paths are RELATIVE — when called against the
// /api/_admin Route group inside Deps.Mount() they resolve to:
//
//	GET  /api/_admin/_setup/detect
//	POST /api/_admin/_setup/probe-db
//	POST /api/_admin/_setup/save-db
//
// Production wiring lives in adminapi.go's Deps.Mount() alongside the
// existing public bootstrap endpoints (BEFORE the RequireAdmin group).
// Tests can also mount against a bare chi.Router; the same relative
// shapes show up at /_setup/detect etc. without the prefix.
func (d *Deps) mountSetupDB(r chi.Router) {
	r.Get("/_setup/detect", d.setupDetectHandler)
	r.Post("/_setup/probe-db", d.setupProbeDBHandler)
	r.Post("/_setup/save-db", d.setupSaveDBHandler)
}

// MountSetupOnly registers JUST the wizard endpoints, without any
// dependency on Deps.Pool / Audit / Sessions / etc. Used by the
// setup-mode boot path in pkg/railbase/app.go (production binary booted
// with no DSN + no embed_pg). The three setup handlers (`detect`,
// `probe-db`, `save-db`) pull data from env vars + filesystem +
// short-lived `pgx.Connect` only, so a bare `&Deps{Log: log}` is
// enough to wire them.
//
// Public counterpart of mountSetupDB so callers outside this package
// can compose it onto their own routers without going through the full
// Mount() that requires a populated Deps.
//
// Also wires a stub `/_bootstrap` GET that the admin SPA probes on
// first paint (`/_/`). The real bootstrap handler requires `d.Admins`
// (nil in setup-mode); the stub returns `{needsBootstrap: true,
// currentMode: "setup"}` so the SPA opens its BootstrapScreen — which
// internally calls `/_setup/detect` and switches to the database-config
// step when current_mode == "setup". POSTing to `/_bootstrap` in
// setup-mode is refused with a typed 409 so a misclick on the admin-
// creation form (which is impossible to reach normally — the wizard
// blocks the admin step) doesn't crash silently.
func (d *Deps) MountSetupOnly(r chi.Router) {
	r.Get("/_bootstrap", setupBootstrapStubHandler)
	r.Post("/_bootstrap", setupBootstrapRefuseHandler)
	d.mountSetupDB(r)
	// v1.7.43 — mailer-setup endpoints are public-by-design (no admin yet).
	// In setup-mode the Settings manager isn't wired (no DB connection),
	// so /mailer-save / /mailer-skip nil-guard early; the operator must
	// finish DB setup first which kicks the process back into normal-boot
	// where Settings IS wired. /mailer-status still works (returns clean
	// state), letting the wizard render the form pre-emptively.
	d.mountSetupMailer(r)
}

// setupBootstrapStubHandler returns the admin-SPA-compatible probe
// shape so the LoginGate component routes to BootstrapScreen. The
// `currentMode` field is a hint — the SPA's BootstrapScreen calls
// `/_setup/detect` anyway for the authoritative state.
func setupBootstrapStubHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"needsBootstrap": true,
		"adminCount":     0,
		"currentMode":    "setup",
		"note":           "Database not configured yet. Complete the setup wizard before creating an admin.",
	})
}

// setupBootstrapRefuseHandler is the POST counterpart — explicit 409
// so a stray admin-creation submit in setup-mode produces a clear
// "database first" error rather than a confusing 500.
func setupBootstrapRefuseHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write([]byte(`{"code":"setup_required","message":"Database is not configured. Run the setup wizard at /_/ first, then restart railbase."}` + "\n"))
}

// setupDetectHandler — GET /api/_admin/_setup/detect.
//
// Reports the current boot mode + any detected local PG sockets + a
// suggested username pulled from $USER. The wizard pre-fills its
// fields from this response.
//
// Configured=true means a `.dsn` file already exists under DataDir,
// i.e. the operator has previously completed db-setup and is just
// viewing the wizard for a re-configuration. We surface this so the
// wizard can render a "Database is already configured — running on
// real PostgreSQL" notice instead of the cold-boot intro.
func (d *Deps) setupDetectHandler(w http.ResponseWriter, _ *http.Request) {
	dataDir, err := dataDirFromEnv()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "resolve data dir"))
		return
	}

	resp := setupDetectResponse{
		CurrentMode:       setupCurrentMode(dataDir),
		Sockets:           []setupSocketInfo{},
		SuggestedUsername: os.Getenv("USER"),
	}
	resp.Configured = resp.CurrentMode == "external"

	for _, s := range config.DetectLocalPostgresSockets() {
		resp.Sockets = append(resp.Sockets, setupSocketInfo{
			Dir:    s.Dir,
			Path:   s.Path,
			Distro: s.Distro,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// setupCurrentMode infers the active DB mode for the running process.
// We can't introspect the actual c.EmbedPostgres flag here (Deps
// doesn't carry it), but the same .dsn file the wizard writes is also
// what config.Load() reads on the NEXT boot — so its presence is the
// canonical "already configured" signal.
//
//	external     — <DataDir>/.dsn exists and is non-empty
//	embedded     — no .dsn AND embed_pg is compiled in (dev build,
//	               zero-config UX active)
//	setup        — no .dsn AND embed_pg NOT compiled in (production
//	               binary on its first boot — the wizard is the only
//	               surface that works; everything else 503s)
//	unconfigured — DataDir itself can't be resolved (shouldn't happen)
//
// The frontend wizard branches on this: "embedded" renders the cold-
// boot intro with a note that data lives in a throwaway dev cluster;
// "setup" renders the same intro but flags that nothing else will
// work until DB config is saved + the process restarted.
func setupCurrentMode(dataDir string) string {
	if dataDir == "" {
		return "unconfigured"
	}
	persisted := readPersistedDSNFile(filepath.Join(dataDir, setupDSNFilename))
	if persisted != "" {
		return "external"
	}
	if embedded.Available() {
		return "embedded"
	}
	return "setup"
}

// setupProbeDBHandler — POST /api/_admin/_setup/probe-db.
//
// Validates the body, builds a DSN, attempts a single
// `SELECT version()` round-trip, returns the result. Never writes to
// disk and never applies migrations. Always returns 200 with an
// ok-flagged envelope so the wizard can render the result inline
// without juggling status codes (the 400 case is reserved for body
// validation errors — malformed JSON, missing required fields).
func (d *Deps) setupProbeDBHandler(w http.ResponseWriter, r *http.Request) {
	var body setupDBBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}
	dsn, verr := buildSetupDSN(body)
	if verr != nil {
		rerr.WriteJSON(w, rerr.Wrap(verr, rerr.CodeValidation, "%s", verr.Error()))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), setupProbeTimeout)
	defer cancel()

	resp := probeDSN(ctx, dsn)
	writeJSON(w, http.StatusOK, resp)
}

// setupSaveDBHandler — POST /api/_admin/_setup/save-db.
//
// Body is the same shape as /probe-db PLUS create_database. Steps:
//
//  1. Build + probe the DSN. If the connection fails for any reason
//     OTHER than "database does not exist when create_database=true",
//     bail out with the probe error.
//  2. If create_database=true AND db doesn't exist, connect to the
//     `postgres` admin db on the same server, CREATE DATABASE the
//     target.
//  3. Write the DSN to <DataDir>/.dsn at 0600.
//  4. Return {ok, dsn, restart_required:true, note}.
//
// The wizard explicitly does NOT auto-restart railbase. The operator
// hits Ctrl-C and re-runs `./railbase serve`; the next boot reads
// .dsn via config.Load() and bypasses embedded postgres.
//
// For driver=embedded we still write a marker? No: embedded is the
// DEFAULT when .dsn is absent. The wizard short-circuits the embedded
// path in the frontend; if the operator manages to POST one here we
// just respond ok without writing.
func (d *Deps) setupSaveDBHandler(w http.ResponseWriter, r *http.Request) {
	var body setupDBBody
	if err := decodeJSON(r, &body); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeValidation, "%s", err.Error()))
		return
	}

	// Embedded selection short-circuits: nothing to write, the absence
	// of `.dsn` is exactly the "use embedded" signal. The wizard never
	// SHOULD post here in the embedded case, but we handle it gracefully.
	if body.Driver == "embedded" {
		writeJSON(w, http.StatusOK, setupSaveResponse{
			OK:              true,
			RestartRequired: false,
			Note:            "Embedded postgres remains active. No restart needed.",
		})
		return
	}

	dsn, verr := buildSetupDSN(body)
	if verr != nil {
		rerr.WriteJSON(w, rerr.Wrap(verr, rerr.CodeValidation, "%s", verr.Error()))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), setupProbeTimeout)
	defer cancel()

	probe := probeDSN(ctx, dsn)
	if !probe.OK {
		// Probe failed. If the failure was "database does not exist"
		// AND create_database=true, fall through to the CREATE-DATABASE
		// path; otherwise bail with the probe error verbatim so the
		// operator sees the same message they'd have seen on the probe
		// tab.
		if !(body.CreateDatabase && strings.Contains(probe.Error, "does not exist")) {
			writeJSON(w, http.StatusOK, setupSaveResponse{
				OK:   false,
				DSN:  dsn,
				Note: probe.Error,
			})
			return
		}
		// CREATE DATABASE: connect to the `postgres` admin db on the
		// SAME server with the SAME credentials.
		if err := createDatabaseFromDSN(ctx, dsn, body.Database); err != nil {
			writeJSON(w, http.StatusOK, setupSaveResponse{
				OK:   false,
				DSN:  dsn,
				Note: fmt.Sprintf("create database failed: %v", err),
			})
			return
		}
		// Re-probe — the target db now exists; this also confirms our
		// credentials can connect to it (not just to `postgres`).
		probe = probeDSN(ctx, dsn)
		if !probe.OK {
			writeJSON(w, http.StatusOK, setupSaveResponse{
				OK:   false,
				DSN:  dsn,
				Note: "database created but re-probe failed: " + probe.Error,
			})
			return
		}
	}

	dataDir, err := dataDirFromEnv()
	if err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "resolve data dir"))
		return
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "ensure data dir"))
		return
	}
	dsnPath := filepath.Join(dataDir, setupDSNFilename)
	if err := os.WriteFile(dsnPath, []byte(dsn+"\n"), 0o600); err != nil {
		rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "write dsn file"))
		return
	}

	// If running in setup-mode, signal the main Run loop to reload
	// THE SAME PROCESS onto the new DSN. The HTTP response flushes
	// BEFORE the reload kicks in (writeJSON does the write here);
	// the listener tears down a few hundred ms after — the browser
	// gets the JSON, the operator sees the "Reloading..." UI from
	// the frontend, and a window.location.reload() in 2s lands on
	// the now-fully-booted server. No Ctrl-C, no manual restart.
	//
	// Outside setup-mode (regular wizard re-run) the channel is nil
	// and we fall back to "restart manually" which is still correct.
	if d.SetupReload != nil {
		select {
		case d.SetupReload <- dsn:
			writeJSON(w, http.StatusOK, setupSaveResponse{
				OK:              true,
				DSN:             dsn,
				RestartRequired: false,
				Note:            "Configuration saved. Reloading on the new database — refresh this page in a moment.",
			})
			return
		default:
			// Channel full somehow — already triggered? Fall through
			// to the manual-restart path so we still respond ok.
		}
	}

	writeJSON(w, http.StatusOK, setupSaveResponse{
		OK:              true,
		DSN:             dsn,
		RestartRequired: true,
		Note:            "Configuration saved to " + dsnPath + ". Restart railbase to apply: Ctrl-C then `./railbase serve`.",
	})
}

// buildSetupDSN composes a DSN string from the wizard body. Returns
// a validation error when required fields for the chosen driver are
// missing.
//
// For driver=local-socket we use the libpq URL form with the host
// parameter overridden via querystring: `postgres://user@/dbname?host=/tmp&sslmode=...`.
// That's the canonical pgx-friendly way to point at a unix socket
// without the URI parser tripping on the leading slash of the path.
//
// For driver=external we accept the operator's DSN verbatim after
// a sanity check (must start with postgres:// or postgresql://).
func buildSetupDSN(body setupDBBody) (string, error) {
	switch body.Driver {
	case "local-socket":
		if body.SocketDir == "" {
			return "", fmt.Errorf("socket_dir is required for driver=local-socket")
		}
		if body.Username == "" {
			return "", fmt.Errorf("username is required for driver=local-socket")
		}
		db := body.Database
		if db == "" {
			db = "railbase"
		}
		sslmode := body.SSLMode
		if sslmode == "" {
			sslmode = "disable"
		}
		u := &url.URL{
			Scheme: "postgres",
			Path:   "/" + db,
		}
		if body.Password != "" {
			u.User = url.UserPassword(body.Username, body.Password)
		} else {
			u.User = url.User(body.Username)
		}
		q := u.Query()
		q.Set("host", body.SocketDir)
		q.Set("sslmode", sslmode)
		u.RawQuery = q.Encode()
		return u.String(), nil
	case "external":
		dsn := strings.TrimSpace(body.ExternalDSN)
		if dsn == "" {
			return "", fmt.Errorf("external_dsn is required for driver=external")
		}
		if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
			return "", fmt.Errorf("external_dsn must start with postgres:// or postgresql://")
		}
		return dsn, nil
	case "embedded":
		// No DSN — the caller short-circuits before reaching here. We
		// return empty to make accidental misuse a typed validation
		// error rather than a silent fall-through.
		return "", fmt.Errorf("embedded driver does not produce a DSN")
	default:
		return "", fmt.Errorf("driver must be one of: local-socket, external, embedded")
	}
}

// probeDSN performs a single connect + SELECT version() round-trip.
// Returns a populated setupProbeResponse — OK is the only field
// callers should branch on; the rest is the human-readable detail
// the wizard renders. Never panics; ctx-cancellable.
//
// Why we don't reuse db/pool.New: that path applies the production
// connection-pool tunables + a server-version gate that would reject
// PG < 14 with a typed error. Here we want a permissive probe — the
// operator should be told their PG12 is too old via a clear hint, not
// just "can't connect".
func probeDSN(ctx context.Context, dsn string) setupProbeResponse {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return setupProbeResponse{
			OK:    false,
			DSN:   dsn,
			Error: err.Error(),
			Hint:  "DSN looks malformed. Expected postgres://user[:password]@host[:port]/dbname?sslmode=...",
		}
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return setupProbeResponse{
			OK:    false,
			DSN:   dsn,
			Error: err.Error(),
			Hint:  setupProbeHint(err.Error()),
		}
	}
	defer conn.Close(ctx)

	var version string
	if err := conn.QueryRow(ctx, "select version()").Scan(&version); err != nil {
		return setupProbeResponse{
			OK:    false,
			DSN:   dsn,
			Error: err.Error(),
			Hint:  "Connected but SELECT version() failed. The account may lack basic SELECT privileges.",
		}
	}

	// db_exists is implicit on a successful probe — we connected to it.
	// can_create_db is best-effort: query datcreate from pg_roles for
	// the current role. Failure here is non-fatal; we report false.
	canCreate := false
	_ = conn.QueryRow(ctx,
		`select rolcreatedb from pg_roles where rolname = current_user`,
	).Scan(&canCreate)

	// Schema scan: count non-system tables in `public` AND check for
	// our marker table `_migrations`. Combined into one round-trip
	// because both consult pg_tables. Best-effort: failure here leaves
	// the fields at their zero values (count=0, marker=false), which
	// makes the wizard fall back to "treat as empty / proceed normally"
	// — strictly less protective than the scan, but never breaks the
	// happy path.
	publicCount := 0
	isExisting := false
	_ = conn.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_tables WHERE schemaname = 'public')::int AS table_count,
		  EXISTS (
		    SELECT 1 FROM pg_tables
		    WHERE schemaname = 'public' AND tablename = '_migrations'
		  ) AS has_railbase_marker
	`).Scan(&publicCount, &isExisting)

	return setupProbeResponse{
		OK:                 true,
		DSN:                dsn,
		Version:            version,
		DBExists:           true,
		CanCreateDB:        canCreate,
		PublicTableCount:   publicCount,
		IsExistingRailbase: isExisting,
	}
}

// setupProbeHint maps connection-error text to an actionable hint.
// We pattern-match on substrings rather than typed errors because
// pgx wraps both libpq and OS-level errors and the typed surface
// isn't stable across versions.
func setupProbeHint(errMsg string) string {
	low := strings.ToLower(errMsg)
	switch {
	case strings.Contains(low, "does not exist") && strings.Contains(low, "database"):
		return "The database does not exist. Tick \"Create database\" on the save step, or run `createdb <name>` manually."
	case strings.Contains(low, "authentication failed"), strings.Contains(low, "password"):
		return "Authentication failed. Verify the password, or for local sockets check pg_hba.conf for peer/trust auth on this user."
	case strings.Contains(low, "connection refused"):
		return "No server is listening on that address. Is PostgreSQL running? Try `pg_isready -h <host>` from the same machine."
	case strings.Contains(low, "no such file or directory"), strings.Contains(low, "no such host"):
		return "Host or socket path not found. For local sockets, /tmp (Homebrew) and /var/run/postgresql (Debian/Ubuntu) are the common locations."
	case strings.Contains(low, "ssl"), strings.Contains(low, "tls"):
		return "TLS handshake failed. Try sslmode=disable for local connections, or sslmode=require for managed providers like Supabase / Neon."
	case strings.Contains(low, "timeout"), strings.Contains(low, "deadline exceeded"):
		return "Connection timed out. Check firewall rules / VPN reachability between this host and the database."
	}
	return "See the error message above. Common fixes: verify host, port, username, database name, and sslmode."
}

// createDatabaseFromDSN connects to the `postgres` admin db on the
// same server as `dsn` (same credentials) and runs CREATE DATABASE
// for `dbName`. Identifier is quoted defensively so a hostile dbName
// can't smuggle SQL — pgx has no parameterised DDL, so we hand-roll
// the quoting through pgx.Identifier.Sanitize.
//
// Race window: between the existence check on the original DSN and
// the CREATE here, a parallel operator could have created the db. We
// accept that — the CREATE will fail with "already exists", which
// the re-probe then handles transparently.
func createDatabaseFromDSN(ctx context.Context, dsn, dbName string) error {
	if strings.TrimSpace(dbName) == "" {
		return fmt.Errorf("database name is required")
	}
	adminDSN, err := dsnWithDatabase(dsn, "postgres")
	if err != nil {
		return fmt.Errorf("compose admin DSN: %w", err)
	}
	cfg, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		return fmt.Errorf("parse admin DSN: %w", err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect to postgres admin db: %w", err)
	}
	defer conn.Close(ctx)

	quoted := pgx.Identifier{dbName}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		// "already exists" is treated as success by the caller — the
		// race window between probe and create can land us here.
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("CREATE DATABASE %s: %w", quoted, err)
	}
	return nil
}

// dsnWithDatabase returns dsn with its database path rewritten to
// newDB. Preserves user / host / port / query string. Used to flip a
// target-db DSN into a `postgres` admin-db DSN so CREATE DATABASE
// runs against the right session.
func dsnWithDatabase(dsn, newDB string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + newDB
	return u.String(), nil
}

// writeJSON is the small response-writer used across this file. We
// don't reuse rerr.WriteJSON because these are success envelopes;
// rerr is reserved for the error path.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// readPersistedDSNFile is the local twin of config.readPersistedDSN
// for use by setupDetectHandler — adminapi can't import config's
// unexported helper directly. Reads the file, trims whitespace, "" on
// any error.
func readPersistedDSNFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

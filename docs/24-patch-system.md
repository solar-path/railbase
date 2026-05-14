# Patch System

## Context

Railbase ships as a **single self-contained Go binary**: backend, the Vite/Preact
admin SPA (`//go:embed all:dist` in `admin/embed.go`), and system SQL migrations
(`//go:embed *.sql`) are all baked in at compile time. There is no Go `plugin`/`.so`
mechanism — changing compiled Go core requires a full rebuild + redeploy, which on a
production instance risks data loss and misconfiguration.

The goal is the ELMA365 operational experience: **upload a patch through the admin UI,
verify it, apply it, keep working** — no "stop the server / rebuild the container /
roll the infra". The target model:

```
Railbase Core  = immutable trusted kernel (Go binary): execution engine,
                 auth/security boundary, migration orchestrator, storage substrate.
Patches        = mutable runtime surface: collections (SQL migrations), JS hooks,
                 admin SPA — versioned, capability-gated extensions.
Recovery layer = backup + coordinated rollback + startup reconciliation.
```

This is a runtime **extension plane** over an immutable core — closer to Kubernetes
operators / ERP modules / VSCode extensions than to "hot file reloading". We do **not**
attempt live Go hot-patching, binary mutation, or symbol injection.

The architecture already has most of the seams:
- **Backend** — JS hooks runtime (`internal/hooks/`) loads `.js` from `<DataDir>/hooks/`
  with fsnotify hot-reload and atomic route-table swaps.
- **DB** — `migrate.Runner` supports disk-based *user* migrations (version ≥ 1000) from
  the `migrations/` dir, tracked in `_migrations` with SHA-256 drift detection.
- **Frontend** — the one real gap: `admin.Handler` serves only the embedded FS.
- **Backups** — `internal/backup` + `cli/backup.go` do pure-Go pgx COPY dump/restore.

Design decisions: whole-SPA disk override for the frontend; the admin **UI is the
primary operator interface** (CLI is the automation/recovery path); ship the **full**
mechanism in v1; honest framing — **"coordinated rollback", never "atomic/transactional
deploy"**.

### Scope: the mechanism, not the finished platform

This design delivers the **patch mechanism** plus a small *initial* patchable surface.
The "Railbase becomes a platform" end-state — where most changes ship as patches and
core releases are rare and strategic — is a **direction, not a deliverable of this
design**. Its arrival is gated by how fast the `$app` surface and migration
expressiveness grow over subsequent core releases. Expect a ramp: right after this
ships, some changes are patches and many still need core. The framing also comes with
new permanent obligations — `$app` ABI stability, signing-key management, patch-
lifecycle governance — which are accepted costs, not problems to "fix". Do not let that
end-state pull extra scope into v1; the deferred items below stay deferred until there
is concrete need.

## What is runtime-patchable vs. requires a new binary

**Patchable at runtime (the mechanism's scope):**
- DB schema via SQL migrations (additive columns, new tables, indexes, backfills).
- Backend logic through the `$app` JS surface: `routerAdd`,
  `onRecordBefore/After{Create,Update,Delete}`, `cronAdd`, `onRequest`, `dao()`,
  `realtime().publish`, `settings()`, `$export.xlsx/pdf`.
- The entire admin SPA (whole-bundle swap).

**Still requires a new binary:**
- Any compiled Go core change (REST handlers, auth/RBAC, middleware, migrate runner,
  schema DSL).
- New `$app` bindings (capabilities are Go in `internal/hooks/loader.go`).
- New *system* migrations (version < 1000) — including the `_patches` table this design
  introduces, and the new hooks/maintenance-mode/embed code. **The mechanism bootstraps
  with exactly one binary deploy**, then the team patches at runtime until they need
  new Go core or new `$app` surface.

## Core vs. patch — decision guide

The rule is a **containment test**: a change can ship as a patch if and only if it fits
entirely inside the existing runtime surface.

> **Patch = configuration of the platform. Core = evolution of the platform.**

**Ship as a PATCH** — the platform already knows *how*:
- DB: a forward SQL migration (new tables/columns/indexes, backfills).
- Backend: expressible through the existing `$app` API (`routerAdd`, record hooks,
  `cronAdd`, `onRequest`, `dao()`, `realtime().publish`, `settings()`, `$export.*`).
- Frontend: any admin SPA change (whole-bundle swap).
- Examples: new dashboard, new cron, new collection hooks, a new admin screen, a new
  integration expressible in goja, new reports.

**Ship as CORE (new binary)** — the platform does not yet know *how*:
- A new `$app` binding, a new core REST handler, an auth/RBAC/middleware change.
- A change to the migrate runner, schema DSL, storage engine, or lifecycle semantics.
- A new *system* migration (version < 1000).
- Anything goja can't express well (async/await, modules, concurrency, native perf).
- The patch mechanism's own code.

**You don't decide by guessing — the system answers.** A patch manifest declares
`requires_api_surface` + `api_surface_revision` and `min/max_binary_tag`; `patch verify`
rejects a bundle the running binary can't satisfy *before* any side effect
(`missing capability: realtime.publish` → that change belongs in a core release). The
gate stops *missing* primitives; it does not stop *abuse* of present ones — architectural
review of the dry-run plan still applies.

**Resulting rhythm:** core releases are infrequent and **expand the capability surface**
(publishing new extension points); patches are frequent and **consume** it. When a patch
needs surface the core lacks, that is the signal to schedule a core release.

## Patch bundle format

A signed `.tar.gz` (mirrors the existing backup archive shape):

```
patch-<id>/
  manifest.json
  manifest.sig          Ed25519 signature over manifest.json + checksums.txt
  checksums.txt         sha256 per file
  migrations/           NNNN_<slug>.up.sql  (+ optional NNNN_<slug>.down.sql)
  hooks/                *.js  (mirrors <DataDir>/hooks/ layout)
  hooks_removed.txt     paths of hook files to delete
  frontend/             full Vite dist/ tree, OR absent
```

`manifest.json` — a **rich declarative descriptor** (the artifacts themselves are
imperative; the manifest describes and gates them):

```json
{
  "patch_id": "...", "patch_version": 1042,
  "title": "Sanctions Screening Module",
  "description": "...", "vendor": "...", "release_notes": "...",
  "breaking_changes": [], "requires_restart": false,
  "min_binary_tag": "v0.8.0", "max_binary_tag": "",
  "requires_api_surface": ["routerAdd", "cronAdd", "realtime.publish"],
  "api_surface_revision": "<hash of sorted binding names + per-binding semver>",
  "migrations": [{ "version": 1042, "hash": "..." }],
  "hooks": ["sanctions/ofac_sync.js"], "hooks_removed": [],
  "frontend": { "mode": "full" },
  "rollback": { "strategy": "backup_restore" },
  "health": { "routes": ["/api/patch-self-test"] }
}
```

Notes:
- **Signing** (`manifest.sig`, Ed25519): checksums verify *integrity*, the signature
  verifies *authenticity*. Without it a filesystem attacker could inject hooks / swap
  the frontend / run arbitrary JS. Trusted-publisher keys configured via a new
  `RAILBASE_PATCH_TRUSTED_KEYS` config option; `--force` / an explicit "allow unsigned"
  flag bypasses for dev.
- **`api_surface_revision`** — a name *plus* semantics contract. A binding name can
  exist while its behaviour changed; the revision hash catches that.
- `depends_on` / `conflicts_with` (patch dependency graph) — **deferred to v1.x**;
  reserve the manifest keys now.

## Components

### 1. `_patches` system migration — `internal/db/migrate/sys/0027_patches.up.sql`
Operational/audit ledger, distinct from `_migrations` (which stays the schema ledger +
drift detector + boot source of truth). Columns:
`patch_version` PK, `patch_id`, `manifest` JSONB, `bundle_hash`, `signed_by`,
`status` (7-state enum below), `backup_path`,
**provenance**: `applied_by`, `applied_from_ip`, `applied_via` (`ui` | `cli`),
`reason`, `ticket`, `created_at`, `applied_at`, `updated_at`.

State machine: `staged → verified → applying → applied`, plus
`rollback_in_progress → rolled_back`, and `failed`. Persisting the in-flight states
(`applying`, `rollback_in_progress`) is what makes crash recovery possible (see §6).

### 2. `internal/patch` package — the reusable core
Shared by the admin API and the CLI. Files:
- `manifest.go` — parse/validate the rich manifest.
- `bundle.go` — open `.tar.gz`, verify `checksums.txt`.
- `signature.go` — Ed25519 verify against trusted keys.
- `capabilities.go` — baked-in binding list + `api_surface_revision`; gate the manifest.
- `verify.go` — fully offline: signature, checksums, semver-gate `buildinfo.Tag` vs
  `min/max_binary_tag`, capability + revision check, goja parse-check every `hooks/*.js`,
  migration-version collision check vs `_migrations` rows + existing files.
- `plan.go` — **dry-run report**: what will change, estimated time, rollback
  availability, backup requirement, compatibility verdict. No side effects.
- `apply.go` — the sequenced apply (see §"Apply sequencing").
- `rollback.go` — coordinated rollback (see §"Rollback").
- `reconcile.go` — startup reconciliation (see §6).

### 3. Transactional hook activation — `internal/hooks`
**The single most important hardening.** Today the loader is "write file → fsnotify →
logs-and-skips on error" — eventual and non-deterministic. A patch needs *all-or-fail*.
Add an explicit staged activation path alongside the watcher:
```go
func (r *Runtime) Activate(dir string) error   // parse + bootstrap + build route
                                               // table + ONLY THEN atomic-swap
```
It parses every `.js`, runs bootstrap, builds the route/cron/onRequest tables, and only
on full success performs the existing `atomic.Pointer` swaps (`loader.go:210/219/230/240`).
On any parse/bootstrap error it returns and swaps nothing. `patch.Apply` calls
`Activate` directly instead of relying on the fsnotify "hope" path; the watcher stays
for dev ergonomics.

### 4. Frontend disk-override seam — `admin/embed.go`
Add alongside `Handler`:
```go
func HandlerWithOverride(prefix, overrideDir string) http.Handler
```
- `overrideDir` empty **or** `overrideDir/index.html` absent → behave exactly as today
  (embedded only). A half-populated dir is **ignored** — this prevents hashed-filename
  breakage (`index.html` references `assets/index-<hash>.js`; a partial override
  mismatches).
- `overrideDir/index.html` present → serve the **entire** SPA from `os.DirFS(overrideDir)`;
  the embedded FS is not consulted at all. `serveIndex` SPA fallback reads the override
  dir too. This makes the asset graph immutable per-patch — no merge FS, no overlay.
- Live pickup: fsnotify watch on the override dir flipping an `atomic.Bool`.
- Wire in `pkg/railbase/app.go` (~line 924, the `Mount("/_", adminui.Handler("/_"))`):
  `overrideDir := filepath.Join(a.cfg.DataDir, "admin_dist")`.
- **Ring retention**: keep timestamped dirs `admin_dist.<RFC3339>/` (last N, default 3,
  or a size cap) — not a single `.bak-`, so repeated patching and rollback chains stay
  safe. Atomic swap = `os.Rename` on the same filesystem.
- Do **not** repurpose `cfg.PublicDir` (a reserved "static site at `/`" seam — different
  concern). Use the dedicated `<DataDir>/admin_dist/` convention.

### 5. Patch maintenance mode — `internal/server` + `pkg/railbase/app.go`
A new `PatchState{ Active atomic.Bool }` and a middleware wired early in the server
chain: while a patch is applying, **reject state-changing requests with 503**, allow
reads, restrict to admin-only. This collapses the catastrophic-rollback window — no
concurrent writes during the migration/swap window means `backup.Restore` loses far
less (ideally nothing). `requires_restart: false` patches still avoid a restart; the
maintenance window is seconds, not a redeploy.

### 6. Startup reconciliation — `internal/patch/reconcile.go`, called from `app.go` boot
On boot, scan `_patches`: any row in `applying` or `rollback_in_progress` means the
instance crashed mid-patch. The reconciler enters a **safe mode** (maintenance mode on,
admin-only), surfaces the incomplete patch in the UI, and recommends rollback rather
than leaving the runtime in an undefined state.

### 7. Admin HTTP API — `internal/api/adminapi/patch.go`
The **primary operator interface**. Thin wrapper over `internal/patch`, mounted in the
existing admin router group (pattern: `internal/api/adminapi/hooks_files.go`; auth via
the existing admin-session middleware). Endpoints:
- `POST /api/_admin/patches/upload` — upload bundle to a staging area → state `staged`.
- `POST /api/_admin/patches/{id}/verify` — run `patch.Verify` → state `verified`.
- `GET  /api/_admin/patches/{id}/plan` — `patch.Plan` dry-run report.
- `POST /api/_admin/patches/{id}/apply` — `patch.Apply`; accepts `reason`/`ticket`;
  records provenance.
- `GET  /api/_admin/patches` — list from `_patches`.
- `POST /api/_admin/patches/{version}/rollback` — `patch.Rollback`.
"Installed vs active" — `verified` patches can sit installed; `apply` activates. (Full
deferred-activation windows: v1.x.)

### 8. Admin UI screen — `admin/src/screens/patches.tsx`
ELMA365-style flow: **upload → dry-run report → apply → continue**. The report screen
shows the `patch.Plan` output:
```
Patch 1042 — Sanctions Screening Module     Compatible: YES
Will change:  ✓ DB schema (1 migration: sanctions_hits)
              ✓ 2 hooks   ✓ frontend bundle
Estimated time: ~15s   Backup required: YES   Rollback available: YES
Breaking changes: none   Requires restart: no
```
Plus a history list (version / title / status / applied_by / reason) with a rollback
action. Register in `admin/src/app.tsx` and `admin/src/layout/command_palette.tsx`
(mirror `auth_methods.tsx` / `mailer_config.tsx`).

### 9. `railbase patch` CLI — `pkg/railbase/cli/patch.go`
Automation + recovery path (works when the HTTP API is degraded). Registered in
`cli/root.go` next to `newBackupCmd()`. Subcommands `verify` / `plan` / `apply` /
`list` / `rollback`, flags `--dry-run`, `--force` (bypass signature/version gate, dev).
A future `patch init` / `patch build` Patch-SDK is **deferred to v1.x**.

## Apply sequencing (`patch.Apply`)

```
0.  state staged → verified (verify: signature, checksums, version gate,
    capability+revision check, goja parse-check, migration collision check)  [offline]
1.  state → applying ; ENTER MAINTENANCE MODE (reject writes, admin-only)
2.  open DB, apply system migrations, take pg_advisory_lock for the window
3.  BACKUP: backup.Backup() → <DataDir>/backups/pre-patch-<ver>.tar.gz
    + tar-snapshot the existing <DataDir>/hooks/ tree
4.  copy migrations/*.up.sql (+ .down.sql) → migrations/ dir  [PERMANENT — required so
    the next boot's drift detection / migrate diff stays consistent]
5.  migrate.Runner.Apply()   [one tx per migration; statement_timeout + lock_timeout
                              set; per-migration progress logged]
6.  frontend: extract → <DataDir>/admin_dist.staging-<ver>/, verify, then
    os.Rename(current → admin_dist.<timestamp>/) + os.Rename(staging → admin_dist)
7.  hooks: write hook files (+ delete hooks_removed), then runtime.Activate() —
    transactional: validates everything, swaps only on full success
8.  HEALTH CHECK: poll /readyz + /healthz + every manifest health.routes entry
    (fail if a route is unavailable or returns != 200)
9.  state → applied ; record provenance ; EXIT MAINTENANCE MODE
10. on ANY failure in 2-8 → coordinated rollback, state → failed
```
Order rationale: migrations first (most likely to fail; the backup covers them), then
frontend (cheapest to revert — a rename), then hooks (transactional activation). Honest
framing: this is **coordinated rollback to a known-good restore point**, not atomic
transactional patching.

## Rollback (`patch.Rollback`)

State → `rollback_in_progress`, then reverse:
1. Frontend — `os.Rename` the retained `admin_dist.<timestamp>/` back.
2. Hooks — restore the pre-patch hooks tar snapshot, `runtime.Activate()`.
3. DB — `backup_restore` (default, the only *guaranteed-safe* path): `backup.Restore`
   the pre-patch archive. `down_sql` strategy is **experimental, off by default, not
   exposed in the UI** — operator-authored, unverified, irreversible-transform risk.
4. State → `rolled_back`; health check; exit maintenance mode.

## Safety

- **No atomicity claims.** Cross-resource (DB + FS + hooks) atomicity is impossible;
  the strategy is coordinated rollback to a known-good restore point. Never market as
  "atomic" / "transactional deploy".
- **Maintenance mode** shrinks the catastrophic window (writes between
  "migrations committed" and "health check passed").
- **Backup mandatory** — `Apply` refuses to proceed if `backup.Backup` fails.
- **Transactional hook activation** — hooks become deterministic, matching DB/frontend.
- **Startup reconciliation** — no undefined runtime state after a mid-patch crash.
- **Signing + capability revision** — authenticity, not just integrity; semantics-aware
  compatibility gating.
- **Long-running migration protections** — `statement_timeout`, `lock_timeout`,
  per-migration progress logging (so the UI doesn't appear to hang → operator panic →
  forced restart mid-apply).
- **JS sandbox hardening** — patch uploads are effectively arbitrary code execution;
  ensure execution timeout, panic containment, and cron isolation in `internal/hooks`
  hold under patched code (audit the existing 5s/500ms watchdogs).
- **Drift** — patch migration files MUST persist permanently into `migrations/`.
- **Concurrency** — `pg_advisory_lock` for the apply window.

## Deferred to v1.x (reserve hooks now, don't build)

Patch dependency graph (`depends_on` / `conflicts_with`); deferred-activation windows;
Patch SDK (`railbase patch init/build`); marketplace-style distribution; richer
`api_surface_revision` derivation.

## Critical files

| File | Change |
|---|---|
| `internal/db/migrate/sys/0027_patches.up.sql` | new — `_patches` ledger (7-state, provenance) |
| `internal/patch/*.go` | new — manifest, bundle, signature, capabilities, verify, plan, apply, rollback, reconcile |
| `internal/hooks/loader.go` (+ `hooks.go`) | new `Runtime.Activate(dir)` — transactional activation |
| `admin/embed.go` | `HandlerWithOverride` + fsnotify watch + ring retention |
| `pkg/railbase/app.go` | wire `<DataDir>/admin_dist`, maintenance-mode middleware, boot reconciliation |
| `internal/server/*.go` | maintenance-mode middleware + `PatchState` |
| `internal/config/config.go` | `RAILBASE_PATCH_TRUSTED_KEYS` |
| `internal/api/adminapi/patch.go` | new — admin HTTP endpoints |
| `admin/src/screens/patches.tsx` | new — operator UI (upload → report → apply → history) |
| `admin/src/app.tsx`, `admin/src/layout/command_palette.tsx` | register the screen |
| `pkg/railbase/cli/patch.go` + `cli/root.go` | new — `railbase patch` CLI |

Reuse: `internal/backup` (`Backup`/`Restore`), `internal/db/migrate` (`Runner`,
`Discover`), `cli/migrate.go` (`discoverAllMigrations`, `userMigrationsDir`),
`internal/buildinfo` (`Tag`), the existing fsnotify pattern in `internal/hooks/loader.go`.

## Verification

1. **Bootstrap**: build the binary once with all new code + `0027_patches`; confirm
   `migrate status` shows it applied and the admin UI has the Patches screen.
2. **Backend-only patch**: signed bundle with one `hooks/*.js` adding a `$app.routerAdd`
   route; upload → verify → plan (review report) → apply via UI; `curl` the route;
   confirm `_patches` row with provenance and that `Activate` (not fsnotify) ran.
3. **DB patch**: bundle with an `NNNN_*.up.sql` (version ≥ 1000); apply; confirm the
   table exists, `_migrations` + `migrations/` dir both updated, drift check clean on
   restart, maintenance mode rejected writes during apply.
4. **Frontend patch**: bundle a full rebuilt `dist/`; apply; hard-reload `/_/`, confirm
   new assets serve from `<DataDir>/admin_dist/`; confirm a timestamped retention dir
   exists; remove the override dir → fallback to the embedded SPA.
5. **Failed health check**: bundle whose `health.routes` 404s; confirm coordinated
   rollback restores DB (backup), hooks (snapshot), frontend (retention dir), state
   `failed`.
6. **Mid-patch crash**: kill the process during `applying`; restart; confirm the
   reconciler enters safe mode and surfaces the incomplete patch.
7. **Signature / version gates**: tampered bundle and one with a too-high
   `min_binary_tag` — confirm `verify` rejects both before any side effect.
8. `make verify-release` (vet + test-race + cross-compile + size budget) stays green.

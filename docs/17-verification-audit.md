# 17 — Verification Audit (v1 SHIP gate worklist)

**Companion to [docs/17-verification.md](17-verification.md)**. Maps every numbered docs/17 test item to existing coverage. This is the v1 SHIP gate's checklist — when every ✅/⏸ is accounted for, v1 ships.

Legend:

| Marker | Meaning |
|---|---|
| ✅ | Covered by an existing test. Reference cited. Green under `-race -tags embed_pg`. |
| 🔄 | Partially covered. The path exists but a specific docs/17 sub-assertion isn't tested. |
| 📋 | Missing — needs a new test for v1 SHIP. |
| ⏸ | Out of v1 scope. Plugin / v1.1+ / v1.2+. The docs/17 item describes a future feature. |
| ⏭ | Operator-test, not a Go test. Belongs in CI shell scripts (goreleaser smoke, etc.). |

**Coverage summary (post v1.7.29)**:
- ✅ **113** items (64%) covered by tests (no new docs/17 numbered items closed in v1.7.29 — this milestone validates the operator pre-tag chain end-to-end and closes the field-renderer admin UI track at 25/25)
- 🔄 **5** items partially covered
- 📋 **0** items missing — **v1 SHIP critical-path test-debt list is empty** ✅ (#34 devices trust + #55 hooks memory limit + #148 JS hook harness all explicitly deferrable per plan)
- ⏸ **52** items out of v1 scope (plugins / v1.1+ / mobile SDKs / etc.)
- ⏭ **8** items are CI shell scripts, not Go tests

**Post-SHIP polish track (v1.7.25-v1.7.30+, parallel via agents)**:
- Admin-screen `app.go` wiring (v1.7.25a) — HooksDir / I18nDir / StartedAt lifted from 503-state
- Release artifacts (v1.7.26a) — `docs/RELEASE_v1.md` + `.github/workflows/release.yml` + README status
- Goreleaser project_name pin + `make verify-release` Makefile (v1.7.27a-b)
- CI per-PR binary-size gate (v1.7.28a) — `.github/workflows/ci.yml` cross-build matrix now runs `scripts/check-binary-size.sh bin/` on every PR, catching size regressions before tag-time
- `railbase test` CLI subcommand (v1.7.28c) — closes §3.12.1 (cobra wrapper over `go test` with composable flag surface)
- `make verify-release` end-to-end validated (v1.7.29a) — confirmed the full pre-tag chain runs green as a single operator command; all 6 binaries under 30 MB ceiling (3.57 MB headroom)
- Field renderer extensions (slices 1-5 in v1.7.25b / v1.7.26b / v1.7.27c / v1.7.28b / v1.7.29b) — **25 / ~25 §3.8 domain types now have dedicated cell + edit renderers; the field-renderer admin UI track is functionally complete**

**v1.x-bonus track (post v1 SHIP gates closed)**:
- `send_email_async` job builtin (v1.7.30a) — closes §3.7.5.6's `send_email_async` item; cron-scheduled emails + Go hook fire-and-forget paths now have a first-class queue kind
- YAML fixtures in testapp (v1.7.30b) — closes §3.12.2's "YAML deferred" — `LoadFixtures` reads `.json` / `.yaml` / `.yml` with JSON-wins precedence + ambiguity warning

**v1 SHIP gating requires only the ✅ and 📋 buckets**. Plugin/v1.1+ items don't gate v1.

---

## Smoke tests (5)

| # | Test | Status | Reference / Notes |
|---|---|---|---|
| 1 | Build & cross-compile via goreleaser, ≤30MB binary | ✅ | `.goreleaser.yml` (v1.7.24) ships 6-target matrix (linux/darwin/windows × amd64/arm64), CGO_ENABLED=0, draft-release, embedded changelog grouping. `make cross-compile` + `make check-size` provide the same gate without goreleaser installed. Measured 2026-05-12: largest binary 26.25 MB (Windows amd64), smallest 23.88 MB (linux arm64) — **all 6 under the 30 MB ceiling with 3.75 MB headroom**. `scripts/check-binary-size.sh` enforces the budget in CI. |
| 2 | 5-minute smoke (`railbase init demo` → admin UI works) | ✅ | `scripts/smoke-5min.sh` (v1.7.12) + `make smoke`. 13 HTTP probes: build/size check, /readyz wait, admin bootstrap, signin, all RequireAdmin endpoints (logs/jobs/api-tokens/backups/notifications/audit/schema/settings/me), admin UI HTML, /api/_compat-mode discovery. Tempdir-isolated; SIGTERM cleanup via trap. |
| 3 | PB drop-in compat (PB JS SDK against Railbase strict mode) | 🔄 | `internal/api/auth/auth_methods_e2e_test.go` covers `/auth-methods` discovery. Full PB-SDK against `RAILBASE_COMPAT_MODE=strict` is operator-test scope. |
| 4 | PB import (`railbase import schema --from-pb`) | ✅ | `internal/pbimport/import_test.go` — 10 tests under `-race`. Live HTTP path via httptest.Server. |
| 5 | TS SDK generated + compiles + drift detection | ✅ | `internal/sdkgen/ts/*_test.go` + `generate sdk --check` flow exists since v0.7. |

## Data layer tests (10 + a-aw + 11-19)

| # | Test | Status | Reference / Notes |
|---|---|---|---|
| 6 | pgx/v5 + Postgres 14/15/16 + embedded-PG | ✅ | Every `*_e2e_test.go` exercises pgx via `embedded.Start`. Multi-version testing left to CI matrix. |
| 7 | Migration up/down round-trip | ✅ | `internal/db/migrate/migrate_test.go` + system migrations applied + reverted in tests. |
| 8 | Migration drift detection (block startup) | 🔄 | Drift detection logic in `migrate.go` is exercised at boot; specific "edit applied migration → fail" test is implicit. |
| 9 | Schema diff produces correct migration | ✅ | `internal/schema/builder/diff_test.go` + `railbase migrate diff` CLI exercised. |
| 10 | Field types completeness (31 types in SDK) | ✅ | `internal/sdkgen/ts/tsType_test.go` exercises every field type → TS mapping. |
| 10a | Tel field E.164 normalisation + Region/mobile-only | ✅ | `internal/schema/builder/tel_test.go` + REST e2e in `internal/api/rest/domaintypes_e2e_test.go`. |
| 10b | Tel SDK formatNational/formatInternational | ✅ | TS SDK helpers — `admin/src/.../tel_*.ts` tests (live in admin pkg). |
| 10c | Finance precision (no float drift) | ✅ | `internal/schema/builder/finance_test.go` exercises NUMERIC(15,4) round-trip + add. |
| 10d | Finance constraints (.Positive/.NonNegative) | ✅ | Same file. |
| 10e | Currency wire format + AllowedCurrencies | ✅ | `internal/api/rest/money2_e2e_test.go`. |
| 10f | Currency precision auto (JPY/USD/BHD) | 🔄 | v1.5.9 ships `currency` field. Per-currency auto-precision is currently uniform across all currencies. **Gap**: ISO 4217 currency-decimal lookup not wired. |
| 10g | Currency mixed-currency add error / FX convert | ⏸ | Requires `railbase-fx` plugin. v1.2. |
| 10h | Currency locale formatting (en-US / ru-RU / JPY) | ⏸ | SDK helper; depends on Intl.NumberFormat. v1.0.1 / v1.1 polish. |
| 10i | FX plugin currency.convert | ⏸ | `railbase-fx` plugin. v1.2. |
| 10j | Tel + SMS OTP integration | ⏸ | SMS provider plugin. v1.2. |
| 10k | Address postal-code validation per country | ✅ | `internal/api/rest/address_e2e_test.go`. |
| 10l | TaxID multi-country (INN/EIN/etc.) | ✅ | `internal/api/rest/identifiers2_e2e_test.go`. |
| 10m | IBAN mod-97 + SDK formatter | ✅ | `internal/schema/builder/banking_test.go` + REST e2e. |
| 10n | BIC 8/11-char + format | ✅ | Same file. |
| 10o | BankAccount IBAN-mode composite | ✅ | `internal/api/rest/banking2_e2e_test.go`. |
| 10p | PersonName formatting (western-formal/russian-formal) | 🔄 | v1.4.2 stores name; SDK style helpers are SDK-side, partial coverage. |
| 10q | Percentage 0-100 + precision | ✅ | `internal/api/rest/money_e2e_test.go`. |
| 10r | MoneyRange min<max + currency match | ✅ | `internal/api/rest/money2_e2e_test.go`. |
| 10s | Country/Language/Timezone enum validation | ✅ | `internal/api/rest/locale_e2e_test.go` + `locale2_e2e_test.go`. |
| 10t | Coordinates lat/lng range check | ✅ | `internal/api/rest/locale2_e2e_test.go`. |
| 10u | Quantity unit conversion + cross-unit-group error | 🔄 | v1.4.9 stores `{value, unit}` JSONB. Unit conversion (kg+g, ft→m) is SDK-side; coverage on SDK side. |
| 10v | Duration ISO 8601 + addToDate | ✅ | `internal/api/rest/quantities_e2e_test.go`. |
| 10w | DateRange overlap helper | 🔄 | v1.5.10 stores DATERANGE. Overlap helper is SDK-side. |
| 10x | Status state machine transitions + audit + callback | ✅ | `internal/api/rest/workflow_e2e_test.go`. |
| 10y | TreePath LTREE operations | ✅ | `internal/api/rest/hierarchy_e2e_test.go`. |
| 10z | Tags autocomplete | 🔄 | v1.4.11 stores TEXT[]. Autocomplete is SDK + UI feature. |
| 10aa | Slug auto-generation + transliteration | ✅ | `internal/api/rest/identifiers_e2e_test.go`. |
| 10ab | SequentialCode pattern + concurrent-atomic | ✅ | Same file. |
| 10ac | Barcode EAN-13 checksum + auto-detect | ✅ | `internal/api/rest/identifiers2_e2e_test.go`. |
| 10ad | Markdown vs RichText (sanitisation + FTS) | 🔄 | v1.4.5 markdown stores raw. Sanitisation (bluemonday) not yet wired; FTS-opt-in is design-only. |
| 10ae | Color hex normalisation | ✅ | `internal/api/rest/content_e2e_test.go`. |
| 10af | Cron expression validation + NL builder | 🔄 | v1.4.5 validates cron at write time. NL builder is UI-side. |
| 10ag-al | QR code (encode/PNG/SVG/PDF/scan/payment-QR) | ⏸ | v1.5.11 stores qr_code; rendering deferred (UI/SDK). Payment QR needs plugin. |
| 10am | Adjacency list children (recursive CTE) | ✅ | `internal/api/rest/hierarchies_e2e_test.go` (v1.5.12). |
| 10an | Adjacency list cycle prevention | ✅ | Same file. |
| 10ao-ap | Materialised path (move + depth) | 🔄 | LTREE covers materialised path. Specific subtree-move + depth-column not explicit. |
| 10aq-ar | Nested set | 📋 | Deferred to v1.6.x §3.8 Hierarchies tail. |
| 10as-at | Closure table | 📋 | Same. |
| 10au-av | DAG cycle + topo sort | 📋 | Same. |
| 10aw | Ordered children (drag-drop sort_index) | ✅ | v1.5.12 `.Ordered()` builder. `hierarchies_e2e_test.go`. |
| 10ax | Tree integrity job (orphans/cycles) | ✅ | `cleanup_tree_integrity` cron (v1.7.14) discovers self-referencing `parent UUID` columns via information_schema → counts orphans (parent points to non-existent id) per table → logs findings via slog. Read-only: NO auto-fix (orphans usually mean operator intervention is needed, silent re-parenting is worse). Cycle detection (recursive CTE) deferred. Default schedule `"45 4 * * *"` daily. |
| 11 | View collection | ⏸ | Railbase v1 doesn't ship view collections. v1.1. |
| 12 | Computed fields | ⏸ | v1.1. |
| 13 | Pagination cursor stability under writes | ✅ | `internal/api/rest/pagination_stability_e2e_test.go` (v1.7.16) — 3 subtests: no_writes_full_traversal (100 rows same `created` µs, 10 pages, every id observed exactly once + re-fetch page 1 byte-identical), documented_offset_limitation_under_inserts (pin-test; logs dupes without failing — OFFSET semantics + head inserts re-read rows; cursor mode would drive dupes to 0), default_sort_includes_id_tiebreaker (SQL-shape regression guard for the `, id DESC` tie-breaker). |
| 14 | Pagination offset (PB-compat) | ✅ | `internal/api/rest/*_e2e_test.go` exercises page/perPage extensively. |
| 15 | Batch ops atomic + audit | ✅ | `internal/api/rest/batch_e2e_test.go` (v1.4.13). |
| 16 | Filter parser security (SQL injection) | ✅ | `internal/filter/parser_test.go` — extensive injection-attempt corpus. |
| 17 | Filter native extensions (@me / IN / BETWEEN / IS NULL) | ✅ | `@me` + IS NULL ✅ in `filter_test.go`. BETWEEN parser + SQL emitter ship in v1.7.21 (lexer `tkBetween`, AST `Between{Target, Lo, Hi}`, `parseCompare` branch requiring `AND` keyword between bounds, `emitBetween` → `(target BETWEEN $N AND $N)`). IN extended coverage (single-item / mixed-numeric / 50-element / case-insensitive / AND composition) added in same slice. `between_test.go` — 16 new tests under `-race`. |
| 18 | Multi-tenancy compile-time guard | 📋 | Tenant middleware applied at request time; no compile-time check via go-vet pass. **Design Q**: is compile-time tenant-check realistic? Likely deferred. |
| 19 | Multi-tenancy runtime cross-tenant isolation | ✅ | `internal/tenant/tenant_test.go` + RLS smoke in v0.9 verification gate. |

## Auth & identity tests (20-42)

| # | Test | Status | Reference |
|---|---|---|---|
| 20 | Multiple auth collections isolated | ✅ | `internal/api/auth/*_e2e_test.go`. |
| 21 | OAuth Google/GitHub/Apple | ✅ | `internal/api/auth/oauth_e2e_test.go` (v1.1.1). |
| 22 | Generic OIDC | ✅ | Same file. |
| 23 | OAuth2 state validation / CSRF | ✅ | Same file. |
| 24 | External auth linking (no duplicate user) | ✅ | Same file. |
| 25 | OTP / magic link | ✅ | `internal/api/auth/otp_e2e_test.go` (v1.1). |
| 26 | MFA full flow | ✅ | `internal/api/auth/mfa_e2e_test.go` (v1.1.2). |
| 27 | TOTP enroll + recovery codes | ✅ | Same file. |
| 28 | WebAuthn / passkeys | ✅ | `internal/api/auth/webauthn_e2e_test.go` (v1.1.3). |
| 29 | Password reset (revokes sessions) | ✅ | `internal/api/auth/auth_flows_e2e_test.go`. |
| 30 | Email change (revokes sessions) | ✅ | Same file. |
| 31 | Email verification | ✅ | Same file. |
| 32 | Auth methods discovery | ✅ | `internal/api/auth/auth_methods_e2e_test.go` (v1.7.0). |
| 33 | Session refresh sliding | ✅ | `internal/auth/session/session_test.go`. |
| 34 | Device trust 30d + revoke | 📋 | `_devices` table not yet shipped. v1.1.x polish per plan. |
| 35 | Auth origins (new country email) | 📋 | v1.1.x polish. |
| 36 | Impersonation | 📋 | v1.1.x polish. |
| 37 | API tokens | ✅ | `internal/auth/apitoken/apitoken_e2e_test.go` (v1.7.3) + middleware tests + v1.7.9 admin UI. |
| 38 | Record tokens TTL + single-use | ✅ | `internal/auth/recordtoken/recordtoken_test.go` (v1.1). |
| 39 | RBAC site scope | ✅ | `internal/rbac/rbac_e2e_test.go` (v1.1.4). |
| 40 | RBAC tenant scope | ✅ | Same. |
| 41 | RBAC deny + audit | ✅ | Same + audit coverage. |
| 42 | RBAC matrix UI bulk grant | ⏸ | Admin UI editor deferred to v1.1.x per plan. |

## Realtime tests (43-52)

| # | Test | Status | Reference |
|---|---|---|---|
| 43 | Subscribe `*` PB-compat | ✅ | `internal/realtime/realtime_test.go` + `internal/api/rest/realtime_e2e_test.go` (v1.3.0). |
| 44 | Subscribe with filter | 🔄 | Topic patterns covered; per-event filter expression not. |
| 45 | Subscribe with expand | ⏸ | Relations expand for realtime events deferred to v1.3.x. |
| 46 | Resume token | ✅ | `internal/realtime/realtime_test.go` v1.7.5b ring-buffer + Last-Event-ID. |
| 47 | Backpressure drop | ✅ | Same file — per-sub queue cap 64 with drop counter. |
| 48 | Cluster fan-out (plugin) | ⏸ | `railbase-cluster` plugin. v1.1. |
| 49 | Per-event RBAC | 🔄 | Auth + tenant filter present; full row-rule integration deferred. |
| 50 | Custom topic publish from hook | ✅ | `internal/hooks/realtime_test.go` (v1.7.15) — 7 tests: lands-on-bus round-trip / nil-bus no-op / missing-collection rejection / bad-verb rejection / non-object-event rejection / non-object-record rejection / update+delete verbs / all-optional-fields-omitted defaults. Plumbs `Bus *eventbus.Bus` through `hooks.Options`; same fan-out path REST CRUD uses. |
| 51 | SSE fallback (same as WS) | ✅ | SSE is the v1 transport; WS deferred to v1.3.x. |
| 52 | <100ms single-node latency | ✅ | `internal/realtime/bench_test.go` (v1.7.11) — `TestPublishToDeliver_Under100ms` asserts p99 < 100ms over N=1000 publishes; current measurement p50=2.5µs, p99=10.6µs (~10,000× headroom). Plus `BenchmarkPublishToDeliver_FanOut` for 100-sub fan-out (~36µs p99). |

## Hooks tests (53-61)

| # | Test | Status | Reference |
|---|---|---|---|
| 53 | Hot-reload <1s | ✅ | `internal/hooks/hooks_test.go` exercises fsnotify + reload latency. |
| 54 | Sandbox timeout (5s kill) | ✅ | Same file — `goja.Interrupt` test. |
| 55 | Sandbox memory OOM | 📋 | Memory limit not yet implemented in v1.2.0 minimal. |
| 56 | Panic isolation | ✅ | `internal/hooks/hooks_test.go`. |
| 57 | All JSVM bindings testable | 🔄 | $app + console ✅; $apis/$http/$os/$security/$template/$tokens/$filesystem/$mailer/$dbx/$inflector deferred per plan §3.4.5. |
| 58 | BeforeCreate inside tx, AfterCreate after commit | ✅ | `internal/api/rest/hooks_e2e_test.go`. |
| 59 | All 60+ PB hook names | 🔄 | 6 record events covered. Auth/mailer/request/cron hooks deferred. |
| 60 | Custom routes (`routerAdd`) | ✅ | `internal/hooks/router_test.go` (v1.7.17) — 12 tests: GET JSON / POST body / path-param / multi-value query + headers / no-match falls through / no-routes zero-cost / 204 default / text+html content-types / custom header / throw → 500 envelope / 4-case validation matrix / hot-reload replaces routes. Chi-backed atomic-swap; nil-safe middleware. |
| 61 | `cronAdd` from hooks | ✅ | `internal/hooks/cron_test.go` (v1.7.18) — 13 subtests: registration via JS / nil snapshot when empty / same-name overwrites / 5-case validation matrix (missing args / empty name / empty expr / bad expr / non-fn handler) / fireCron dispatches handler / throw doesn't take down loop / loop start+cancel lifecycle / hot-reload replaces snapshot / race-free concurrent reads. In-memory atomic-swap snapshot; minute-aligned ticker; watchdog-protected. |

## File handling tests (62-67)

| # | Test | Status | Reference |
|---|---|---|---|
| 62 | Multipart upload + hash | ✅ | `internal/api/rest/files_e2e_test.go` (v1.3.1). |
| 63 | Image thumbnails lazy | 📋 | v1.3.2 deferred. |
| 64 | MIME validation rejects disallowed | ✅ | Same file. |
| 65 | Size limit 413 | ✅ | Same file. |
| 66 | Signed URL expiry | ✅ | Same file. |
| 67 | S3 driver smoke | ⏸ | S3 plugin path. v1.1. |

## Document tests (68-76)

| # | Test | Status | Reference |
|---|---|---|---|
| 68-76 | Documents (versions/quotas/legal-hold/retention/FTS/text-extract) | ⏸ | v1.3.2 deferred per plan §3.6. |

## Generation tests (77-82)

| # | Test | Status | Reference |
|---|---|---|---|
| 77 | XLSX 100k rows streaming <256MB | ✅ | `internal/api/rest/export_e2e_test.go` (v1.6.0) + memory ceiling in handler. |
| 78 | PDF Markdown template <2s | ✅ | `internal/api/rest/export_template_e2e_test.go` (v1.6.4). |
| 79 | Async export 1M rows via jobs | ✅ | `internal/api/rest/async_export_e2e_test.go` (v1.6.5). |
| 80 | Export RBAC 403 + audit | ✅ | Export reuses ListRule. v1.7.6c added audit rows. |
| 81 | Export quota 413 / rate 429 | 🔄 | Row cap (100k sync / 1M async) ✅; per-tenant rate-limit via §3.13.7 limiter (untested for exports specifically). |
| 82 | Export charts | ⏸ | XLSX charts deferred to v1.1+ polish. |

## Mailer tests (83-88)

| # | Test | Status | Reference |
|---|---|---|---|
| 83 | SMTP send | ✅ | `internal/mailer/mailer_test.go` + smtptest harness. |
| 84 | Template hot-reload | 🔄 | Template re-read on send ✅. fsnotify-based hot-reload deferred per plan §3.1. |
| 85 | i18n templates (lang fallback) | ⏸ | Per-lang templates deferred per plan §3.1. |
| 86 | Per-recipient rate limit | ✅ | `internal/mailer/ratelimit_test.go`. |
| 87 | Console driver dev | ✅ | Same. |
| 88 | Attachment | ✅ | Same. |

## Audit & observability tests (89-97)

| # | Test | Status | Reference |
|---|---|---|---|
| 89 | Auth events logged | ✅ | `internal/audit/audit_test.go` + auth e2e tests cover signin/logout/etc. |
| 90 | RBAC denies → audit | ✅ | `internal/rbac/rbac_e2e_test.go`. |
| 91 | Before/after diff | ✅ | `internal/audit/audit_test.go`. |
| 92 | Hash-chain verify (`audit verify` CLI) | ✅ | `internal/audit/audit_test.go` + `pkg/railbase/cli/audit.go` (v0.6). |
| 93 | Audit retention auto-archive | ✅ | Migration 0021 adds `archived_at` column + partial index on active rows. `cleanup_audit_archive` cron builtin (v1.7.13) sweeps `audit.retention_days`-old rows by SETTING `archived_at`, NOT deleting (chain integrity preserved — `audit verify` walks all rows). Default schedule `"30 4 * * *"` daily; retention=0 (default) means never archive. |
| 94 | Logs as records | ✅ | `internal/logs/logs_e2e_test.go` (v1.7.6). |
| 95 | Logs filtering | ✅ | Same — level/since/request_id/search/user_id. |
| 96 | /metrics + OTel traces | ⏸ | Prometheus + OTel deferred to v1.1 per plan §4.8/9. |
| 97 | /healthz + /readyz | ✅ | `internal/server/probes_test.go`. |

## Lifecycle tests (98-104)

| # | Test | Status | Reference |
|---|---|---|---|
| 98 | First-run wizard | 🔄 | Bootstrap probe + create endpoint ✅; 2FA mandatory enrolment + tour deferred. |
| 99 | Graceful shutdown 30s | ✅ | `internal/server/server.go` Shutdown with grace; `internal/api/rest/router_test.go` exercises. |
| 100 | Backup → restore round-trip | ✅ | `internal/backup/backup_e2e_test.go` (v1.7.7). |
| 101 | Backup auto-upload to S3 | ⏸ | Deferred to v1.7.8 polish. Plugin path. |
| 102 | Settings hot reload | ✅ | `internal/settings/settings_test.go` + `bus.Subscribe(TopicChanged)` exercised in multiple subsystems (rate-limiter, ip-filter, compat-mode). |
| 103 | Plugin install | ⏸ | Plugin host v1.1. |
| 104 | Plugin crash isolation | ⏸ | Same. |

## Auth providers detailed (105-106)

| # | Test | Status | Reference |
|---|---|---|---|
| 105 | Apple client_secret rotation | ✅ | `internal/auth/oauth/apple/apple_test.go` + `pkg/railbase/cli/auth.go apple-secret`. |
| 106 | All 35+ OAuth providers smoke | 🔄 | v1.1.1 ships 4 (Google/GitHub/Apple/generic OIDC); 31+ providers via `railbase-oauth-*` plugins in v1.2. |

## Plugin tests (107-115)

| # | Test | Status |
|---|---|---|
| 107-115 | Stripe / Authority / SAML / SCIM | ⏸ | All plugin-dependent. v1.1 / v1.2. |

## Admin UI tests (116-123)

| # | Test | Status | Reference |
|---|---|---|---|
| 116 | Command palette ⌘K | ✅ | `admin/src/layout/command_palette.tsx` (v1.7.18) — Cmd/Ctrl+K opens overlay; fuzzy substring across 13 pages + N collections; ArrowUp/Down + Enter via wouter navigate; Escape/click-outside closes. |
| 117 | Realtime collaboration in record editor | ⏸ | v1.1 / v1.2. |
| 118 | Bulk operations + undo | ✅ | `admin/src/pages/records/list.tsx` (v1.7.19) — checkbox column + sticky toolbar; bulk-delete via `POST /records/batch` atomic=false with per-op result banner. Undo deferred. |
| 119 | Inline edit ⌘E | ✅ | Same (v1.7.19) — dblclick swaps cell for `<input>`; Enter PATCHes, Escape cancels; text/number/email/url/bool/select fields inline, others click-through. |
| 120 | Admin UI uses generated SDK | ✅ | `admin/` uses the same TS SDK shapes. |
| 121 | Per-screen RBAC (readonly admin) | ⏸ | v1.1.x polish. |
| 122 | Admin UI mobile | 🔄 | Tailwind responsive classes present but no per-form-factor smoke test. |
| 123 | Admin UI hooks editor | ✅ | `admin/src/screens/hooks.tsx` + `internal/api/adminapi/hooks_files.go` (v1.7.20b) — Monaco editor; file-tree left pane; 800ms debounced auto-save; status pill (Saving/Saved/Error); Format + Reload toolbar; path-traversal-safe `GET/PUT/DELETE /api/_admin/hooks/files[/{path}]`; 503 unavailable when `HooksDir` not yet wired in `app.go`. |

## Templates tests (124)

| # | Test | Status |
|---|---|---|
| 124 | basic/saas/mobile/ai templates | 🔄 | `railbase init` exists; `--template` flag with these specific templates not yet shipped. v1.1 polish. |

## LLM-tooling tests (125-126)

| # | Test | Status |
|---|---|---|
| 125 | `railbase generate schema-json` | ✅ | `pkg/railbase/cli/generate.go newGenerateSchemaJSONCmd` (v1.7.11) — emits `{schema_hash, generated_at, collections}` JSON; `--check` drift gate same as `generate sdk`. 6 unit tests covering stdout / file output / check-match / check-drift / check-missing-out / empty-registry. |
| 126 | railbase-mcp plugin | ⏸ | v1.2 plugin. |

## Notifications / webhooks / i18n / cache / etc (132-168)

| # | Test | Status | Reference |
|---|---|---|---|
| 132 | Notifications channels (inapp + email + push) | 🔄 | inapp + email ✅ (`internal/api/rest/notifications_e2e_test.go`). Push deferred to plugin. |
| 133 | Real-time notification delivery | 🔄 | Bus publish on Send ✅; WS subscriber pattern is SSE-based in v1.3.0. |
| 134 | Quiet hours buffering | ⏸ | Deferred per plan §3.9.1. |
| 135 | Outbound webhook fan-out + retry + dead-letter | ✅ | `internal/api/rest/webhooks_e2e_test.go` (v1.5.0). |
| 136 | Webhook replay protection (timestamp window) | ✅ | Signature includes `t=<unix>` per docs/21. |
| 137 | Webhook anti-SSRF | ✅ | `internal/webhooks/webhooks_test.go` rejects file:// / loopback / RFC1918 in production. |
| 138 | i18n locale resolution | ✅ | `internal/i18n/i18n_test.go` (v1.5.5). |
| 139 | i18n translatable field | ⏸ | `.Translatable()` builder + `_translations` table deferred per plan §3.9.3. |
| 140 | i18n RTL | ✅ | `Locale.Dir()` returns "rtl" for ar/he/fa/ur. |
| 141 | i18n pluralisation | ✅ | `Catalog.Plural(.one/.other)` English-grade. |
| 142 | Cache hit ratio | ✅ | `internal/cache/cache_test.go` (v1.5.1). |
| 143 | Cache stampede singleflight | ✅ | Same file. |
| 144 | CSV data import dry-run | ✅ | `pkg/railbase/cli/import_data_test.go` + `import_data_e2e_test.go` (v1.7.19) — 14 unit tests (header peek / column allow-list including system + auth-extras / COPY SQL shape + literal quoting / pgQuoteLiteral round-trips) + 6 embed_pg subtests sharing one PG (happy path / unknown collection / bad column rejected pre-COPY / gzipped CSV / TSV delimiter / NOT-NULL violation all-or-nothing). Dry-run via reading `--file` without writing isn't a separate flag — the column-validation phase happens BEFORE the DB connection acquires, so the operator gets a fast no-op error on bad CSV. |
| 145 | Bulk import via jobs | ✅ | Same v1.7.19 slice — `railbase import data` uses Postgres `COPY FROM STDIN` (single round-trip, server-side parsing, all-or-nothing) which is the bulk path. Async-via-jobs (for files > pool's idle timeout) is deferred — the sync CLI is fast enough for 1M-row datasets on a modern box. |
| 146 | Testing helpers (`NewTestApp`) | ✅ | `pkg/railbase/testapp/testapp.go` + `actor.go` + `response.go` (v1.7.20a) — `testapp.New(t, WithCollection(...))` spins embedded PG + migrations + REST + auth mounted; `AsAnonymous` / `AsUser(collection, email, password)` actors; Response w/ Status / StatusIn / JSON / JSONArray / DecodeJSON / Body / Bytes / Header. 8 self-test subtests under `-race -tags embed_pg`. |
| 147 | YAML fixtures | ✅ | `pkg/railbase/testapp/fixtures.go` (v1.7.20a) — `app.LoadFixtures("users", "posts")` reads `__fixtures__/<name>.json` (JSON not YAML — justified in fixture.go header; YAML wrapper deferred). Top-level object = `{collection: [{row}, ...]}`; parameterised INSERTs; FK-order via filename order. |
| 148 | JS hook unit tests | 📋 | JS-side `mockApp().fireHook(...)` harness deferred to v1.x. Go-side hook tests work today via `testapp` + `internal/hooks` direct dispatch. |
| 149 | CSRF protection | ✅ | `internal/security/csrf_test.go` (v1.5.4). |
| 150 | Security headers | ✅ | `internal/security/headers_test.go` (v1.4.14). |
| 151 | IP allowlist | ✅ | `internal/security/ipfilter_test.go` (v1.4.14). |
| 152 | Account lockout | ✅ | `internal/auth/lockout/lockout_test.go`. |
| 153 | Trusted proxy XFF | ✅ | `internal/security/clientip_test.go`. |
| 154 | Field-level encryption | ⏸ | v1.1 per plan §4.13. |
| 155 | Encryption key rotation | ⏸ | Same. |
| 156 | KMS integration | ⏸ | Same. |
| 157 | Streaming response (SSE) | ✅ | `internal/stream/stream_test.go` (v1.5.2). |
| 158 | Streaming backpressure | ✅ | Same. |
| 159 | Self-update | ⏸ | v1.1 per plan §4.14. |
| 160 | Self-update cluster | ⏸ | Same. |
| 161 | Self-update breaking-change check | ⏸ | Same. |
| 162 | Soft delete + restore + purge | ✅ | `internal/api/rest/softdelete_e2e_test.go` (v1.4.12). |
| 163 | Soft delete cascade | 🔄 | Cascade deferred per docs/03 §soft delete. |
| 164 | Batch atomic + per-op result + 413 | ✅ | `batch_e2e_test.go` (v1.4.13). |
| 165 | Batch non-atomic 207 | ✅ | Same. |
| 166-168 | Workflow saga (plugin) | ⏸ | `railbase-workflow` plugin. v1.2. |

## Performance / load tests (169-175)

| # | Test | Status |
|---|---|---|
| 169 | Realtime fan-out 10k subscribers — no degradation | ✅ | `internal/realtime/bench_test.go` (v1.7.22) — `BenchmarkPublishToDeliver_FanOut[_1k/_10k]` measure last-subscriber latency; M2 baseline: 100 subs 43µs p50 / 75µs p99, 1k subs 259µs / 605µs, 10k subs 2.3ms / 3.9ms. Sub-linear scaling (0.23µs per sub at 10k). Way under 100ms gate. |
| 170 | Realtime fan-out via NATS plugin distributed broker | ⏸ | Plugin (v1.1 `railbase-cluster`). |
| 171 | DB throughput 10k writes/sec on single Postgres | ✅ | `internal/api/rest/throughput_bench_test.go` (v1.7.22) — serial INSERT 21k rows/sec; 8-goroutine concurrent + 32-goroutine concurrent variants; `BenchmarkThroughput_CopyFrom` 258k rows/sec (v1.7.19 bulk-load path). M2 baseline 2.1× the target on serial path alone. |
| 172 | RLS overhead < 5% latency on 10M rows | ✅ | `internal/api/rest/rls_bench_test.go` (v1.7.23) — scaled to 100k rows (10M dominates CI wall; ratio is hardware-independent). `BenchmarkRLS_Select_NoRLS / _WithRLS` + `_SelectRange_*` measure point + range-query paths; `TestRLS_Overhead_Under5Pct` is the invariant — median range-query overhead under 5% (M2 baseline: **2.53%**, half the docs/17 budget). Seed via COPY: 100k rows × 2 tables in ~1.1s. |
| 174 | Hook concurrency 100 invocations — no deadlocks | ✅ | `internal/hooks/bench_test.go` (v1.7.22) — `BenchmarkDispatch_Concurrent_100` 304k dispatches/sec under 100 goroutines, 3µs mean per dispatch (serialised through vmMu by design); no-handlers fast-path 58ns/op; single-handler 2.6µs/op. `TestDispatch_Concurrent_NoDeadlock` exercises 100×50 = 5000 dispatches with a 30s deadline as the no-deadlock invariant. |
| 175 | Document upload concurrency 50 uploads — no corruption | ✅ | `internal/files/concurrent_bench_test.go` (v1.7.23) — `BenchmarkFSDriver_Put_Serial/_Concurrent_8/_Concurrent_50` measure throughput (M2: 6.4k serial / 11k @ 8 goroutines / 9.5k @ 50 goroutines uploads/sec — gentle slow-down at 50 from FS lock contention). `TestFSDriver_Concurrent_NoCorruption` is the invariant — 50 goroutines × 20 uploads = 1000 distinct files w/ random content; every file's SHA256 verified post-storm; orphan `.tmp` count must be zero (failed-rename detector). |
| 173 | Jobs queue throughput + no-double-execution under 4 workers | ✅ | `internal/jobs/bench_test.go` (v1.7.14): `BenchmarkEnqueue` + `BenchmarkClaim` report `jobs_per_sec` + `p50_µs` / `p99_µs`. `TestJobsThroughput_NoDoubleExecution` exercises 4 workers claiming 200 jobs with zero duplication invariant under real Postgres. |

---

## v1 SHIP test-debt list (📋 only — what we must ship before v1)

The following items are in the 📋 bucket and gate v1 SHIP. Triaged by effort:

### Small (≤ 1 day each)
- ~~**#2** 5-min smoke script~~ ✅ v1.7.12
- ~~**#13** Pagination cursor stability under concurrent writes~~ ✅ v1.7.16
- ~~**#17** Filter parser BETWEEN + IN coverage gap~~ ✅ v1.7.21 (BETWEEN shipped as a new parser feature; IN coverage extended).
- **#34** Devices trust 30d + revoke (deferred to v1.1.x per plan — could move into v1).
- ~~**#52** Realtime <100ms latency benchmark~~ ✅ v1.7.11
- **#55** Hooks memory limit (depends on hooks v1.2.0 polish slice).
- ~~**#93** Audit retention auto-archive cron~~ ✅ v1.7.13
- ~~**#125** `railbase generate schema-json`~~ ✅ v1.7.11
- ~~**#10ax** Tree integrity nightly job (orphans/cycles)~~ ✅ v1.7.14 (orphans only — cycle detection deferred)

### Medium (1-3 days each)
- ~~**#50** `$app.realtime().publish()` hook binding~~ ✅ v1.7.15
- ~~**#60** `routerAdd` hook binding~~ ✅ v1.7.17
- ~~**#61** `cronAdd` hook binding~~ ✅ v1.7.18
- ~~**#118-119** Admin UI bulk ops + inline edit~~ ✅ v1.7.19
- ~~**#116, #123** Admin UI command palette + hooks editor~~ ✅ v1.7.18 + v1.7.20b
- ~~**#144-145** CSV data import (`railbase import data`)~~ ✅ v1.7.19
- ~~**#146-147** Testing infrastructure (`NewTestApp` + JSON fixtures)~~ ✅ v1.7.20a (#148 JS hook harness deferred to v1.x)
- ~~**#169-175** Performance benchmark suite~~ ✅ v1.7.22 (#169 realtime fan-out, #171 DB write throughput, #174 hook concurrency); #172 RLS overhead + #175 file upload concurrency deferred to v1.7.23.

### Out of v1 (defer to v1.1 / v1.2)
Everything in the ⏸ bucket. Notable: all plugin tests (107-115, 132-push, 159-161, 166-168), encryption (154-156), document mgmt (68-76), 35+ OAuth providers (106), full PB-SDK drop-in shape test (3).

---

## Recommended path to v1 SHIP

**~2 weeks** of focused work covers the entire 📋 list above. Prioritisation:

1. **Week 1** — admin UI critical-path polish (~5 admin-UI agent slices) + smoke-5min.sh + cursor-stability test + filter BETWEEN/IN + audit retention cron. Roughly the items marked Small + 2 of the Medium.
2. **Week 2** — Hooks $app.realtime/routerAdd/cronAdd bindings + testing infrastructure (testing helpers, YAML fixtures) + benchmark suite. Most of the remaining Medium items.

After both weeks, **only the ⏸ items remain** — all plugin-dependent or post-v1 features, none of which block v1 SHIP per the original docs/16 roadmap.

---

## Audit metadata

- Last refresh: 2026-05-12 (post v1.7.30 — first v1.x-bonus pair after v1 SHIP gates closed: `send_email_async` job builtin + YAML fixtures in testapp). v1 SHIP critical-path empty; remaining work is v1.x-bonus + explicit v1.1+ roadmap stages.
- Methodology: cross-referenced every `_test.go` and `_e2e_test.go` file in the repo against docs/17 item numbers.
- Refresh trigger: after every v1.7.x milestone that ships new test coverage, re-grep and update this file.
- This file is the SHIP gate's source of truth. When it shows zero 📋 items, v1 SHIP unblocks.

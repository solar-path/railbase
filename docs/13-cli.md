# 13 — CLI: all commands

`railbase` CLI built on `spf13/cobra`.

## Commands

### Init & lifecycle

```
railbase init <name> [--template basic|auth-starter|fullstack]
                     [--railbase-source <abs-path-to-railbase-checkout>]
  Scaffold pb_data/, pb_hooks/, schema/main.go, railbase.yaml, Makefile,
  cmd/<name>/main.go. With --railbase-source the generated go.mod gets a
  `replace` directive pointing at your local railbase tree (validated:
  path exists, is a directory, contains a go.mod for github.com/railbase/railbase).

railbase serve [--addr :8095] [--dev]
  Start HTTP server. --dev: hot reload Go code via embedded air-style watcher.

railbase dev [--addr :8095] [--embed-pg] [--web ./web] [--web-cmd "npm run dev"]
  Run backend + frontend dev server side-by-side with single Ctrl-C lifecycle.
  Reads RAILBASE_HTTP_ADDR from env when --addr isn't explicitly passed
  (FEEDBACK #B5 — pre-fix, the cobra default :8095 always won over a .env value).

railbase version
  Print version, build info, plugins installed.
```

**Scaffold Makefile.** `railbase init` теперь генерирует Makefile с
`TAGS_DEV ?= embed_pg` встроенным в targets `build`, `dev`,
`migrate-diff`, `migrate-up`. `make dev` на свежем проекте сразу даёт
embedded-postgres путь без знания про `-tags embed_pg`. Production-
slim бинарь — отдельным `make build-prod` (без тега). FEEDBACK #6/#27.

### Migrations

```
railbase migrate up [--steps N]
  Apply pending migrations.

railbase migrate down [--steps 1]
  Roll back N migrations.

railbase migrate status
  Show applied vs pending migrations.

railbase migrate diff [--out migrations/]
  Compare Go DSL schema with applied; generate new migration files.

railbase migrate jsdiff [--out pb_migrations/]
  Generate JS-style migrations (PB compat).

railbase migrate create <name>
  Scaffold blank migration file.

railbase migrate user-upgrade
  Run user-side migrations during Railbase version upgrade.
```

### Admin / superusers

```
railbase admin create <email> [--password <p>] [--no-email]
  Create system admin (с force-2FA on first login). EMAIL is a POSITIONAL
  argument — `--email X` not accepted (FEEDBACK #B9).
  Password — interactive prompt by default (insecure path: `--password 'X'`
  ends up in shell history; rotate after first sign-in).
  --no-email skips welcome + broadcast notice emails (use on fresh boxes
  without mailer setup; rerun via `railbase jobs enqueue` later).
  Prints a one-line note when `mailer.from` is unset so the operator
  doesn't wonder where the welcome email is (FEEDBACK #10).

railbase admin update <email> [--password <p>]
  Update admin.

railbase admin delete <email>
  Delete admin.

railbase admin list
  List all system admins.

railbase admin reset-2fa <email>
  Reset 2FA для admin (audit обязательный).
```

### Generation

```
railbase generate sdk [--out ./client] [--lang ts|swift|kotlin|dart]
  Generate frontend SDK from Go schema.

railbase generate openapi [--out openapi.json]
  Generate OpenAPI spec.

railbase generate schema-json [--out schema.json]
  Generate machine-readable schema (для LLM agents).
```

### Import

```
railbase import schema --from-pb <url> [--admin-email <e>]
  Migrate schema из existing PocketBase. Generates Go DSL + migrations.

railbase import data <collection> --file <csv-path> [--delimiter ,] [--quote "] [--null ""]
  Bulk-load CSV rows via Postgres COPY FROM STDIN.
  Header row required; unknown headers fail BEFORE the DB is touched.

  Column-type cheatsheet:
    Number / Int            → bare digits: 42, -7, 1234
    Bool                    → true / false (also 1 / 0)
    Date / DateTime         → ISO-8601: 2026-05-16, 2026-05-16T01:13:58Z
    Tags / Relations (M2M)  → Postgres array literal: "{tag1,tag2}"
                              FEEDBACK #B10 — quote-wrap if any tag has comma/space.
    JSON / Translatable     → quoted JSON object: "{""key"":""val""}"
    File                    → import via REST multipart, not CSV
    NULL                    → use --null '\N' (or whatever sentinel you pick)
```

### Plugins

```
railbase plugin install <name> [--from <url>]
  Install plugin from registry или URL.

railbase plugin remove <name>
  Uninstall.

railbase plugin list
  List installed plugins.

railbase plugin update [<name>]
  Update plugin(s) to latest.

railbase plugin info <name>
  Show plugin manifest.
```

### Backup & restore

```
railbase backup [--out file.tgz] [--upload-s3]
  Create backup.

railbase restore <file.tgz> [--force]
  Restore from backup. --force skips version compat check.

railbase backup list
  List existing backups (local + S3 если configured).

railbase backup schedule "0 3 * * *" [--retention 30d]
  Configure scheduled backup.
```

### Documents

```
railbase documents list [--owner-type <t>] [--owner-id <id>]
  List documents matching filter.

railbase documents archive <id> [--reason <r>]
  Archive document.

railbase documents purge <id> --confirm
  Permanent delete (irreversible, audit обязательный).

railbase documents quota [--tenant <id>]
  Show quota usage.

railbase documents extract-text [--all | --document <id>]
  Trigger text extraction job.
```

### Audit

```
railbase audit verify
  Walk hash chains end-to-end + verify Ed25519 seals where present.
  Reports the first row whose recomputed hash doesn't match.

railbase audit seal-keygen [--force]
  Write a fresh Ed25519 keypair to <dataDir>/.audit_seal_key
  (chmod 0600). Refuses to overwrite without --force — historical
  seals still verify against their persisted public_key, but new
  seals shift to the new key.

railbase audit export [--from <date>] [--to <date>] --out <file>
  Export audit log to JSON/CSV.
```

#### Chain coverage (v3.x)

`audit verify` currently walks the **legacy `_audit_log`** chain
(chain v1). The v3.x split tables — `_audit_log_site` and
`_audit_log_tenant` — have their own per-table chains, verified
through Store APIs (`audit.Store.VerifySite` / `VerifyTenant`).

**Phase 1**: the CLI's `audit verify` reports on chain v1 only. The
v3 chains are write-path verified (chain advances atomically with
each insert, broken chains surface immediately as failed writes) and
read-path verifiable through the admin UI Timeline filter for
`outcome=denied|error` rows.

**Phase 1.5 plan**: extend `audit verify` to walk all three chains
(legacy + site + per-tenant), with `--target=site|tenant|legacy|all`
flag. Until then, deployments needing forensic-grade tamper-evidence
should stay on legacy writers (the `Writer.AttachStore` dual-write
keeps both chains in sync).

### Auth helpers

```
railbase auth apple-secret \
  --team-id <id> --key-id <id> --private-key <file> --client-id <id>
  Generate Apple Sign-In client_secret JWT.

railbase auth oauth2-test --provider <name>
  Test OAuth2 provider config.

railbase auth scim-token --tenant <id>
  Issue SCIM provisioning token (с plugin railbase-scim).
```

### Database

```
railbase db shell
  Interactive psql session с активной DSN. Admin-only.

railbase db vacuum [--full]
  Run VACUUM (либо `VACUUM FULL` с `--full` flag).

railbase db analyze
  Run ANALYZE для refresh planner statistics.

railbase db stats
  Pool stats, table sizes, slow queries (через pg_stat_statements если enabled).
```

### Settings

```
railbase config get <key>
  Read config value (env-resolved).

railbase config set <key> <value>
  Set runtime-mutable setting (стores в _settings).

railbase config list [--section <s>]
  List all settings.
```

### Mailer

```
railbase mailer test --to <email> [--template <name>]
  Send test email (verify SMTP config).
```

### Export (CLI)

```
railbase export collection <name> --format xlsx [--filter <expr>] --out <file>
railbase export query "SELECT ..." --format xlsx --out <file>
railbase export pdf --template <name> --data <json> --out <file>
```

### Healthcheck

```
railbase health
  CLI healthcheck (для container probes без HTTP).
```

### UI kit (`ui`)

Раздача embedded shadcn-on-Preact компонент-библиотеки downstream-приложениям. Бинарь embed'ит весь `admin/src/lib/ui/` (50 компонентов + 11 Radix-replacement primitives + cn/icons/theme + styles.css с oklch theme tokens); этот subcommand копирует source-файлы в указанный frontend-проект — shadcn-style "copy don't install" workflow, но без HTTP round-trip к shadcn.com (registry зашит в Railbase).

Те же файлы параллельно доступны через HTTP по `/api/_ui/*` — см. docs/12-admin-ui.md, секция «Shareable UI kit».

```
railbase ui list [--with-peers]
  Print available components (alphabetical, one per line).
  --with-peers : append npm peer-dep list to each row.

railbase ui peers [--json]
  Print `npm install <peers>` line for the entire kit.
  Includes peers transitively reached via _primitives/ (например
  @floating-ui/dom приходит из popper.tsx).
  --json : array form вместо shell line.

railbase ui init [--out DIR]
  Scaffold foundation files в DIR (default: ./):
    src/styles.css                  (theme tokens — only if absent)
    src/lib/ui/cn.ts                (overwrite — owned by kit)
    src/lib/ui/icons.tsx            (overwrite)
    src/lib/ui/theme.ts             (overwrite)
    src/lib/ui/index.ts             (overwrite)
    src/lib/ui/_primitives/*.{ts,tsx}  (overwrite — all 11 files)
  styles.css скипается если уже существует — пользователи обычно
  владеют своим global CSS, не клобберим.

railbase ui add NAME... [--out DIR] [--force]
railbase ui add --all     [--out DIR] [--force]
  Copy specific components in DIR/src/lib/ui/. Transitive local deps
  resolve автоматически через BFS по импортам:
    railbase ui add password   # подтянет ./input.ui за компанию
    railbase ui add form       # подтянет ./label.ui
  Печатает peer-dep summary в stderr (npm install ... для выбранного
  set'a). --force перезаписывает существующие файлы (default: skip).
```

Pre-condition для `ui add`: `cn.ts` должен существовать (т.е. `ui init` уже запускался). Иначе команда падает с подсказкой «run `railbase ui init` first».

### MCP (с plugin railbase-mcp)

```
railbase mcp serve
  Start MCP server для LLM agents (alternative to HTTP).
```

## Конфигурация

```
--config <path>             Override default railbase.yaml
--data <path>               Override pb_data/ location
--addr <addr>               HTTP listen address (default :8095)
--db-url <url>              Override RAILBASE_DB

--log-level debug|info|warn|error

--dev                       Development mode: hot reload, source maps,
                            console mailer, sample data offer
```

## Environment variables

```
RAILBASE_CONFIG             config file path
RAILBASE_DSN                Postgres DSN (`postgres://user:pass@host:port/db?sslmode=...`)
RAILBASE_EMBED_POSTGRES     "true" для запуска embedded PG subprocess (dev only; refused в RAILBASE_PROD=true)
RAILBASE_EMBED_PG_PORT      override embedded-PG port (1..65535). Без override:
                            sticky choice из <DataDir>/postgres/.port → default 54329 →
                            scan [54330, 54429]. Используется для развода двух Railbase-
                            проектов на одной машине (FEEDBACK #B4).
RAILBASE_HTTP_ADDR          listen address. Читается и `serve`, и `dev` (FEEDBACK #B5);
                            explicit `--addr` имеет приоритет над env value.
RAILBASE_ADDR               legacy alias for RAILBASE_HTTP_ADDR (kept for v0 configs)
RAILBASE_DATA               data dir
RAILBASE_DATA_DIR           same as RAILBASE_DATA (admin setup wizard reads this when persisting .dsn)
RAILBASE_LOG_LEVEL
RAILBASE_PBCOMPAT           strict | native | both
RAILBASE_PROD               production mode flag (disables dev features)
RAILBASE_FORCE_INIT         "1" overrides v1.7.42 foreign-DB safety gate; allows migrating into a non-empty DB
                            that lacks the `_migrations` marker (co-location with another app). Default: refuse.
RAILBASE_LOCAL_PATH         absolute path to a local railbase checkout for `railbase init`'s
                            `replace` directive. Validated at init time — path must exist,
                            be a directory, and contain a go.mod whose `module` line names
                            `github.com/railbase/railbase` (FEEDBACK #12).
RAILBASE_CLUSTER_PEERS      cluster mode: "host1:4222,host2:4222" (для railbase-cluster plugin)
RAILBASE_STORAGE            storage URL: fs:./storage | s3://bucket?...
RAILBASE_SECRET_KEY         override .secret file
OTEL_EXPORTER_OTLP_ENDPOINT OpenTelemetry
HOOKS_TIMEOUT_MS
HOOKS_OS_CMD                enable $os.cmd in hooks (security)
EXPORT_MEMORY_LIMIT_MB
DOCUMENTS_ALLOW_HARD_DELETE
DOCUMENTS_EXTRACT_TEXT
```

## Exit codes

- 0 — success
- 1 — generic error
- 2 — config / arguments error
- 3 — DB error
- 4 — migration drift detected (без `--allow-drift`)
- 5 — plugin error
- 6 — version mismatch (restore)

# 13 — CLI: all commands

`railbase` CLI built on `spf13/cobra`.

## Commands

### Init & lifecycle

```
railbase init <name> [--template basic|saas|mobile|ai]
  Scaffold pb_data/, pb_hooks/, schema/main.go, railbase.yaml.

railbase serve [--addr :8090] [--dev]
  Start HTTP server. --dev: hot reload Go code via embedded air-style watcher.

railbase version
  Print version, build info, plugins installed.
```

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
railbase admin create <email> [--password <p>]
  Create system admin (с force-2FA on first login).

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

railbase import data --from-pb <url> --collection <name>
  Import data из PocketBase collection.

railbase import csv <file> --collection <name>
  Import CSV.
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
  Verify hash chain integrity (если sealing enabled).

railbase audit seal
  Manual seal hash chain (signs latest hash с Ed25519).

railbase audit export [--from <date>] [--to <date>] --out <file>
  Export audit log to JSON/CSV.
```

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

### MCP (с plugin railbase-mcp)

```
railbase mcp serve
  Start MCP server для LLM agents (alternative to HTTP).
```

## Конфигурация

```
--config <path>             Override default railbase.yaml
--data <path>               Override pb_data/ location
--addr <addr>               HTTP listen address (default :8090)
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
RAILBASE_ADDR               listen address
RAILBASE_DATA               data dir
RAILBASE_LOG_LEVEL
RAILBASE_PBCOMPAT           strict | native | both
RAILBASE_PROD               production mode flag (disables dev features)
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

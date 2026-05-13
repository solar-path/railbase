# 23 — Testing infrastructure для пользователей

Vibe-friendly testing — vibe-коду нужны guardrails. PB не имеет first-class; Railbase даёт.

## CLI

```bash
railbase test                                # обнаруживает *_test.go и pb_hooks/__tests__/*.test.js
railbase test --collection posts             # filter по collection
railbase test --watch                         # re-run on file change
railbase test --coverage                     # combined Go + JS coverage report
railbase test --integration                   # запускает только integration-marked tests
railbase test --only TestCreatePost          # specific test name
```

## Test database

- **Embedded Postgres per-suite** — `fergusstrange/embedded-postgres` запускает PG subprocess на свободном порту; одна инстанция на test binary, fresh `CREATE DATABASE` per-test (или transactional rollback per-test для скорости)
- **Transactional fixtures** (default) — каждый test wrapping в tx, rollback в `t.Cleanup`. Sub-millisecond cleanup; full schema isolation
- **`testcontainers-go` mode** — для tests требующих real Postgres (replication, advisory locks, cross-tx scenarios) через `--testcontainers` flag
- **CI mode** — `RAILBASE_TEST_DSN=postgres://...` подключает к pre-provisioned PG (e.g. GitHub Actions service container); CI-friendly без download embedded-postgres каждый раз

## Fixtures

YAML files в `__fixtures__/`:

```yaml
# __fixtures__/users.yaml
users:
  - id: u1
    email: alice@example.com
    name: Alice
    role: admin
  - id: u2
    email: bob@example.com
    name: Bob
    role: user

# __fixtures__/posts.yaml
posts:
  - id: p1
    title: Hello
    author: u1
    status: published
```

Loading:
```go
app := railbase.NewTestApp(t)
app.LoadFixtures("users", "posts")
```

## API testing helpers (Go)

```go
func TestCreatePost(t *testing.T) {
    app := railbase.NewTestApp(t)
    app.LoadFixtures("users")

    // As specific user
    actor := app.AsUser("alice@example.com")

    // HTTP-style assertions
    post := actor.Post("/api/collections/posts/records", `{"title":"Hi"}`).
        Status(201).
        JSON()
    assert.Equal(t, "Hi", post["title"])

    // Subsequent requests carry session
    list := actor.Get("/api/collections/posts/records").
        Status(200).
        JSON()
    assert.Len(t, list["items"], 1)

    // Anonymous
    anon := app.AsAnonymous()
    anon.Get("/api/collections/posts/records").Status(401)

    // Admin
    admin := app.AsAdmin()
    admin.Get("/_/api/users").Status(200)
}
```

## Hook unit tests (JS)

```js
// pb_hooks/__tests__/posts.test.js
import { test, expect, mockApp } from "@railbase/test"

test("computeSlug на BeforeCreate", () => {
  const app = mockApp()
  const record = app.dao().newRecord("posts", { title: "Hello World" })
  app.fireHook("onRecordCreate", { record })
  expect(record.get("slug")).toBe("hello-world")
})

test("guard на пустой title", () => {
  const app = mockApp()
  const record = app.dao().newRecord("posts", {})
  expect(() => app.fireHook("onRecordCreate", { record })).toThrow(/title required/)
})
```

Runner: Bun test (default) или Node with `vitest`. `mockApp()` создаёт isolated runtime с in-memory DB.

## Mock data generator

```go
app.Seed("users", 100)                      // creates 100 valid records
app.Seed("posts", 500, railbase.SeedOpts{
    Relations: map[string]string{"author": "users"},
})
```

Powered by `gofakeit` + schema-aware generation:
- Email field → faker.Email()
- Tel field → faker.Phone() в правильном формате per-region
- Finance/Currency → realistic amounts по distribution
- Relation → random pick из existing records target collection
- Enum → random выбор из allowed values
- Pattern → regex-aware (через `regen` library)

Configurable seed для reproducibility:
```go
app.Seed("users", 100, railbase.SeedOpts{Seed: 12345})
```

## Lint enforcement для admin UI

Поверх tsc + Playwright admin UI имеет третий слой защиты от design-drift — собственный ESLint plugin `eslint-plugin-railbase` (живёт в-репо, не публикуется на npm). Цель — не дать следующей волне правок незаметно вернуть UI в дрейф после Wave 0-3.

**Rules** (все три — flat-config flag-driven, начинают как `warn`, флипаются в `error` после миграции всех экранов):

| Rule | Что проверяет | Escape hatch |
|---|---|---|
| `railbase/no-raw-page-shell` | Экран в `admin/src/screens/*.tsx` должен начинаться с `<AdminPage>` либо whitelisted base (`<LoginShell>` для login, `<BootstrapShell>` для wizard) | Whitelist по filename |
| `railbase/no-hardcoded-tw-color` | Tailwind utility classes с literal цветом (`text-red-500`, `bg-[#1a1a1a]`) запрещены — все цвета через oklch tokens из `lib/ui/theme.ts` | Comment-pragma `/* recharts: explicit hex required */` для chart series; whitelist для `currentColor` |
| `railbase/no-list-when-data-is-paged` | `useQuery` с возвращаемым `{ items, total, page }` shape не должен рендериться через `.map()` без `<QDataTable>` либо `<Pager>` wrap | Per-line `// eslint-disable-next-line` для legitimately-small lists (≤ 5 rows known) |

**Phasing**: Wave 1 включает rules в `warn` (CI green, видны в IDE). Wave 3 после полной миграции экранов на `<AdminPage>` — флип в `error`. Это предотвращает «обновлю один экран и сломаю CI на других».

**Plugin source**: `admin/eslint-rules/` — три файла rule + `index.ts` aggregator. `admin/eslint.config.ts` ссылается на локальный путь, не на published package.

**Не покрывается ESLint** (нужен другой механизм):
- Bundle-size regression — отдельный CI шаг (`vite build` + size check), не lint
- Accessibility (ARIA, focus order) — Playwright + axe-core, не ESLint
- Visual drift — Playwright screenshot diff (см. ниже)

## Snapshot testing для admin UI

Playwright scaffold уже скоммичен в `admin/playwright.config.ts` + `admin/e2e/{auth,screens}.spec.ts`. Покрывает 10 тестов (login flow + baseline для 8 ключевых экранов: schema / audit / logs / jobs / health / settings / api-tokens / mailer). Threshold `maxDiffPixelRatio: 0.2%` — толерантен к font-hinting drift между ОС, но ловит layout-регрессии.

Setup (one-off per dev machine):

```bash
cd admin
bunx playwright install chromium     # ~250 MB browser binary
```

Workflow:

```bash
bun run test:e2e          # headless, validates baseline (CI default)
bun run test:e2e:ui       # interactive — review screenshots side-by-side
bun run test:e2e:update   # accept new baselines after intentional UI change
```

Бэкенд для тестов поднимается отдельно (CI делает это explicitly, локально оператор делает `make run-embed` в соседнем терминале) — Playwright `webServer` нарочно НЕ настроен, чтобы было видно когда e2e-сессия использует «грязный» state из dev-БД.

Baseline-изображения генерируются на первом `--update-snapshots` запуске и коммитятся в `admin/e2e/__snapshots__/`. Они platform-агностичны благодаря loose threshold + `mask:` на `.tabular-nums` / `.rb-mono` (счётчики, ids, timestamps), но при крупном tailwind-обновлении могут потребовать `--update-snapshots`.

Через Playwright integration в template:

```ts
// admin-ui/__tests__/posts.spec.ts
import { test, expect } from "@playwright/test"

test("posts list renders", async ({ page }) => {
  await page.goto("/_/collections/posts")
  await expect(page).toHaveScreenshot("posts-list.png")
})
```

Auto-runs в CI; review через Playwright UI mode локально.

## Integration tests

Marked tests используют real DB (Postgres testcontainer), real mailer (mhale/smtpd test SMTP), real S3 (minio testcontainer):

```go
//go:build integration

func TestStripeWebhook(t *testing.T) {
    app := railbase.NewIntegrationApp(t)
    // ... real Stripe test webhook simulation
}
```

`railbase test --integration` запускает только эти.

## Realtime testing

```go
ws := actor.WebSocket("/api/realtime")
defer ws.Close()

ws.Subscribe("posts", "*")

actor.Post("/api/collections/posts/records", `{"title":"Hi"}`).Status(201)

event := ws.Wait(100 * time.Millisecond)
assert.Equal(t, "create", event.Action)
assert.Equal(t, "Hi", event.Record["title"])
```

## Coverage

Combined report Go core + JS hooks:

```
railbase test --coverage --out coverage.html

# Opens in browser:
# - Go files coverage (core + user code)
# - JS hooks coverage (per-file)
# - Combined %
```

## Golden tests

Для DB dialect parity (см. [03-data-layer.md](03-data-layer.md)):

```go
//go:build golden

func TestSchemaDiff(t *testing.T) {
    diff := schema.Diff(oldSchema, newSchema)
    golden.Match(t, diff.SQL())
}
```

Update golden: `railbase test --update-golden`.

## Test patterns в documentation

В docs ship cookbooks:
- "Testing custom hooks"
- "Testing with multiple users (RBAC)"
- "Testing realtime subscriptions"
- "Testing payment flows (с stripe-mock)"
- "Testing email sending (с test SMTP)"
- "Testing approvals (authority plugin)"

## CI integration

```yaml
# .github/workflows/test.yml
- run: railbase test --coverage --ci    # JUnit XML + coverage report
- run: railbase test --integration      # с testcontainers
```

## Test isolation

- Каждый `NewTestApp(t)` — fresh database
- Tests могут run в parallel через `t.Parallel()`
- Schema applied once per process (cached); data isolated per-test
- Fixtures reloaded per-test

## Что НЕ делает

- Load testing — leave to k6 / vegeta / etc. (Railbase exposes Prometheus metrics для observability)
- Chaos engineering — too specific
- Visual regression beyond admin UI — frontend dev's responsibility

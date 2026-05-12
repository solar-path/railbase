# 12 — Admin UI

Главный touchpoint между Railbase и разработчиком/оператором. Не пытается победить Retool, но должен покрыть всё, что делает PB admin (с улучшениями).

## Tech stack

- **React 19** + **TypeScript** (strict)
- **Tailwind 4** + **Radix UI** primitives + **shadcn/ui** patterns
- **wouter** для routing (~1.5 KB, hook-based, minimal API) + **TanStack Query** (data + cache)
- **QDataTable** — кастомный data grid (см. ниже); virtualized через `@tanstack/virtual` (только windowing primitive, не headless table API)
- **Monaco editor** для hooks JS / JSON / SQL view queries
- **Tiptap** для RichText field editor
- **React Hook Form** + **zod** для всех форм
- **Recharts** для dashboard графиков (поверх shadcn chart primitives)
- **cmdk** для command palette
- **Vite** build → один `dist/` → `go:embed` в бинарь
- **Использует тот же TS SDK**, что Railbase генерирует для пользователей — dogfooding (если что-то сломалось в SDK, admin UI перестаёт работать)

**Заметка про routing**: wouter сознательно отказывается от typed-routes / file-based-routing / nested-loaders в пользу size & simplicity. Для admin UI это приемлемо — данные грузим через TanStack Query (где есть caching, deduplication, mutations), а не через router loaders. Type-safety для path params живёт в helper `routes.ts` (см. ниже), не в router-runtime.

Размер: target ≤ 3 MB gzipped (~5 MB raw embedded).

## Authentication & access

- Только **system admins** (не application users) имеют доступ
- Login на `/_/login` — email + password + 2FA (mandatory с v1)
- Failed-login lockout: 5 attempts → 15 min lockout по IP+email
- Session timeout: 8 hours sliding window (refresh on activity)
- Все admin actions audited в `_audit_log` с маркером `actor_type=system_admin`
- Per-screen RBAC: `system_readonly` видит, `system_admin` изменяет

## Layout

```
┌─────────────────────────────────────────────────────────────┐
│  [Railbase] Search (⌘K)            [Env: prod] [👤 Admin▾] │
├──────────────┬──────────────────────────────────────────────┤
│ ⊞ Dashboard  │                                              │
│ ▼ Data       │                                              │
│  • posts     │                                              │
│  • users     │           Main content area                  │
│  • + new     │                                              │
│ ▼ Auth       │                                              │
│  • Sessions  │                                              │
│  • Devices   │                                              │
│  • Tokens    │                                              │
│ ⚙ RBAC       │                                              │
│ ⏱ Jobs       │                                              │
│ 📤 Realtime  │                                              │
│ 📜 Audit     │                                              │
│ 🛠 Hooks     │                                              │
│ ✉ Mailer     │                                              │
│ 📁 Documents │                                              │
│ 💾 Backups   │                                              │
│ 🔌 Plugins   │                                              │
│ ⚙ Settings   │                                              │
└──────────────┴──────────────────────────────────────────────┘
```

## Screens — полный список (22)

### 1. Dashboard

Stats cards (records per top-5 collections, active sessions, jobs pending/failed, storage used, audit events 24h, error rate, p95 latency); subsystem health indicators; recent activity (последние 20 audit-events, clickable); quick actions; live charts (requests/min, errors/min, hooks/min последние 24h).

### 2. Collection records — data grid

- **QDataTable** (custom virtualized grid; см. [QDataTable раздел](#qdatatable-custom-data-grid))
- Columns configurable (show/hide/reorder/resize, persist в localStorage per-collection)
- **Filtering**: per-column filters typed по field-type (date picker для date, range slider для number, multi-select для enum, **tel** — region selector + partial-number search, **finance** — min/max range с decimal input, **currency** — currency picker + amount range, **address** — country/city/postal filters, **tax_id** — country + type filter, **country/language/timezone/locale** — searchable multi-select, **status** — state machine valid states multi-select, **priority** — level multi-select, **rating** — min stars slider, **tags** — chip-input «has all/any», **tree_path** — subtree picker, **date_range** — overlap-filter с reference range, **percentage** — min/max %, **quantity** — unit-aware range); compound filter builder
- **Saved filters** с именами — shared в команде через DB
- Multi-column sort
- **Pagination**: native cursor (default) + offset toggle; page size 20/50/100/all
- **Search**: FTS если коллекция имеет FTS field
- **Inline edit**: Cmd+E на ячейке → editable; optimistic mutation
- **Bulk operations**: select rows → toolbar {edit field, delete, export.xlsx, export.pdf, run hook, assign role}
- **Realtime**: новые записи появляются с highlight; «3 users editing this collection now» indicator
- **Column rendering** typed: file → thumbnail; relation → linked chip с hover preview; richtext → truncated с «expand»; vector → `[1.23, ...]` chip; **tel → formatted national display + country flag**; **finance → right-aligned, decimal-aligned, with thousand separators**; **currency → right-aligned `$1,234.56` с currency badge; group totals в footer (per-currency)**; **address → single-line «city, country»**; **person_name → «Last, First» (configurable)**; **percentage → right-aligned `15.00%`**; **iban → formatted в groups + country chip**; **tax_id → masked отображение + country flag**; **country → flag + name**; **timezone → city + UTC offset**; **status → coloured chip** (color из state metadata); **priority → coloured icon**; **rating → star icons**; **quantity → `10.5 kg`**; **duration → humanized `1y 6m`**; **date_range → `Jan 1 — Jan 15`**; **tree_path → breadcrumb с last segment bold**; **tags → coloured chips inline**; **color → swatch + hex value**; **barcode → monospace + format badge**; **qr_code → small QR thumbnail (60×60) + value preview tooltip; click → full-size modal с download/print options**
- Density toggle (comfortable / compact / dense)

### 3. Record editor — modal или full page

- 2-column layout: fields слева, metadata справа
- **Field editors** typed (расширенный список):
  - **PB-paritет**: text/email/url с validation; number с slider; bool switch; date picker с timezone; select dropdown; file drag-drop с image preview + thumbnail variants display + replace; relation searchable picker с autocomplete + «open record»; json — Monaco с schema validation; richtext — Tiptap toolbar; vector — read-only display
  - **Communication & contact**: **tel** — country-code dropdown + national number input с live libphonenumber validation; **address** — multi-field form с country selector → постпересчёт state/postal validation, optional «verify on map» с geocoding; **person_name** — multi-field (prefix/first/middle/last/suffix) с culturally-aware order toggle
  - **Money**: **finance** — decimal input с precision-aware mask, no scientific notation, Excel-friendly paste (`1,234.56` / `1234.56`); **currency** — amount input + currency dropdown (filtered по `AllowedCurrencies`); locale-aware preview; precision auto-adjusts per currency (USD=2, JPY=0); **percentage** — input с % suffix, slider option; **money_range** — two currency inputs side-by-side с validation min ≤ max
  - **Banking**: **iban** — masked input с live mod-97 validation, auto-format groups of 4, country chip; **bic** — uppercase input с validation; **bank_account** — composite editor с conditional fields (IBAN-mode vs US-routing-mode по `Format()`)
  - **Identifiers**: **tax_id** — country dropdown + type dropdown (если страна имеет несколько) + masked input per format; **slug** — auto-generated preview от source field с manual override toggle; **sequential_code** — read-only display next-value preview; **barcode** — input + format auto-detect + visual barcode rendering preview
  - **Locale & geo**: **country** — searchable dropdown с flag emoji + native name; **language** — searchable dropdown с native name; **timezone** — searchable IANA tree (grouped by region); **locale** — composite picker (language + country + optional extensions); **coordinates** — lat/lng input + small embedded map (Leaflet) для preview/pick
  - **Quantities**: **quantity** — amount input + unit dropdown (filtered по `UnitGroup`); auto-conversion preview к alternative units; **duration** — composite input (years/months/days/hours/minutes) с ISO 8601 preview; **date_range** — dual date pickers с calendar visualization; **time_range** — dual time pickers с timezone awareness
  - **State & workflow**: **status** — visual state graph с current state highlighted, click allowed transition → confirmation modal с rationale prompt (если transition требует authority approval); **priority** — radio buttons с color/icon indicators; **rating** — star widget с hover-to-preview
  - **Hierarchies**: **tree_path** — tree picker (drill-down navigation) с breadcrumb display, drag-to-reorder в admin tree screen; **tags** — chip input с autocomplete по existing values, color labels
  - **Content**: **markdown** — split editor (Monaco raw + rendered preview) с formatting toolbar; **color** — hex input + color picker (chrome-style) + palette presets; **cron** — UI builder с natural language preview («Every Monday at 9 AM») + raw cron expression toggle; **qr_code** — value input + live QR preview (200×200) + format selector (PNG/SVG/PDF) + size/ECC controls + «Download» / «Print» / «Copy URL» actions; для `EncodeFrom(field)` показывает source field link и auto-renders при changes
- **Right sidebar**: metadata (id, created, updated, version); **History timeline** из audit log с before/after diff; **Related records** (back-references); **Active subscribers** на эту запись; computed fields preview; actions (clone, delete, run hook); **Documents tab** (если `.AllowsDocuments()`)
- **Realtime collision**: «Bob is editing this record»; на save при version mismatch — «merge or override?»
- **Validation**: zod schema из generated SDK; inline errors per field

### 4. Auth collections (users, sellers, etc.)

Special-mode для каждой auth-коллекции с extra tabs:

- **Sessions** (active с device/IP/last-activity, revoke)
- **Devices** (trusted, revoke, force re-2FA)
- **OAuth** (connected providers via external auth model)
- **2FA** (status, generate recovery codes, reset 2FA — admin override audited)
- **MFA** (configured factors, reset)
- **Roles** (assign/revoke; site + tenant scope)
- **Audit** (per-user trail)
- **Auth origins** (recent IPs, devices, locations)
- **Impersonate** (new tab под user'ом с banner и end-impersonation, всё audited)
- **Reset password** (link или email)
- Manual email verify

### 5. System admins

Доступ только system_admin (не readonly). Add admin → force-set-password-on-first-login. Manage admin roles. 2FA mandatory.

### 6. RBAC editor

- **Roles** tab: list (system + custom + tenant); per-role granted action keys
- **Action keys** tab: каталог (generated из code, read-only display) с descriptions
- **Matrix view**: roles × actions checkbox grid; bulk grant/revoke
- **User assignments**: per-user role view; bulk assign
- **Tenant scope** (multi-tenant): switcher для tenant context
- **Visual graph** of permission inheritance (с organizations plugin)

### 7. Schema viewer (read-only)

- **Visual graph**: collections как nodes, relations как edges (d3-force / ELK layout)
- **Per-collection** detail: fields с types, indexes, rules, hooks attached, computed fields
- **Migration history**: list applied migrations с timestamp/hash/applied-by; click → diff view
- **Drift indicator**: warning banner если Go-DSL hash != applied schema hash; migration generation hint
- **Export**: `--json` (для LLM) и `--sql` (raw migration)

### 8. Hooks editor

- File browser `pb_hooks/`
- Создать новый → choose template (onRecordCreate / route / cron)
- **Monaco**: JS syntax + format on save; auto-complete для `$app.*`/`$apis.*`/etc через embedded `.d.ts`; inline error markers; save → fsnotify reload (toast «hook reloaded in 200ms»)
- **Sidebar**: recent invocations с status; per-file metrics (invocations/min, p95 duration, error rate); console output panel (debug mode, live `console.log`)
- **Test panel**: «run with sample event» — pre-fill event с record from collection, see output
- Lock: только role с `hooks.write` permission редактирует

### 9. Realtime monitor

- Live tap событий брокера; filter by collection/topic/action/actor
- Per-event: payload, subscriber count delivered, latency
- **Active subscribers**: connection list (actor, IP, topics, queue depth, last activity); per-subscription metrics; **Kick** button с reason (audited)
- **Topic stats**: messages/sec, fan-out factor, average payload size

### 10. Jobs / Queue

Tabs: **Pending** (manual «run now»); **Running** (с progress); **Failed** (error trace + retry); **Cron** (next-run, pause/resume, manual trigger); **History**; **Metrics** (throughput, p95 duration, error rate per job kind).

### 11. Audit log viewer

- Search bar + filter UI: actor, event type, outcome, time range, tenant, IP, request_id
- Table: time / actor / event / outcome / target
- **Detail**: expanded view с before/after diff (JSON), full request context, related events (тот же request_id)
- **Hash chain verify** button (если sealing enabled)
- **Export**: filtered → CSV/JSON/XLSX
- Retention indicator

### 12. Files / Storage browser

Tree view per-collection; thumbnail grid для images, list для других. File detail: preview (image/pdf/video), metadata, signed URL generator с TTL chooser. Bulk: download as zip, delete, move. Storage usage stats (total, по коллекциям, по tenant).

### 13. Mailer

- **Templates** browser: list of `email_templates/*.md`; click → Monaco
- **Preview**: render с sample data side-by-side; live update on edit
- **Test send**: enter email → send rendered template; result toast
- **i18n variants**: tabs per language; missing variant → warning
- **SMTP config check**: «send test» button verifies config
- **History** (если send-log enabled): per-email status

### 14. Backups

- **Manual backup** button → progress bar → downloadable zip
- **Scheduled** tab: cron-config; next-run; retention policy
- **Backup files list**: stored backups (local или S3); size; date; download/delete
- **Restore** flow: upload или select → manifest preview (versions, schema hash) → red confirmation modal → progress
- Auto-upload to S3 toggle

### 15. Settings / Configuration (runtime-mutable)

Tabs:
- **Site**: name, logo, primary color (theming)
- **Auth providers**: per-provider OAuth config (client_id/secret через UI), WebAuthn settings, password policy, lockout settings
- **Mailer**: SMTP / SES / provider config; from address; rate limits
- **Storage**: driver, FS path или S3 creds, signed URL TTL default
- **Realtime**: max subscriptions; backpressure threshold; resume window
- **Jobs**: max concurrency; default timeout; retention
- **Hooks**: timeout, memory ceiling, recycle frequency
- **Rate limiting**: per-IP/user defaults; per-route overrides
- **Audit**: retention; sealing on/off
- **Tenant** (multi-tenant): defaults; quota templates
- **Apple Sign-In**: secret rotation (button «regenerate now»)
- **Advanced**: env-vars view (read-only, redacted); secrets rotation actions

Каждое изменение → audit row.

### 16. Logs viewer

Live tail slog stream через WS. Filter by level/source module/request_id/trace_id/time. Click log → expanded JSON. Search в historical logs (file rotation). Export filtered.

### 17. Health & Metrics

- Per-subsystem indicators
- **Built-in Prometheus dashboard** (не external Grafana): HTTP (rps, errors, p50/p95/p99); DB (query rate, slow queries, pool, locks); Realtime (subscriptions, events/sec, fan-out); Hooks (invocations, timeouts, OOM, recycles); Jobs (pending, running, failed); Storage (rw throughput); Mailer (sent/bounced/queued)
- Live charts: 1m / 5m / 1h / 24h windows
- Click metric → drill-down

### 18. API Tokens

Per-user management (admin видит all). Create: scope (action keys subset), expiration, name. Display once at creation («Save this — you won't see it again»). Revoke; rotation. Per-token audit.

### 19. Plugins (если plugin host installed)

Installed plugins list с version/status (running/stopped/crashed); per-plugin configure UI (mounted at `/_/plugins/{name}/`); install from registry (URL → manifest verify → download → restart prompt); health check; logs per-plugin.

### 20. Tenants (multi-tenant only)

Tenant list с stats (records, storage, members); per-tenant: settings overrides, member management, quota usage, audit scope, billing (если `railbase-billing` plugin); switch-as-admin (impersonation в tenant context).

### 21. Documents (core)

- Browser tree by collection (vendor → list of vendors → list of docs)
- Filter: category, owner, archived/active, mime, legal-hold, retention status
- Per-document detail: metadata, version timeline, current preview pane, access log, legal-hold toggle, retention-until editor
- Upload zone на каждой странице записи (Documents tab в record editor)
- Bulk: archive, change category, set legal hold, export (zip всех версий)
- Quota dashboard: usage по tenants, growth trend
- Cross-workspace view для system admin
- Per-document audit trail

### 21a. Hierarchical browsers (для коллекций с tree/DAG patterns)

Когда коллекция имеет hierarchy через любой из patterns (см. [03-data-layer.md](03-data-layer.md#hierarchical-data--4-patterns--dag)) — admin UI auto-mounts специальный tree view как альтернативу обычной grid:

#### Tree view (adjacency / materialized path / nested set)

- **Hierarchical layout**: expand/collapse nodes; lazy-load children (для больших trees)
- **Drag-drop reordering** (если `.Ordered()`): visual indicator drop zone, valid/invalid drops
- **Drag-drop move subtree**: drop node на другой parent → confirmation modal с preview affected children count + warning о cost (если materialized path)
- **Search-in-tree**: filter mode highlights matching nodes + auto-expands ancestors
- **Breadcrumb navigation**: current location в tree
- **Inline create child**: «+» button at each node opens new-record form pre-filled с parent
- **Bulk select**: multi-select children, bulk move/delete
- **Counts**: «Engineering (12)» — descendant count badge
- **Toggle между tree и flat grid view** (одна кнопка)

#### DAG view (multi-parent)

- **Graph visualization**: d3-force layout или ELK для structured layouts
- **Click node → focus mode**: highlight all paths to/from
- **Add edge**: drag from source node → target (с cycle prevention check)
- **Topological order view**: alternative linear render (build order)
- **Cycle detection alert**: badge если cycle detected

#### State machine view (для Status field)

- **Visual graph**: states как nodes, transitions как edges
- **Current record highlight**: green node = current state
- **Click transition edge**: triggers transition (с confirmation если требуется rationale)
- **Forbidden transitions**: greyed out edges
- **Audit overlay**: hover transition → recent transitions count + average time

### 22. Approvals (если `railbase-authority` plugin)

- **My Requests** tab: свои pending/approved/rejected requests с current step indicator
- **Pending Approvals** tab: requests где ты в chain step (или delegated); approve/reject UI с rationale
- **Policies editor**: visual builder — resource/action picker, condition editor (typed по schema field types), chain step editor (role picker drag-drop ordering)
- **Delegations**: outgoing/incoming, period management, revoke
- **Authority audit**: все decisions с filtering, on-behalf-of disclosure
- Time-to-approval metrics dashboard

## QDataTable (custom data grid)

Admin UI отказывается от TanStack Table и катит **собственный QDataTable**. Причины: tight coupling с Railbase SDK / field-type catalog / RBAC / realtime — сторонняя headless library требует слой адаптации, который сам по себе не меньше custom-реализации.

### Что заменяем

TanStack Table даёт headless «column model + row model + sort/filter/group/pagination state + plugins». QDataTable перепокрывает только то, что реально нужно admin UI; остальное **не реализуем**.

### Что включено в QDataTable

| Capability | Реализация |
|---|---|
| **Virtualization** | `@tanstack/virtual` (~3 KB; чистый windowing primitive — `useVirtualizer`); row + column virtualization |
| **Schema-driven columns** | Колонки строятся из generated SDK schema каждой коллекции; per-field-type renderer + editor берётся из `components/field-editors/` registry автоматически |
| **Server-driven state** | Sort / filter / pagination — **state на сервере**, не в клиенте. URL-synced query params; SDK `list({ filter, sort, cursor })` |
| **Filter UX** | Per-column popover, типизированный по field type (см. screen 2 описание) |
| **Sort** | Multi-column через shift-click; индикатор приоритета 1/2/3 в header |
| **Column configurable** | Show/hide/reorder (drag header), resize (drag border), persist в localStorage per-collection-per-admin |
| **Inline edit** | `Cmd+E` на ячейке → `<FieldEditor>` overlay; optimistic mutate через TanStack Query; revert on conflict |
| **Bulk select** | Header checkbox (current page / all matching filter); `Shift+Click` range select; sticky bulk-action toolbar при non-empty selection |
| **Realtime overlay** | Подписка на `collections.{name}.*` вшита в QDataTable: новые/обновлённые row'ы получают highlight 2s; deleted row'ы fade out |
| **Density** | comfortable / compact / dense через CSS variables на root |
| **Empty / error / loading** | Slots: `<QDataTable.Empty>`, `<QDataTable.Error>`, skeleton row на `loading` |
| **Sticky** | Sticky header + sticky first N columns (configurable, e.g. id + title) |
| **Footer aggregations** | Group totals для finance / currency / quantity columns в footer (per-currency для mixed) |
| **Keyboard navigation** | Arrow keys, `Enter` open row, `Cmd+E` edit, `Space` select, `j/k` next/prev (Linear-style) |

### Что **не** реализуем (отказ от TanStack Table features, которые нам не нужны)

- Client-side sorting / filtering / pagination (state всё равно на сервере — данные могут не помещаться в memory)
- Grouping / aggregation rows (это работа materialized view collections, не grid)
- Pivoting (Excel territory)
- Tree / sub-rows (для иерархий — отдельный `QTreeBrowser` компонент, см. screen 21a)
- Plugin architecture как у TanStack — все «features» — composition, не registered plugins
- Faceted filters (server возвращает option list для select-фильтров; не строим distinct-values на клиенте)

### API surface (sketch)

```tsx
import { QDataTable, useQDataTable } from "@/components/qdatatable"
import { sdk } from "@/lib/sdk"

function CollectionView({ name }: { name: string }) {
  const ctl = useQDataTable({
    collection: name,
    fetch: ({ filter, sort, cursor, limit }) =>
      sdk.collections[name].list({ filter, sort, cursor, limit }),
    realtime: sdk.realtime.subscribe(`collections.${name}.*`),
    persistKey: `grid:${name}`,            // localStorage namespace
  })

  return (
    <QDataTable controller={ctl}>
      <QDataTable.Toolbar>
        <QDataTable.SearchBox />
        <QDataTable.SavedFilters />
        <QDataTable.ColumnPicker />
        <QDataTable.DensityToggle />
        <QDataTable.BulkActions>
          <BulkExportXLSX /><BulkDelete /><BulkEditField />
        </QDataTable.BulkActions>
      </QDataTable.Toolbar>

      <QDataTable.Header sticky stickyColumns={["id", "title"]} />
      <QDataTable.Body
        renderRow={(row) => <QDataTable.Row record={row} />}
        emptyState={<EmptyHint collection={name} />}
      />
      <QDataTable.Footer>
        <QDataTable.Aggregations />
        <QDataTable.Pagination mode="cursor" />
      </QDataTable.Footer>
    </QDataTable>
  )
}
```

### Структура

```
components/qdatatable/
  index.ts                            # public exports
  QDataTable.tsx                      # composition root, context provider
  useQDataTable.ts                    # controller hook (state + fetch + realtime)
  parts/
    Header.tsx, Body.tsx, Footer.tsx
    Toolbar.tsx, SearchBox.tsx, ColumnPicker.tsx, DensityToggle.tsx
    Row.tsx, Cell.tsx
    Pagination.tsx, Aggregations.tsx
    BulkActions.tsx, SavedFilters.tsx
  filters/
    TextFilter.tsx, NumberFilter.tsx, DateFilter.tsx, EnumFilter.tsx,
    TelFilter.tsx, FinanceFilter.tsx, CurrencyFilter.tsx, AddressFilter.tsx,
    StatusFilter.tsx, TagsFilter.tsx, TreePathFilter.tsx, ...
  hooks/
    useColumnState.ts                 # show/hide/reorder/resize, persisted
    useRowSelection.ts                # bulk select state
    useRealtimeOverlay.ts             # subscribe → row highlight on changes
    useEditingCell.ts                 # Cmd+E inline edit lifecycle
  styles.css                          # CSS vars для density / sticky
```

### Цена / выигрыш

| Параметр | Custom QDataTable | TanStack Table v8 |
|---|---|---|
| Bundle | ~10 KB QDataTable + 3 KB virtual = **~13 KB** | ~30 KB core + ~3 KB virtual = **~33 KB** |
| Schema-driven columns из Railbase SDK | Native (по definition) | Через wrapper-слой ~5-10 KB |
| Filter UI per-field-type для всех 30+ типов | Built-in | Пишем сами поверх headless |
| RBAC-aware actions | Native | Wrapper |
| Realtime overlay built-in | Native | Wrapper |
| Long-tail features (grouping, faceting, pivoting) | Нет | Есть |
| Maintenance cost | На нас | TanStack-team поддерживает |

Trade-off: берём maintenance на себя, но не платим за features которыми всё равно не пользуемся, и не строим адаптер-слой между TanStack core и нашим SDK / RBAC / realtime / field-types.

---

## UX features (cross-screen)

- **Command palette (⌘K)**: jump to collection, search records globally, quick actions («create user», «backup now», «toggle dark mode»)
- **Global search (⌘/)**: full-text across FTS-indexed collections
- **Keyboard shortcuts**: navigation (`g+c` collections, `g+u` users), actions (`n` new, `e` edit, `⌘s` save), inspired by Linear/Vercel
- **Breadcrumbs** с click-back
- **Toast notifications** (success/error/loading)
- **Optimistic updates** с rollback on backend reject
- **Undo для destructive actions**: 5-сек window после delete
- **Realtime presence**: avatars в углу collection/record views; «Bob is here»
- **Dark / light theme** toggle, persists per-admin
- **Density toggle** comfortable/compact/dense (вся UI scales)
- **Empty states** с onboarding hints
- **Inline help** «?» icons → docs popover
- **Multi-tab support**: each tab — independent route state; shared SDK client (single WS per origin)

## First-run experience

После `railbase init && railbase serve` admin открывает `/_/`:

1. **Setup wizard**: create first admin (email + password + 2FA) → choose template hint (basic/saas/mobile/ai) → configure mailer (skip → console driver) → generate first OIDC client (skip)
2. **Tour**: 3-step intro показывающий collections, hooks, realtime
3. **Sample data offer**: «Load sample data?»

## Mobile / responsive

Tablet (768+): полная функциональность с adapted sidebar. Phone (<768): minimal mode (login + dashboard + audit log read; full editing — desktop). Backend admin, mobile-first не приоритет.

## Branding

- v1: site name, logo, primary color (через Settings)
- v2 plugin: full theming через CSS variables; white-label

## Что admin UI явно НЕ делает (фиксированный scope)

- **Schema editor с миграциями из UI** — конфликт с schema-as-code source-of-truth (read-only viewer; миграции генерятся через `railbase migrate diff`)
- **Bulk-actions across millions rows** — CLI или async export
- **Custom dashboards / report builder** — Retool/Metabase territory
- **Visual workflow / BPMN editor** — `railbase-workflow` plugin v2+
- **SQL query playground** — opt-in plugin `railbase-sql-playground` (raw SQL обходит RBAC)
- **Database admin (vacuum, indexes manual)** — CLI commands
- **Code editor для Go schema files** — IDE territory; в admin UI только JS hooks
- **Theming / white-label** в v1

## Структура admin UI

```
admin-ui/                           # отдельный Vite проект
  src/
    App.tsx                         # <Layout> + <Switch> с <Route> per page
    routes.ts                       # типизированные path-builders: routes.collection(name), routes.record(name, id)
    layout/
      AppLayout.tsx                 # sidebar + topbar wrapper
    pages/
      Dashboard.tsx
      Collection.tsx                # ?name через useParams (wouter)
      RecordEditor.tsx              # /collections/:name/records/:id
      AuthCollection.tsx            # /auth/:collection
      AuthUserDetail.tsx            # /auth/:collection/:id
      RBAC.tsx, RBACMatrix.tsx
      Hooks.tsx                     # /hooks/:file?
      Realtime.tsx, Audit.tsx, Jobs.tsx, Mailer.tsx, Backups.tsx,
      Settings.tsx, Health.tsx, Plugins.tsx, Tenants.tsx, Documents.tsx
    components/
      qdatatable/                   # QDataTable: custom virtualized grid + plugins (filter/sort/columns/edit)
      field-editors/                # per-type
      record-history/
      realtime-presence/
      command-palette/
      charts/                       # shadcn chart primitives over Recharts
    lib/
      sdk.ts                        # imports generated Railbase SDK (dogfooding)
      shortcuts.ts
      theme.ts
  vite.config.ts

internal/admin/
  embed.go                          # go:embed admin-ui/dist/*
  routes.go                         # serves SPA + admin-only API
  middleware.go                     # admin auth + per-screen RBAC
```

**`routes.ts`** centralizes path strings, чтобы links не разъезжались с pattern. Пример:

```ts
export const routes = {
  dashboard: () => "/",
  collection: (name: string) => `/collections/${name}`,
  record: (name: string, id: string) => `/collections/${name}/records/${id}`,
  auth: (col: string) => `/auth/${col}`,
  authUser: (col: string, id: string) => `/auth/${col}/${id}`,
  hook: (file?: string) => file ? `/hooks/${file}` : "/hooks",
  // ...
} as const

// Pattern strings (для wouter <Route path="...">) рядом, чтобы не дрейфовали:
export const patterns = {
  collection: "/collections/:name",
  record: "/collections/:name/records/:id",
  // ...
} as const
```

Use в компоненте:
```tsx
import { Route, Switch, useParams, useLocation } from "wouter"
import { patterns, routes } from "@/routes"

<Switch>
  <Route path={patterns.collection}>
    {(params) => <Collection name={params.name} />}
  </Route>
  <Route path={patterns.record}>
    {(params) => <RecordEditor collection={params.name} id={params.id} />}
  </Route>
</Switch>

// Navigation:
const [, navigate] = useLocation()
navigate(routes.record("posts", "abc123"))
```

Build: `cd admin-ui && bun run build` → `dist/` embedded; CI gate проверяет `dist/` коммитится в release.

## Plugin extension API

Plugins могут регистрировать свои screens. В v1.1 — через iframe (simple, sandboxed):

```
plugins/railbase-billing/admin/      # plugin's React app
  index.html
  app.js
```

Mounted at `/_/plugins/billing/`. Iframe-isolated, communicates с parent через postMessage.

В v2 возможно module federation для seamless integration (но complex).

# 12 — Admin UI

Главный touchpoint между Railbase и разработчиком/оператором. Не пытается победить Retool, но должен покрыть всё, что делает PB admin (с улучшениями).

## Tech stack

- **Preact 10** + **TypeScript** (strict) — выбран вместо React 19 ради bundle size + fine-grained reactivity. React-only зависимости (TanStack Query, Monaco editor wrapper) работают через `preact/compat` alias в `vite.config.ts` + `tsconfig.paths` — наш собственный код импортит `preact` / `preact/hooks` / `@preact/signals` напрямую.
- **@preact/signals** — реактивные ячейки вместо React Context для глобального state (auth state, settings) + `useSignal()` в формах вместо `useState` (fine-grained: re-render только подписчиков, не всего дерева).
- **Tailwind 4** через `@tailwindcss/vite` plugin + **tw-animate-css** для shadcn-style entry/exit animations.
- **wouter-preact** для routing (~1.5 KB, hook-based, minimal API) + **TanStack Query** (data + cache; через preact/compat).
- **shadcn-on-Preact UI kit** — собственный port shadcn/ui под Preact живёт в `admin/src/lib/ui/`: 50 компонентов (Button/Card/Input/Table/Dropdown/Select/Command/Sheet/…) + 11 Radix-replacement primitives (Portal, FocusScope, DismissableLayer, Popper, …) + hand-rolled icon set в `icons.tsx`. **Тот же kit железно зашит в Railbase-бинарь и раздаётся downstream-приложениям** — см. секцию "Shareable UI kit" ниже.
- **class-variance-authority** + **clsx** + **tailwind-merge** — CVA pattern для variant-based компонентов; `cn()` хелпер в `lib/ui/cn.ts` собирает результат.
- **@floating-ui/dom** — backend для `_primitives/popper.tsx` (positioning Select / Popover / DropdownMenu / Tooltip без Radix).
- **react-hook-form** + **@hookform/resolvers** + **zod** — каноничный form pattern через kit-овский `form.ui.tsx`. `login.tsx` — reference-implementation в admin'e. См. секцию **Form strategy** ниже.
- **react-day-picker** — peer-dep для kit-овского `calendar.ui.tsx`.
- **embla-carousel** — peer-dep для kit-овского `carousel.ui.tsx`.
- **Monaco editor** для hooks JS / JSON / SQL view queries — **lazy-loaded** через `lazy(() => import("@monaco-editor/react"))` + `<Suspense>` — не входит в основной bundle, грузится только на `/hooks` экране.
- **Tiptap** для RichText field editor (через preact/compat).
- **Recharts** для dashboard графиков (через preact/compat). Lazy-loaded chunk на screens #1 Dashboard + #17 Health — не входит в main bundle. Цвета series задаются явными hex/oklch значениями из `lib/ui/theme.ts`; ESLint правило `no-hardcoded-tw-color` имеет explicit escape для Recharts axis/series.
- **Geist Variable** (`@fontsource-variable/geist`, ~35 KB) — кросс-платформенный sans, близкий по метрикам к shadcn/Vercel reference. Self-hosted (фолбэк system-ui остаётся в `font-family` stack). Загружается через `admin/src/styles.css` `@import "@fontsource-variable/geist"`. Не упоминается в kit-распространении — `lib/ui/styles.css` остаётся font-agnostic, downstream-приложения подключают собственный шрифт.
- **Command palette** (⌘K) реализован hand-rolled в `layout/command_palette.tsx` — не используем kit-овский `<Command>` чтобы не переписывать keyboard-nav логику.
- **Vite 5** build (`@preact/preset-vite` plugin) → один `dist/` → `go:embed` в бинарь.
- **Использует тот же TS SDK**, что Railbase генерирует для пользователей — dogfooding (если что-то сломалось в SDK, admin UI перестаёт работать).

**Зачем Preact**: после миграции с React 19 main-bundle gzip сократился с 132 KB → **79 KB** (−40%). Lazy-load Monaco отделил ещё ~50 KB JS chunk загружающийся только когда оператор открывает `/hooks`. Signals дают per-field rerender вместо whole-form rerender (заметно на больших record editor'ах с 30+ полями). React 19 + react-dom (~45 KB gzip) заменены на preact (~10 KB) + preact/compat shim (~5 KB) для React-only зависимостей.

**Заметка про routing**: wouter-preact сознательно отказывается от typed-routes / file-based-routing / nested-loaders в пользу size & simplicity. Для admin UI это приемлемо — данные грузим через TanStack Query (где есть caching, deduplication, mutations), а не через router loaders. Type-safety для path params живёт в helper `routes.ts` (см. ниже), не в router-runtime.

**Build pipeline notes** (для maintainers):
- `vite.config.ts` маппит `react → preact/compat`, `react-dom → preact/compat`, `react-dom/test-utils → preact/test-utils`, `react/jsx-runtime → preact/jsx-runtime`.
- `tsconfig.json` имеет `jsxImportSource: "preact"` + `paths.react → ./node_modules/preact/compat` для type-resolution.
- Native Preact JSX types: handlers используют `e.currentTarget.value` (не `e.target.value`) и HTML-style attrs (`spellcheck`, не React's `spellCheck`).
- `main.tsx` использует `preact.render(<App/>, root)` вместо React's `createRoot(root).render(...)`. `<StrictMode>` не существует в Preact и не нужен.

Размер: после миграции на shadcn-on-Preact kit main bundle **~115 KB gzip / ~422 KB raw** (был 79 KB / 307 KB до использования kit-овских компонентов — расход ~35 KB gzip оправдан тем, что kit полностью кроет UI surface всех 24+ экранов и одновременно раздаётся downstream-приложениям). Target ≤ 3 MB gzipped — большой запас на будущие фичи.

## Shareable UI kit (`admin/src/lib/ui/`)

**Источник правды для UI** — каталог `admin/src/lib/ui/`. Тот же код, что использует Railbase admin, **раздаётся через бинарь** любому downstream-приложению — пользователи Railbase получают готовый Preact-port shadcn/ui «бесплатно» вместе с бэкендом.

### Структура

```
admin/src/lib/ui/
├── *.ui.tsx            ← 50 компонентов (button, card, input, table, dialog, …)
├── _primitives/        ← 11 Radix-replacement modules
│   ├── slot.tsx          asChild composition
│   ├── portal.tsx        DOM portal через createPortal
│   ├── popper.tsx        @floating-ui/dom wrapper
│   ├── focus-scope.tsx   focus trap для модалок
│   ├── dismissable-layer.tsx  click-outside + Escape
│   ├── presence.tsx      enter/exit animations
│   ├── visually-hidden.tsx
│   ├── collection.ts
│   ├── use-controllable.ts
│   ├── use-id.ts
│   └── index.ts
├── cn.ts               ← cn() = twMerge(clsx(...)) helper
├── icons.tsx           ← hand-rolled SVG icon set (без lucide зависимости)
├── theme.ts            ← light/dark toggle
└── index.ts            ← barrel export + source-of-truth doc
```

### Контракт

1. **Этот каталог = source of truth.** Admin сам импортит компоненты через `@/lib/ui/<name>.ui`; downstream-приложения копируют те же файлы в свой `src/lib/ui/` через CLI/HTTP.
2. **Никаких ссылок наружу.** Файлы под `admin/src/{auth,api,fields,layout,screens}/` — admin-app-private, **не уезжают** с kit'ом. Любой компонент, который читает application state, **не принадлежит** kit'у.
3. **App-specific composites** (типа air-овского `QEditableForm`) живут в `admin/src/screens/` или в `_composites/` под экраном-владельцем, **не в `lib/ui/`**.

### Раздача downstream-приложениям

Бинарь embed'ит kit через `admin/uikit.go` (`//go:embed src/lib/ui src/styles.css`) и сервит его двумя путями:

**HTTP** (для browser-only сценариев — air-gapped registry):

| Endpoint | Что возвращает |
|---|---|
| `GET /api/_ui/manifest` | Полный граф: компоненты + примитивы + peer deps + onboarding notes |
| `GET /api/_ui/registry` | shadcn-shaped список (name + peers) |
| `GET /api/_ui/components/{name}` | JSON с метой и source |
| `GET /api/_ui/components/{name}/source` | raw .tsx |
| `GET /api/_ui/primitives/{name}` | raw _primitives/<name>.{ts,tsx} |
| `GET /api/_ui/cn.ts` | cn() helper |
| `GET /api/_ui/styles.css` | theme tokens (oklch) + tw-animate-css import |
| `GET /api/_ui/peers` | `npm install <peers>` (или JSON с `Accept: application/json`) |
| `GET /api/_ui/init` | Long-form onboarding (vite/tsconfig snippets) |

Endpoints **public** (без auth) — это published-source component code, эквивалент CDN-fetch'a.

**CLI** (offline, transitive-dep aware, атомарный multi-file copy):

```bash
railbase ui list             # 50 components, 11 primitives
railbase ui peers            # npm install line
railbase ui init [--out X]   # scaffold styles.css + cn.ts + icons.tsx + theme.ts + _primitives/*
railbase ui add NAME...      # copy specific components (resolves transitive local deps)
railbase ui add --all        # everything
```

Транзитивное разрешение: `ui add password` подтянет `input` (потому что `password.ui.tsx` импортит `./input.ui`); `ui add form` подтянет `label`. Algorithm — BFS по `Local[]` метаданным до закрытия set'a.

### Регистрационная инфраструктура (Go)

| Файл | Роль |
|---|---|
| `admin/uikit.go` | `//go:embed all:src/lib/ui src/styles.css`, экспорт `UIKit() fs.FS` |
| `internal/api/uiapi/registry.go` | boot-time scan: распарсивает `from '<pkg>'` импорты, классифицирует peers / primitives / local siblings, строит `Manifest` |
| `internal/api/uiapi/handler.go` | 10 chi-handler'ов |
| `pkg/railbase/cli/ui.go` | cobra-команды `ui list/peers/init/add` поверх той же `uiapi` |

Манифест строится один раз через `sync.Once` — FS immutable на process lifetime. `cache.Register("uiapi.manifest", ...)` НЕ wired (не hot-path).

### Покрытие 30 MB binary-size budget

Embed'инг ~500 KB TSX source добавляет ~250 KB к бинарю (некоторый overlap сжимается в embed.FS). Все 6 cross-compile targets укладываются в 30 MB ceiling (largest ~27.7 MB, headroom ~2.3 MB). Размер verified через `make check-size` после каждого изменения.

### Test coverage

`internal/api/uiapi/registry_test.go` — 11 тестов под `-race`:
- классификация импортов (relative `./foo` + alias `@/lib/ui/foo` — обе формы)
- транзитивное разрешение local siblings
- kit-base файлы (`cn` / `icons` / `theme` / `index`) НЕ попадают в Local
- primitive deps (`@floating-ui/dom`) попадают в общий peer set
- handler-тесты: manifest / source / 404 / peers (JSON vs text)
- nil-FS path (dev/test) → пустой манифест без panic

## Form strategy

Канонический form pattern в admin'e — **kit's `<Form>` + react-hook-form + zod**. `form.ui.tsx` импортит `react-hook-form` и оборачивает RHF в shadcn-shaped компоненты (`<Form>`, `<FormField>`, `<FormItem>`, `<FormLabel>`, `<FormControl>`, `<FormDescription>`, `<FormMessage>`). Это **тот же** паттерн, который downstream-приложения получают через `railbase ui add form`.

### Reference implementation: `admin/src/screens/login.tsx`

```tsx
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage } from "@/lib/ui/form.ui";
import { Input } from "@/lib/ui/input.ui";

const schema = z.object({
  email: z.string().email("Enter a valid email"),
  password: z.string().min(1, "Password required"),
});
type Values = z.infer<typeof schema>;

export function LoginScreen() {
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { email: "", password: "" },
    mode: "onSubmit",
  });

  async function onSubmit(values: Values) {
    /* … */
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} class="space-y-4">
        <FormField
          control={form.control}
          name="email"
          render={({ field }) => (
            <FormItem>
              <FormLabel>Email</FormLabel>
              <FormControl><Input type="email" {...field} /></FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        {/* ... */}
      </form>
    </Form>
  );
}
```

### Что даёт паттерн (vs hand-rolled signals + manual onSubmit)

| Концерн | RHF + form.ui | Hand-rolled signals |
|---|---|---|
| Field-level error display | `<FormMessage>` рендерит zod-error автоматически | Ручная state |
| ARIA wiring (id, aria-describedby, aria-invalid) | Авто через `<FormItem>` + `<FormControl>` | Ручная |
| Dirty/touched/isValid tracking | Из коробки `form.formState.*` | Не из коробки |
| Validation timing | `mode: "onSubmit" \| "onChange" \| "onBlur" \| "all"` | Ручная |
| Per-keystroke re-render scope | Только подписчик field (через RHF) | Только подписчик `.value` (через signals) |
| Server-error mapping | `form.setError(field, ...)` на 422 → field-level UI | Ручная state + ручной JSX |

### Preact compat note

RHF полагается на `onChange`-per-keystroke. Preact-native `onChange` срабатывает по blur (HTML-стандарт), что сломало бы RHF — но **`preact/compat` патчит** `onChange → oninput` на vnode-build уровне для `<input>` и `<textarea>` (исключая checkbox/radio/file типы). Verified в `node_modules/preact/compat/dist/compat.mjs::e.vnode` — регулярка вокруг `"onchange" → "oninput"`. Так что `{...field}` spread на `<Input>` работает идентично React.

### Когда `<Form>` ИЗБЫТОЧЕН (можно signals + manual onSubmit)

- **Transient UI state**, не привязанная к submit: filter inputs в list-views, search-as-you-type, modal open/close, busy-флаги, отображаемые server-errors. Это **не форма**, это эфемерное UI-состояние — `useSignal()` подходит.
- **Однополочные toggle'ы без validation**: «show deleted records», «expand row». Чистый `useSignal<boolean>(false)`.
- **Inline-edit cells в таблицах**: текущая реализация в `records.tsx` (компактный inline-input в ячейке) — signals остаются.

### Когда `<Form>` ОБЯЗАТЕЛЕН (RHF + zod)

- **Любая submit-форма с client-side validation** (login, signup, profile-edit, settings, password-change)
- **Multi-step wizards** (bootstrap ✅ v1.7.41)
- **Dynamic field-driven forms** (`record_editor.tsx` — будущая миграция; dynamic zod schema + per-field server-error mapping на 422)
- **Create-modals в list-views** (api_tokens.tsx, webhooks.tsx — будущая миграция)

### Cost

RHF + @hookform/resolvers + zod-validation добавляют ~**+12 KB gzip / +37 KB raw** в bundle (177 → 237 modules). Платится ONE-TIME первой формой; последующие form-screens растят bundle только на размер собственного кода (по ~1-2 KB gzip каждый).

### Migration status — все целевые формы переписаны (v1.7.41)

| Файл | Pattern |
|---|---|
| `login.tsx` ✅ | Static zod schema (email + password) — reference implementation |
| `bootstrap.tsx` ✅ | TWO `useForm()` (по одной на step) + discriminated union по `driver` + `.refine` для confirm-match + `setValue` fan-out в onGenerate |
| `record_editor.tsx` ✅ | Dynamic zod schema через `buildSchema(fields)` + 422-маппинг на `form.setError` из `err.body.details.errors` |
| `api_tokens.tsx` ✅ | Create-modal + chained `useState` для display-once token banner |
| `webhooks.tsx` ✅ | Create-modal + `.refine` для http(s) URL validator |
| `notifications-prefs.tsx` ✅ | Per-user settings form (quiet_hours + digest); master-detail toggle-grid остаётся `useState` |
| `settings.tsx` ✅ | KV add-form + `<select>` для типа + coercion в onSubmit с `form.setError("value")` на parse-failure |
| `i18n.tsx` ✅ | Dynamic zod через `useMemo(() => z.object(shape), [rows])`; flat-key cast `name={k as string}` обходит RHF auto-nested-paths для ключей с точками |
| `hooks.tsx` TestPanel ✅ | 3 поля + `z.string().refine` для JSON-object validation |

### Pattern catalogue

**1) Static zod schema** (`login.tsx`, `api_tokens.tsx`, `webhooks.tsx`, `settings.tsx`, `hooks.tsx`)

```tsx
const schema = z.object({ /* fields */ });
type Values = z.infer<typeof schema>;
const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: {...}, mode: "onSubmit" });
```

**2) Dynamic zod schema** (`record_editor.tsx`, `i18n.tsx`)

```tsx
const schema = useMemo(() => {
  const shape: Record<string, z.ZodTypeAny> = {};
  for (const f of fields) shape[f.name] = z.string();   // или per-field shape
  return z.object(shape);
}, [fields]);
```

**3) Discriminated union** (`bootstrap.tsx` DB step)

```tsx
const dbStepSchema = z.discriminatedUnion("driver", [
  z.object({ driver: z.literal("local_socket"), socket_dir: z.string().min(1), ... }),
  z.object({ driver: z.literal("external_dsn"), external_dsn: z.string().regex(/^postgres/) }),
]);

const driver = form.watch("driver");
{driver === "local_socket" && <FormField name="socket_dir" ... />}
```

Important: switching driver via radio калит `form.reset({ driver: ..., ...defaults })`, **не** просто `setValue("driver", ...)` — иначе другая ветка union'a не пройдёт валидацию.

> **v1.7.41-followup**: третья ветка union'а — `z.literal("embedded")` — была удалена. Embedded postgres теперь чисто build-time decision (`-tags embed_pg` через `make build-embed`); production-бинарник никогда не предлагает embedded оператору. Wizard видит только две ветки: local socket / external DSN.

**4) Cross-field validation** (`bootstrap.tsx` AdminStep)

```tsx
z.object({ password: z.string()..., confirm: z.string() })
  .refine((d) => d.password === d.confirm, { message: "Passwords don't match", path: ["confirm"] });
```

Ошибка рендерится в `<FormMessage>` под `confirm`-полем благодаря `path: ["confirm"]`.

**5) Server-error mapping (422 → field-level)** (`record_editor.tsx`, `notifications-prefs.tsx`, `i18n.tsx`)

```tsx
function handleSubmitError(e: unknown) {
  if (isAPIError(e)) {
    const fields = (e.body.details as { errors?: Record<string, string> } | undefined)?.errors;
    if (fields) {
      for (const [name, msg] of Object.entries(fields)) {
        if (knownFields.has(name)) form.setError(name as never, { type: "server", message: msg });
      }
      return;
    }
  }
  // Fallback: form-wide error in root.serverError или transient useState
  form.setError("root.serverError", { type: "server", message: "..." });
}
```

**6) Generator fan-out** (`bootstrap.tsx` AdminStep)

```tsx
<PasswordInput
  value={field.value}
  onInput={(e) => field.onChange(e.currentTarget.value)}
  showGenerate
  onGenerate={(p) => {
    form.setValue("password", p, { shouldValidate: true });
    form.setValue("confirm", p, { shouldValidate: true });
  }}
/>
```

`shouldValidate: true` запускает schema-check немедленно — generator-сгенерированный пароль сразу зажигает strength-bar + clears any prior validation error.

### `@hookform/resolvers` v5 — known gotcha

zod-`.default(value)` разводит input/output типы у схемы: input получает `field?: T | undefined`, output — `field: T`. `Resolver<...>` ожидает их идентичности и ломается. Workaround: НЕ использовать `.default()` в zod-schema, ставить дефолт через `useForm({ defaultValues: {...} })` + `form.reset({...})` после загрузки данных.

### Bundle cost summary (после полной миграции)

| Snapshot | Modules | Raw | Gzip |
|---|---|---|---|
| До kit'a (v1.7.40 start) | 1695 | 307 KB | 79 KB |
| После kit migration (24 экрана) | 174 | 422 KB | 115 KB |
| После RHF migration (9 forms) | 237 | 480 KB | 133 KB |
| + Geist Variable font | ~238 | ~515 KB | ~138 KB |
| + Recharts (lazy chunk на #1/#17) | ~245 main / +1 lazy | main ~480 KB + lazy ~155 KB | main ~138 KB + lazy ~45 KB |
| Δ от kit-only до full-form+font+viz | +71 main | +93 KB main + 155 KB lazy | +23 KB main + 45 KB lazy |

Main bundle target: ≤ 145 KB gzip. Lazy chunk (Recharts) грузится только при открытии Dashboard / Health и кеш-делится между ними. Бюджет проекта (`docs/18-risks-questions.md`): 3 MB gzip — запас порядка 20×. CI должен фейлиться если main bundle уходит выше 145 KB (regression-guard добавляется в Wave 4).

## Authentication & access

- Только **system admins** (не application users) имеют доступ
- Login на `/_/login` — email + password + 2FA (mandatory с v1)
- Failed-login lockout: 5 attempts → 15 min lockout по IP+email
- Session timeout: 8 hours sliding window (refresh on activity)
- Все admin actions audited в `_audit_log` с маркером `actor_type=system_admin`
- Per-screen RBAC: `system_readonly` видит, `system_admin` изменяет

## Layout

### ADR (v0.9) — IA reorg to PocketBase-style 3-tab header

Pre-v0.9 the sidebar grouped 28 screens into 6 labeled sections (Data /
Auth / Operations / Observability / Messaging / System). Operationally
this worked but the sidebar grew until it was unbrowsable at a glance.

v0.9 adopts the PocketBase mental model:

1. **Three top tabs** in the header — **Data / Logs / Settings** — own
   the conceptual split between (a) writable data, (b) read-only
   observability, (c) configuration.
2. **Context-aware sidebar** that swaps contents based on the active
   top tab. Single `SidebarProvider` keeps state (collapse, schema
   query cache) across tab transitions.
3. **Dashboard** moves off the sidebar — accessed by clicking the
   "Railbase admin" logo. Matches PocketBase, which has no dashboard
   widget in its sidebar at all.
4. **System tables** (`_api_tokens`, `_admins`, `_admin_sessions`,
   `_sessions`, `_jobs`) surface under the Data sidebar's collapsible
   System group at `/data/_xxx`. Each maps to its existing specialized
   screen via a dispatch in `records.tsx`.
5. **Search input** filters the Data sidebar's collections list
   (PocketBase-style live filter).

All pre-v0.9 URLs continue to work via redirects registered in
`app.tsx`. Bookmark + external-link continuity is preserved.

### URL redirects (pre-v0.9 → v0.9)

| Old | New |
|---|---|
| `/audit` | `/logs/audit` |
| `/logs` | `/logs/app` |
| `/realtime` | `/logs/realtime` |
| `/health` | `/logs/health` |
| `/cache` | `/logs/cache` |
| `/notifications` | `/logs/notifications` |
| `/notifications/prefs` | `/settings/notifications` |
| `/email-events`, `/mailer/events` | `/logs/email-events` |
| `/mailer-templates`, `/mailer/templates` | `/settings/mailer/templates` |
| `/mailer` | `/settings/mailer` |
| `/webhooks` | `/settings/webhooks` |
| `/backups` | `/settings/backups` |
| `/hooks` | `/settings/hooks` |
| `/i18n` | `/settings/i18n` |
| `/trash` | `/settings/trash` |
| `/api-tokens` | `/data/_api_tokens` |
| `/system/admins` | `/data/_admins` |
| `/system/admin-sessions` | `/data/_admin_sessions` |
| `/system/sessions` | `/data/_sessions` |
| `/jobs` | `/data/_jobs` |

### Visual layout (v0.9+)

```
┌───────────────────────────────────────────────────────────────┐
│ [Railbase admin]   Data · Logs · Settings              ⌘K  ▾ │
├──────────────┬────────────────────────────────────────────────┤
│  (sidebar)   │                                                │
│              │             Main content area                  │
│  contents    │                                                │
│  depend on   │                                                │
│  active tab  │                                                │
│              │                                                │
│ user@…       │                                                │
│ [Sign out]   │                                                │
└──────────────┴────────────────────────────────────────────────┘
```

**Tab: Data** (path `/data/*`, `/`, `/schema`)

```
[View schema]
[Search collections… ]
Collections
  posts
  users
  …
▸ System            ← collapsible (collapsed by default)
   _api_tokens
   _admins
   _admin_sessions
   _sessions
   _jobs
```

**Tab: Logs** (path `/logs/*`)

```
Audit
App logs
Realtime
Health
Cache
Email events
Notifications
```

**Tab: Settings** (path `/settings/*`)

```
General
Mailer
  Templates
Notifications
Webhooks
Backups
Hooks
Translations
Trash
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

После `./railbase serve` (без `init`, без env-переменных) admin открывает `/_/`:

1. **Setup wizard** (`bootstrap.tsx`) — двухшаговый: **(1) Database** → **(2) Admin account**.

   **Step 1: Database** (v1.7.39 архитектура + v1.7.41 form-strategy + v1.7.42 safety gate):
   - `GET /api/_admin/_setup/detect` detect'ит локальные PG сокеты (Homebrew `/tmp`, Debian/Ubuntu/Fedora/RHEL `/var/run/postgresql`) + suggested username из `$USER`.
   - Driver picker: **Local socket** (zero-password peer/trust auth) или **External DSN** (Supabase / Neon / RDS / self-hosted host). Embedded postgres НЕ предлагается — это build-time decision через `-tags embed_pg`.
   - `POST /api/_admin/_setup/probe-db` делает безопасный `SELECT version()` round-trip + читает `pg_tables` для **foreign-DB safety scan**:
     - `is_existing_railbase=true` (нашли `_migrations` marker) → зелёный banner «Existing Railbase instance detected», Save разрешён.
     - `public_table_count>0 && !is_existing_railbase` → **жёлтый banner с чекбоксом**: «This database is not empty. Found N tables in `public` schema, but none is a Railbase marker.» Save заблокирован пока оператор не подтвердит «I understand — install Railbase alongside the existing tables.»
     - empty DB → нейтральный поток, Save сразу доступен.
   - `POST /api/_admin/_setup/save-db` опционально создаёт целевую БД (`CREATE DATABASE` против `postgres` admin DB на том же сервере), пишет `<DataDir>/.dsn` (0600), сигналит in-process reload — wizard поллит `/readyz` и сам refresh'ит страницу.

   **Step 2: Admin account**: email + password + confirm. Password generator (dice icon) fan-out'ит в оба поля через `form.setValue("password" | "confirm", p, { shouldValidate: true })`. `.refine` cross-field на confirm-match. После успеха session cookie выдан + redirect на `/`.

2. **Tour** (📋 v1.x-bonus): 3-step intro показывающий collections, hooks, realtime
3. **Sample data offer** (📋 v1.x-bonus): «Load sample data?»

> **Safety: foreign-DB protection** (v1.7.42). Wizard-уровень — это первый слой защиты от типового оператор-faux-pas «опечатался в имени БД → попал в чужую прод-БД». Второй слой живёт в `internal/db/migrate.Runner.checkForeignDatabase`: при boot'е если в `public` есть таблицы И нет `_migrations` marker'а — миграции отказываются стартовать с `ErrForeignDatabase`. Escape hatch: `RAILBASE_FORCE_INIT=1`. Покрывает случай ручного редактирования `.dsn`, который wizard миновал бы. Деструктивных операций (`DROP DATABASE`, `DROP TABLE`, `TRUNCATE`) в setup-коде нет ни на одном пути.

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

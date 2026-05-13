# 11 — Frontend SDK

Главное преимущество над PocketBase: **full type-safety от схемы до клиента**. PB SDK runtime-only, Railbase — typed end-to-end.

## Что генерирует `railbase generate sdk`

```
client/
  index.ts                     # createRailbaseClient({ baseURL, auth })
  types.ts                     # все коллекции → TS interfaces (источник истины)
  zod.ts                       # zod-схемы для runtime-валидации
  collections/
    posts.ts                   # typed list/get/create/update/delete/subscribe
    users.ts
    ...
  realtime.ts                  # typed subscribe with collection/topic constraints
  errors.ts                    # ошибки с discriminated unions
  auth.ts                      # signin/signup/oauth/2fa typed flows
  documents.ts                 # documents API (если используется)
  exports.ts                   # exports API
  _meta.json                   # schema hash для drift detection
```

## Использование (целевая поверхность)

```ts
import { createRailbaseClient } from "./client"
const rb = createRailbaseClient({ baseURL: "http://localhost:8095" })

// Полностью типизированно — поля, фильтры, expand, sort
const posts = await rb.collections.posts.list({
  filter: { status: "published" },           // typed enum
  expand: ["author"],                         // typed relation
  sort: ["-created"],
})
posts.items[0].author.email                  // expanded type inferred

// Realtime subscriptions с типами событий
const sub = rb.collections.posts.subscribe("*", (e) => {
  if (e.action === "create") e.record.title  // typed
})

// Auth flows — typed
const session = await rb.auth.users.signin({ email, password })
await rb.auth.users.requestPasswordReset({ email })

// OAuth — typed providers
const url = rb.auth.users.oauth2.google.authURL({ redirectTo: "/welcome" })
const session = await rb.auth.users.oauth2.google.callback({ code })

// Errors — discriminated unions
try {
  await rb.collections.posts.create({...})
} catch (e) {
  if (e.code === "validation") {
    e.details.field  // typed
  }
}
```

## Принципы

### 1. Framework-agnostic core SDK

Без React/Vue/Svelte интеграций в v1. Чистый TS + zod, работает с любым state-management (TanStack Query, SWR, Solid Resource, Svelte stores, RxJS, Redux).

### 2. Examples в docs (не публикуемые пакеты)

TanStack Query / SWR / Solid Resource / Svelte стора — copy-paste sniples в docs. Это снимает churn от drift версий React/etc.

### 3. Drift detection через `_meta.json`

```json
{
  "schemaHash": "sha256:abc123...",
  "generatedAt": "2026-05-09T10:00:00Z",
  "railbaseVersion": "1.0.0"
}
```

Client при инициализации сравнивает с сервером и предупреждает в dev:
```
⚠ SDK schema drift detected. Server schema: sha256:def456..., SDK: sha256:abc123...
  Run `railbase generate sdk` to regenerate.
```

### 4. Multi-language (v1.2+)

`--lang ts | swift | kotlin | dart`

- **TS** (v1) — primary, polished
- **Swift** (v1.2) — для iOS native apps
- **Kotlin** (v1.2) — для Android native apps
- **Dart** (v1.2) — для Flutter
- **Python** (v2) — для server-to-server, AI/ML
- **Rust** (v2 community) — если кто-то контрибьютит

### 5. LLM-friendly TS

JSDoc на каждом методе, явные параметры (не `Partial<...>`-soup), комментарии-примеры. Это и для людей, и для агентов.

### 6. PocketBase compat layer

`pocketbase` npm package работает в `strict` mode без изменений. Native клиент даёт типы поверх.

## Auth API surface

```ts
// Per auth-collection (если несколько: users, sellers, etc.)
rb.auth.users.signin(...)
rb.auth.users.signup(...)
rb.auth.users.signout()
rb.auth.users.refresh()
rb.auth.users.requestVerification(email)
rb.auth.users.confirmVerification(token)
rb.auth.users.requestPasswordReset(email)
rb.auth.users.confirmPasswordReset(token, newPassword)
rb.auth.users.requestEmailChange(newEmail)
rb.auth.users.confirmEmailChange(token, password)
rb.auth.users.requestOTP(identifier, channel)   // email | sms
rb.auth.users.confirmOTP(otpId, code)
rb.auth.users.oauth2.google.authURL({...})
rb.auth.users.oauth2.google.callback({code})
rb.auth.users.webauthn.registerStart()
rb.auth.users.webauthn.registerFinish(credential)
rb.auth.users.webauthn.signinStart()
rb.auth.users.webauthn.signinFinish(credential)
rb.auth.users.totp.enable(secret)
rb.auth.users.totp.disable(code)
rb.auth.users.totp.verify(code)
rb.auth.users.devices.list()
rb.auth.users.devices.revoke(deviceId)
rb.auth.users.sessions.list()
rb.auth.users.sessions.revoke(sessionId)
rb.auth.users.externalAuths.list()
rb.auth.users.externalAuths.disconnect(provider)

// Auth methods discovery (для динамического UI)
const methods = await rb.auth.users.methods()
// → { password: {...}, oauth2: [...], otp: {...}, mfa: {...} }
```

## Collections API surface

```ts
rb.collections.posts.list({ filter, sort, expand, page, perPage })
rb.collections.posts.list({ filter, sort, expand, limit, after })  // cursor pagination
rb.collections.posts.get(id, { expand })
rb.collections.posts.create(data, options)
rb.collections.posts.update(id, data, options)
rb.collections.posts.delete(id)
rb.collections.posts.subscribe(target, callback, options)

// Batch
rb.batch([
  rb.collections.posts.create({...}).build(),       // .build() для batch
  rb.collections.users.update("u1", {...}).build(),
])

// Files
rb.collections.posts.uploadFile(id, fieldName, file)
rb.collections.posts.fileURL(record, fieldName, options)  // returns signed URL
rb.collections.posts.fileURL(record, fieldName, { thumb: "100x100" })
```

## Documents API

```ts
rb.documents.upload({ ownerType, ownerId, title, file })
rb.documents.list({ ownerType, ownerId, includeArchived })
rb.documents.get(id)
rb.documents.versions(id)
rb.documents.uploadVersion(id, file)
rb.documents.download(id, versionNo?)            // returns Blob
rb.documents.preview(id, versionNo?)
rb.documents.archive(id)
rb.documents.restore(id)
rb.documents.search(query)
rb.documents.quota()

// Collection-scoped
rb.collections.vendors.documents(vendorId).list()
rb.collections.vendors.documents(vendorId).upload({ title, file })
```

## Exports API

```ts
// Sync export (small datasets)
const blob = await rb.collections.posts.exportXLSX({ filter, columns })
const blob = await rb.collections.posts.exportPDF({ filter, template })

// Async export (large datasets — auto-routed через jobs)
const job = await rb.exports.start("posts", { format: "xlsx", filter })
await rb.exports.wait(job.id)                    // poll или WebSocket
const blob = await rb.exports.download(job.id)
```

## Error handling

```ts
type RailbaseError =
  | { code: "not_found"; message: string }
  | { code: "unauthorized"; message: string }
  | { code: "forbidden"; message: string }
  | { code: "validation"; message: string; details: { field: string; rule: string }[] }
  | { code: "conflict"; message: string }
  | { code: "rate_limit"; message: string; retryAfter: number }
  | { code: "internal"; message: string }
```

## Domain-specific types

Railbase ships first-class field types для типичных enterprise/B2B/fintech-кейсов: `tel`, `finance`, `currency`. SDK генерирует typed helpers + format/parse utilities. См. также [03-data-layer.md](03-data-layer.md#domain-specific-field-types).

### Tel

```ts
// types.ts (generated)
type User = {
  phone: string                                // E.164 string на wire ("+15551234567")
  // ...
}

// helpers (shipped в SDK)
import { tel } from "@railbase/sdk/tel"

tel.formatNational("+15551234567")             // "(555) 123-4567"
tel.formatInternational("+15551234567")         // "+1 555-123-4567"
tel.formatRFC3966("+15551234567")              // "tel:+1-555-123-4567"
tel.parse("(555) 123-4567", { region: "US" }) // "+15551234567"
tel.isValid("+15551234567")                    // true
tel.region("+15551234567")                     // "US"
tel.type("+15551234567")                       // "MOBILE" | "FIXED_LINE" | ...
```

SDK использует `libphonenumber-js` (web port) для матчинга серверной validation.

### Finance

Decimal precision (НЕ float) — для accounting/payroll/billing где `0.1 + 0.2 ≠ 0.3` неприемлемо.

```ts
// types.ts (generated)
type Invoice = {
  amount: string                               // decimal string ("1234.56") на wire
  // ...
}

// SDK ships decimal.js peer dependency
import Decimal from "decimal.js"

const total = new Decimal(invoice.amount).plus(other.amount)
// → exact decimal arithmetic, no float loss

// Helpers (shipped в SDK)
import { finance } from "@railbase/sdk/finance"

finance.parse("1234.56")                       // Decimal instance
finance.format(new Decimal("1234.56"))         // "1,234.56" (locale-aware)
finance.compare(a, b)                          // -1 | 0 | 1
finance.sum([...amounts])                      // Decimal
finance.average([...amounts])                  // Decimal
finance.tax(amount, rate)                      // amount × rate с правильной precision
```

### Currency

Composite type — amount (decimal) + ISO 4217 code.

```ts
// types.ts (generated)
type CurrencyValue = {
  amount: string                               // decimal string
  currency: string                             // ISO 4217 code ("USD" | "EUR" | ...)
}

type Subscription = {
  price: CurrencyValue
  // ...
}

// Helpers
import { currency } from "@railbase/sdk/currency"

currency.format({ amount: "1234.56", currency: "USD" })
// → "$1,234.56" (auto locale: en-US default; configurable)

currency.format({ amount: "1234.56", currency: "RUB" }, { locale: "ru-RU" })
// → "1 234,56 ₽"

currency.format({ amount: "1234", currency: "JPY" })
// → "￥1,234" (JPY = 0 decimals)

currency.parse("$1,234.56", { currency: "USD" })
// → { amount: "1234.56", currency: "USD" }

currency.symbol("USD")                         // "$"
currency.precision("USD")                      // 2
currency.precision("JPY")                      // 0

// Same-currency arithmetic
const total = currency.add(price1, price2)     // throws если currencies don't match

// Cross-currency conversion (требует FX rates — provided manually или через railbase-fx plugin)
const rates = { USD: 1, EUR: 0.92, RUB: 91.5 }
const converted = currency.convert(price, "EUR", rates)

// React/Vue formatters helper (для component libs)
<CurrencyDisplay value={subscription.price} locale="en-US" />
```

### Validation в формах

zod schemas auto-generated для всех типов:

```ts
import { schemas } from "./client"

// Generated zod schema
schemas.User
// → z.object({
//     phone: z.string().refine(tel.isValid, "invalid phone"),
//     ...
//   })

schemas.Invoice
// → z.object({
//     amount: z.string().refine((s) => finance.isValid(s, { precision: 2 })),
//     ...
//   })

schemas.Subscription
// → z.object({
//     price: z.object({
//       amount: z.string().refine(...),
//       currency: z.enum(["USD", "EUR", "GBP"]),  // если AllowedCurrencies
//     }),
//   })
```

Для React Hook Form / TanStack Form / etc. — works out of the box.

### Bundle size

- `libphonenumber-js` core: ~30 KB gzip (SDK-shipped только если коллекция использует tel field)
- `decimal.js` minimal: ~13 KB gzip (только если используется finance/currency)
- `currency` helpers + ISO 4217 catalog: ~5 KB gzip
- ISO 3166-1 / 639-1 / IANA TZ catalogs: ~10 KB gzip combined
- IBAN / BIC validation: ~3 KB gzip
- Tax ID multi-country catalog: ~8 KB gzip
- `qrcode.js` + `jsQR` (для client-side render и scan): ~25 KB gzip combined

Tree-shaking: если в schema нет соответствующих field types — helpers не bundled.

### ERP-specific helpers

```ts
// Address
import { address } from "@railbase/sdk/address"
address.format(record.billing_address, { locale: "en-US" })
// → "123 Main St, Apt 4B\nSan Francisco, CA 94102\nUSA"
address.validatePostalCode("94102", "US")           // true
address.validatePostalCode("ABC-123", "US")          // false

// IBAN
import { iban } from "@railbase/sdk/iban"
iban.validate("DE89370400440532013000")              // true (mod-97)
iban.format("DE89370400440532013000")                // "DE89 3704 0044 0532 0130 00"
iban.country("DE89370400440532013000")               // "DE"

// Tax ID
import { taxId } from "@railbase/sdk/taxid"
taxId.validate({ country: "RU", type: "INN", value: "7707083893" })  // true (checksum)
taxId.types("RU")                                     // ["INN", "KPP", "OGRN", "OGRNIP"]

// Country / Language / Timezone
import { country, language, timezone } from "@railbase/sdk/locale"
country.name("US")                                    // "United States"
country.nativeName("DE")                              // "Deutschland"
country.flag("RU")                                    // "🇷🇺"
country.dialCode("US")                                // "+1"
country.currency("JP")                                // "JPY"
language.name("ru")                                   // "Russian"
timezone.list({ country: "US" })                      // ["America/New_York", ...]

// Person name
import { personName } from "@railbase/sdk/person-name"
personName.format(record.contact, { style: "western-formal" })
// → "Dr. Ivan Petrov, PhD"
personName.format(record.contact, { style: "russian-formal" })
// → "Петров Иван Сергеевич"
personName.initials(record.contact)                   // "IP"

// Quantity
import { quantity } from "@railbase/sdk/quantity"
quantity.format({ amount: "10.5", unit: "kg" }, { locale: "en-US" })
// → "10.5 kg"
quantity.convert({ amount: "10", unit: "lb" }, "kg")  // { amount: "4.535", unit: "kg" }
quantity.add({ amount: "5", unit: "kg" }, { amount: "500", unit: "g" })
// → { amount: "5.5", unit: "kg" }

// Duration
import { duration } from "@railbase/sdk/duration"
duration.parse("P1Y6M")                               // ISO 8601 → object
duration.format("P1Y6M", { locale: "en", style: "long" })
// → "1 year 6 months"
duration.addToDate(new Date(), "P3M")                 // Date 3 months from now

// DateRange
import { dateRange } from "@railbase/sdk/date-range"
dateRange.overlap(a, b)                               // boolean
dateRange.duration(range)                             // duration object
dateRange.format(range, { locale: "en-US" })          // "Jan 1 — Jan 15, 2026"

// Tree path
import { tree } from "@railbase/sdk/tree"
tree.depth("/root/eng/backend")                       // 3
tree.parent("/root/eng/backend")                      // "/root/eng"
tree.ancestors("/root/eng/backend")                   // ["/root", "/root/eng"]

// Tags
import { tags } from "@railbase/sdk/tags"
const matches = await tags.suggest("react", "posts")   // autocomplete suggestions

// Slug
import { slug } from "@railbase/sdk/slug"
slug.fromString("Hello World!", { locale: "en" })     // "hello-world"
slug.fromString("Привет мир", { transliterate: true }) // "privet-mir"

// Barcode
import { barcode } from "@railbase/sdk/barcode"
barcode.validate("0012345678905", "EAN13")            // true (checksum)
barcode.format("0012345678905", "EAN13")              // formatted
barcode.detect("4006381333931")                        // "EAN13"

// Status (state machine)
import { status } from "@railbase/sdk/status"
status.allowedNext(invoice, "submitted")               // ["approved", "rejected"]
status.canTransition(invoice, "submitted", "paid")     // false

// QR code
import { qr } from "@railbase/sdk/qr"
qr.url(record, "payment_qr", { format: "svg", size: 400 })
// → "/api/files/.../payment_qr.svg?size=400&token=..."
qr.encode("PO-2026-0042", { ecc: "M", margin: 4 })
// → renders client-side using qrcode.js
qr.scan(videoElement, (value) => {...})                // client-side scanner via getUserMedia + jsQR

// Hierarchical helpers — adjacency list
import { tree } from "@railbase/sdk/tree"
const children = await rb.collections.comments.tree.children(parentId)
const descendants = await rb.collections.comments.tree.descendants(parentId, { maxDepth: 5 })
const ancestors = await rb.collections.comments.tree.ancestors(commentId)

// Hierarchical helpers — materialized path
const subtree = await rb.collections.departments.tree.descendants("/root/eng")
await rb.collections.departments.tree.move("/root/eng/team-a", "/root/research")

// Hierarchical helpers — DAG
const ordered = await rb.collections.bom.dag.topologicalSort()
await rb.collections.bom.dag.addEdge(parentPart, childPart)
const hasCycle = await rb.collections.bom.dag.hasCycle()
```

---

## Configuration

```ts
const rb = createRailbaseClient({
  baseURL: "https://api.example.com",
  language: "ru",                              // for i18n mailer + locale
  authStorage: "localStorage",                  // localStorage | sessionStorage | cookie | memory
  realtimeAutoConnect: true,
  realtimeResume: true,
  fetch: customFetch,                           // override fetch for testing
  onError: (err) => {...},                      // global error handler
  onAuthChange: (session) => {...},
})
```

## Generation workflow

```bash
# Dev: auto-regen on schema change
railbase serve --dev          # generates SDK on schema change automatically

# Manual
railbase generate sdk --out ./client --lang ts

# In CI
railbase generate sdk --out ./client --check  # exits 1 если drift
```

## Hot reload в dev

При `railbase serve --dev` после schema change:
1. Migrations apply
2. SDK regenerated в `client/`
3. Dev server (Vite/Bun) picks up через file watch
4. HMR refreshes browser

## React / framework integration examples

В docs (не shipped как packages):

### TanStack Query

```ts
import { useQuery, useMutation } from "@tanstack/react-query"
import { rb } from "./client"

export function usePosts(filter: PostFilter) {
  return useQuery({
    queryKey: ["posts", filter],
    queryFn: () => rb.collections.posts.list({ filter }),
  })
}

export function useCreatePost() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: rb.collections.posts.create,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["posts"] }),
  })
}

export function usePostsSubscribe(filter: PostFilter) {
  // Custom hook integrating realtime + TanStack cache
  ...
}
```

### SWR

```ts
import useSWR from "swr"
import { rb } from "./client"

export const usePosts = (filter: PostFilter) =>
  useSWR(["posts", filter], () => rb.collections.posts.list({ filter }))
```

### Solid Resource, Svelte stores, etc.

См. cookbook в docs.

## Что НЕ делает SDK

- Не пытается быть state-management library — оставляем frameworks их делать
- Не кеширует автоматически — это ответственность TanStack Query / SWR
- Не делает optimistic updates автоматически — same
- Не shipиm React hooks как пакет — framework drift

## Open questions

- **Auto-generated React hooks** в v1.2+ optional? Низкий приоритет — frameworks эволюционируют, churn высокий.
- **Codegen via `railbase generate sdk` vs runtime introspection**: codegen better DX, но требует rebuild. Default codegen, fallback runtime для quick prototyping?
- **Python SDK timing**: AI/ML use cases растут — может v1.2 vs v2?

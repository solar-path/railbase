# 06 — Hooks: JSVM bindings, isolation, Go hooks, internal eventbus

## Two hook surfaces

### 1. Go hooks (compile-time)

Регистрируются в Go DSL коллекций или programmatically. Максимум performance и type safety.

```go
schema.Collection("posts").
    Hook(schema.BeforeCreate, func(ctx, r *Record) error {
        if r.GetString("title") == "" {
            return errors.New("title required")
        }
        r.Set("slug", slugify(r.GetString("title")))
        return nil
    }).
    Hook(schema.AfterCreate, notifySubscribers)
```

### 2. JS hooks (runtime, через goja)

Файлы в `pb_hooks/*.pb.js`. PB-compatible API. Hot-reload через fsnotify.

---

## JS hooks API (PB-compatible + extensions)

### Record hooks

Hooks are methods on the `$app` global. Each `on*` method returns a
builder; call `.bindFunc(handler)` to attach. The handler MUST call
`e.next()` to proceed — throwing aborts the chain (400 from a Before
hook; logged from an After hook, since the row is already committed).

```js
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
  // Before — inside the transaction.
  // `e.record` is a plain JS object — read/write fields directly.
  const title = (e.record.title || "").trim()
  if (!title) throw new Error("title required")
  e.record.title = title
  e.next()
})

$app.onRecordAfterCreate("posts").bindFunc((e) => {
  // After commit — side effects safe
  console.log("post created:", e.record.id)
  e.next()
})
```

Available events: `onRecordBeforeCreate`, `onRecordAfterCreate`,
`onRecordBeforeUpdate`, `onRecordAfterUpdate`, `onRecordBeforeDelete`,
`onRecordAfterDelete`. Each scope is per-collection (the first
argument). Watchdog: each invocation is capped at 5s wall time.

### Custom HTTP routes

```js
$app.routerAdd("GET", "/hello/:name", (c) => {
  return c.json(200, { hi: c.pathParam("name") })
})
```

### Cron jobs

```js
$app.cronAdd("digest", "0 9 * * *", () => {
  // runs at 09:00 daily, server time
  console.log("daily digest")
})
```

### Per-request hook

```js
$app.onRequest((e) => {
  // Fires synchronously before every request. Use for telemetry,
  // request-shape rewrites, etc. Call e.next() to proceed.
  e.next()
})
```

### Authority + Document hooks

> **Status: deferred.** `onAuthority*` / `onDocument*` are not in the
> shipping JS surface. For now, register equivalent Go hooks via
> `app.GoHooks().OnRecordAfterCreate(...)` — see `pkg/railbase/hooks/`
> for the typed API.

---

## JSVM bindings — что доступно из JS

> **Status (shipping, as of v1.2.x):** Only the `$app.*` surface
> documented above (record hooks, `routerAdd`, `cronAdd`, `onRequest`,
> `realtime()`) is wired through the goja runtime. The remaining
> bindings below (`$apis`, `$http`, `$os`, `$security`, `$template`,
> `$tokens`, `$filesystem`, `$mailer`, `$dbx`, `$inflector`, `$export`,
> `$documents`, `$authority`, `$jobs`) are roadmap entries — calling
> them from JS today will throw "ReferenceError: $apis is not defined"
> (or similar). For now, reach those services from Go via the
> equivalent `app.Mailer()` / `app.Stripe()` / `app.Jobs()` / etc.
> accessors and register the route with `$app.routerAdd` if you need
> a JS entry point. Track the full surface in [16-roadmap.md](16-roadmap.md).

### `$app` — main application object

```js
$app.dao()                          // database access
$app.dao().findRecordById(coll, id)
$app.dao().findRecordsByFilter(coll, filter, sort, limit, offset)
$app.dao().saveRecord(record)
$app.dao().deleteRecord(record)
$app.dao().runInTransaction((txDao) => {...})

$app.realtime().publish(topic, payload)
$app.realtime().subscribers(topicPattern)

$app.settings()                     // runtime-mutable settings
$app.settings().smtp.host

$app.logger()                       // structured logger
$app.logger().info("msg", { key: "value" })
```

### `$apis` — HTTP helpers

```js
$apis.requireAuth(...collections)             // middleware: must be authenticated в одной из collections
$apis.requireRole("admin")                     // middleware: role check
$apis.bodyLimit(bytes)                         // middleware: body size
$apis.gzip()                                   // middleware: response gzip
$apis.recordAuthResponse(c, record, opts)      // helper для auth endpoints
```

### `$http` — outbound HTTP

```js
const res = $http.send({
  url: "https://api.example.com/data",
  method: "POST",
  body: JSON.stringify({...}),
  headers: { "Content-Type": "application/json", "Authorization": "Bearer ..." },
  timeout: 10,
})
res.statusCode
res.body                            // string
res.json                            // parsed JSON
```

### `$os` — OS helpers (gated)

```js
$os.cmd(...args)                    // execute command (DISABLED by default; flag --hooks-os-cmd to enable)
$os.environ(name)                    // env var read
$os.readFile(path)                   // только в pb_data/ (sandbox)
$os.writeFile(path, content)
$os.exists(path)
```

`$os.cmd` jasно opt-in через `RAILBASE_HOOKS_OS_CMD=true` env var (security).

### `$security` — crypto helpers

```js
$security.randomString(length)
$security.randomBytes(length)
$security.hashCode(str)
$security.hmac(text, key, algo)        // sha256, sha512
$security.encrypt(text, key)            // AES-256
$security.decrypt(cipher, key)
$security.parseJWT(token, secret)
$security.createJWT(claims, secret, expiry)
```

### `$template` — template rendering

```js
const html = $template.loadFiles("emails/welcome.html").render({
  user: { name: "Alice" },
  link: "https://...",
})
```

Использует Go `html/template` (auto-escape). Для markdown-templates — `$mailer.render()`.

### `$tokens` — record tokens (verify, reset, file-access, magic-link)

```js
const token = $tokens.recordVerificationToken(record)
const token = $tokens.recordPasswordResetToken(record)
const token = $tokens.recordEmailChangeToken(record, newEmail)
const token = $tokens.recordFileToken(record, filename)
const token = $tokens.recordAuthToken(record)        // session token

$tokens.verifyToken(token, purpose)                    // returns claims или throws
```

### `$filesystem` — file operations

```js
const fs = $filesystem.system()                        // configured driver (FS or S3)
fs.upload("path/to/key", contentBytes)
fs.download("path/to/key")                             // returns ReadCloser
fs.delete("path/to/key")
fs.exists("path/to/key")
fs.list("prefix/")                                     // iterator
fs.serveAndCacheFile(c, key, opts)                     // HTTP serve helper
```

### `$mailer` — email sending

```js
$mailer.send({
  to: "user@example.com",
  cc: ["..."],
  bcc: ["..."],
  subject: "Welcome",
  html: "<p>...</p>",
  text: "...",
  attachments: [{ filename: "doc.pdf", content: bytes }],
})

$mailer.send({
  template: "welcome.md",                              // markdown template
  to: "user@example.com",
  data: { user, link },
})

$mailer.render("welcome.md", data)                     // returns rendered HTML без send
```

### `$dbx` — direct DB access

```js
const records = $dbx.newQuery("SELECT * FROM posts WHERE status={:s}").
  bind({ s: "published" }).
  all()
```

Goes through query builder с param binding. **Не обходит RBAC** — admin-only context.

### `$inflector` — string helpers

```js
$inflector.snakeCase("HelloWorld")        // hello_world
$inflector.camelCase("hello_world")        // helloWorld
$inflector.pluralize("post")               // posts
$inflector.singularize("posts")            // post
$inflector.titleize("hello world")          // Hello World
```

### `$export` — XLSX/PDF generation (Railbase extension)

```js
const xlsx = $export.xlsx({ sheet, columns, rows })
const pdf = $export.pdf({ template, data })
```

См. [08-generation.md](08-generation.md).

### `$documents` — document repository (Railbase extension)

```js
const doc = $documents.upload({
  ownerType: "vendor", ownerId,
  title: "Master Service Agreement",
  file: bytes, fileName: "msa.pdf",
})
$documents.archive(docId, { reason })
$documents.list({ ownerType, ownerId })
```

См. [07-files-documents.md](07-files-documents.md).

### `$authority` — approval engine (с plugin)

```js
const decision = $authority.checkOrSubmit({
  resource: "payments", action: "create", payload, requester,
})
```

См. plugin `railbase-authority` в [15-plugins.md](15-plugins.md).

### `$jobs` — background queue

```js
$jobs.enqueue("send_welcome", { userId }, { priority: 5, runAt: "+5m" })
$jobs.cron("daily_digest", "0 9 * * *", "send_digest", null)
```

См. [10-jobs.md](10-jobs.md).

### Error classes

```js
throw new BadRequestError(message, data)        // 400
throw new UnauthorizedError(message, data)      // 401
throw new ForbiddenError(message, data)         // 403
throw new NotFoundError(message, data)          // 404
throw new RailbaseError({ code, message, details })   // native, custom code
```

PB-compat error names + native RailbaseError class.

---

## Hook isolation model

Goja ≠ V8. Без жёсткой sandbox-модели hooks протекут через 6 месяцев.

### Configuration

```go
type HookConfig struct {
    ExecutionTimeout    time.Duration  // default 5s
    MemoryCeiling       int            // soft, через interrupt watchdog
    RuntimeRecycleEvery int            // recycle после N invocations
    MaxStackDepth       int            // SetMaxCallStackSize
    AllowedAPIs         []string       // whitelist бридж-функций
}
```

### Обязательные механизмы

1. **Hard timeout** — watchdog goroutine вызывает `Runtime.Interrupt()` через timeout
2. **Memory watchdog** — host-hook на allocations, kill при превышении
3. **Runtime pool с recycling** — `*Runtime` recycled после N вызовов или OOM signal; новый чистый runtime создаётся в фоне
4. **No shared globals between invocations** — каждый вызов получает свежий `globalThis` snapshot
5. **Panic isolation** — `defer recover()` в bridge layer, panic в JS не валит process
6. **Deterministic teardown** — если runtime в зомби-состоянии после Interrupt, kill пул-слот и пересоздай
7. **Resource quotas per-tenant** (когда `.Tenant()` активен) — hook от tenant A не съест runtime бюджет tenant B

### Метрики (Prometheus)

- `railbase_hook_duration_seconds{collection, event, outcome}`
- `railbase_hook_timeout_total`
- `railbase_hook_oom_total`
- `railbase_hook_panic_total`
- `railbase_runtime_recycle_total{reason}`

### Pool architecture

```
GOMAXPROCS workers
  ↓
sync.Pool of *goja.Runtime
  ↓ Acquire
Per-invocation: bridges injected, runtime executes script
  ↓ Release (или Discard если recycle threshold)
Runtime может быть discarded → background создаёт новый чистый
```

---

## Hot reload

`fsnotify` watches `pb_hooks/`. На change:

1. Rebuild runtime pool (parse all `.pb.js` files into new pool)
2. Drain inflight invocations (timeout 2s)
3. Atomic swap через `atomic.Pointer[runtimePool]`
4. Toast в admin UI: «hook reloaded in 230ms»

### File structure

```
pb_hooks/
  posts.pb.js           # group по collection
  auth.pb.js
  webhooks/             # subdirectories OK
    stripe.pb.js
    sendgrid.pb.js
  _shared.js            # underscore prefix → не auto-loaded; require()-able
```

### Module system (limited)

Goja без full ES modules. Simulate через `require("./helpers")` resolved against `pb_hooks/`. Cached compiled programs.

Нет npm. Vendor-friendly: `pb_hooks/vendor/lodash.js` копируется руками.

---

## Internal event bus (отдельно от hooks dispatcher)

Не путать с hooks: hooks — user-defined callbacks. Eventbus — internal mechanism для cross-module communication.

Hooks dispatcher сам подписан на eventbus events типа `record.created` и triggers JS hooks. Это внутренняя реализация.

Подробности в [02-architecture.md](02-architecture.md#inter-module-communication--три-механизма).

---

## Что НЕ позволяем

- Spawn arbitrary goroutines из JS — single-thread per runtime
- Direct file system access вне `pb_data/` — sandbox
- Network requests без timeout — `$http.send` requires timeout
- Modify schema из hooks — schema-as-code source-of-truth
- Bypass RBAC из `$app.dao()` — admin context applies (если admin) или actor context
- `eval` strings из user input — security

---

## PB hook compatibility

Полный список PB-compatible hook names supported:

### Record hooks
- `onRecordCreate`, `onRecordAfterCreate`, `onRecordCreateRequest`
- `onRecordUpdate`, `onRecordAfterUpdate`, `onRecordUpdateRequest`
- `onRecordDelete`, `onRecordAfterDelete`, `onRecordDeleteRequest`
- `onRecordValidate`
- `onRecordsListRequest`, `onRecordViewRequest`

### Auth hooks
- `onRecordAuthRequest`
- `onRecordBeforeAuthWithPassword`, `onRecordAfterAuthWithPassword`
- `onRecordBeforeAuthWithOAuth2`, `onRecordAfterAuthWithOAuth2`
- `onRecordBeforeAuthRefresh`, `onRecordAfterAuthRefresh`
- `onRecordBeforeRequestVerification`, `onRecordAfterRequestVerification`
- `onRecordBeforeConfirmVerification`, `onRecordAfterConfirmVerification`
- `onRecordBeforeRequestPasswordReset`, `onRecordAfterRequestPasswordReset`
- `onRecordBeforeConfirmPasswordReset`, `onRecordAfterConfirmPasswordReset`
- `onRecordBeforeRequestEmailChange`, `onRecordAfterRequestEmailChange`
- `onRecordBeforeConfirmEmailChange`, `onRecordAfterConfirmEmailChange`

### Collection hooks
- `onCollectionCreate/AfterCreate/Update/AfterUpdate/Delete/AfterDelete`

### Mailer hooks
- `onMailerBeforeRecordVerificationSend`
- `onMailerBeforeRecordPasswordResetSend`
- `onMailerBeforeRecordEmailChangeSend`
- `onMailerBeforeRecordOTPSend`

### File hooks
- `onFileBeforeUpload`, `onFileAfterUpload`
- `onFileBeforeDelete`, `onFileAfterDelete`
- `onFileDownloadRequest`

### Realtime hooks
- `onRealtimeConnectRequest`, `onRealtimeDisconnect`
- `onRealtimeBeforeSubscribe`, `onRealtimeAfterSubscribe`

### Server hooks
- `onBootstrap`, `onTerminate`
- `onServe`

### Settings hooks
- `onSettingsReload`, `onSettingsBeforeUpdateRequest`, `onSettingsAfterUpdateRequest`

Plus Railbase additions: `onAuthority*`, `onDocument*`, custom topic `onTopic("name", ...)`.

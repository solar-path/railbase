# 17 — Verification

End-to-end tests для каждого major feature. Должны проходить перед v1 release.

## Smoke tests

1. **Build & cross-compile**: `goreleaser build --snapshot --clean` — артефакты для linux/darwin/windows × amd64/arm64, размер каждого ≤ 32 MB (ceiling был 30 MB до v1.7.47; v1.7.48 поднял на 2 MB под 9-локальные lazy-чанки admin SPA)
2. **5-minute smoke**: `railbase init demo --template basic && cd demo && railbase serve` — admin UI работает, REST + realtime + SDK работают
3. **PB drop-in compat**: PB JS SDK против Railbase в `RAILBASE_PBCOMPAT=strict` — работает без изменений
4. **PB import**: `railbase import schema --from-pb http://...` — миграции, коллекции, базовые hooks портированы
5. **TS SDK**: сгенерирован, компилируется, типы правильные, drift-detection работает

## Data layer tests

6. **DB driver smoke**: `pgx/v5` подключается к Postgres 14, 15, 16; embedded-postgres mode стартует subprocess + accepts connection
7. **Migration up/down**: каждая migration применяется и откатывается
8. **Migration drift detection**: edit applied migration → block startup без `--allow-drift`
9. **Schema diff**: добавил поле в Go DSL → `railbase migrate diff` создаёт корректный migration файл
10. **Field types completeness**: каждый field type создаётся, валидируется, экспонируется в SDK с правильным TS типом
10a. **Tel field**: input `(555) 123-4567` с `Region("US")` → stored как `+15551234567`; invalid number → 400; mobile-only filter rejects landline
10b. **Tel formatting**: SDK helpers `formatNational/formatInternational` round-trip с E.164 storage
10c. **Finance precision**: создал record с `amount="0.1"`, добавил `"0.2"` через Go API → result `"0.3"` exact (no float drift); `SELECT SUM(amount)` aggregate возвращает exact `NUMERIC` value
10d. **Finance constraints**: `.Positive()` → 0 fails; `.NonNegative()` → -1 fails; precision violation → 400
10e. **Currency wire format**: JSON `{ amount: "1234.56", currency: "USD" }` round-trips; `AllowedCurrencies("USD")` rejects "GBP"
10f. **Currency precision auto**: JPY storage = "1234" (no decimals); USD = "1234.56" (2 decimals); BHD = "1.234" (3); cross-validation per currency
10g. **Currency mixed-currency**: Go API `add(USD, EUR)` panics/errors без FX; с FX provider — converts; SDK same behavior
10h. **Currency locale formatting**: SDK `format({...USD...}, "en-US")` = "$1,234.56"; same value `"ru-RU"` с RUB = "1 234,56 ₽"; JPY locale truncates decimals
10i. **FX plugin** (railbase-fx): `$fx.convert({from:"USD", amount:"100", to:"EUR"})` returns valid rate; historical rate at past date differs from current
10j. **Tel + OTP integration**: user с tel field включает SMS OTP → request OTP → SMS отправляется (через provider plugin) на нормализованный E.164

### ERP-specific field types

10k. **Address validation**: country-aware postal code validation — `94102` valid для US, `ABC-123` rejected; state validation для US states list
10l. **TaxID multi-country**: ИНН-12 с правильной checksum принимается; неправильная checksum → 400; EIN format `XX-XXXXXXX` validated
10m. **IBAN**: `DE89370400440532013000` accepted (mod-97 valid); same string с одной digit changed → rejected; SDK formatter возвращает groups of 4
10n. **BIC**: `DEUTDEFF` (8 chars) и `DEUTDEFFXXX` (11 chars) оба valid; `DEUT-DEFF` rejected
10o. **BankAccount IBAN-mode**: composite сохраняет IBAN+BIC+bank+currency; rendered preview показывает masked IBAN
10p. **PersonName formatting**: `style: "western-formal"` для US contact → `Dr. Jane Doe, PhD`; `style: "russian-formal"` для русского → `Петров Иван Сергеевич`
10q. **Percentage**: input `15` сохраняется как `15.0000`; display `15%`; with `Precision(2)` → `15.00%`
10r. **MoneyRange validation**: min > max → 400; both required currency match
10s. **Country / Language / Timezone enum**: `XX` rejected (not in catalog); `RU` accepted; SDK lookup name возвращает «Russia» / native «Россия»
10t. **Coordinates lightweight**: `{lat: 55.75, lng: 37.62}` validated (range checks); `{lat: 91, ...}` rejected
10u. **Quantity unit conversion**: `5 kg` + `500 g` через Go API/SDK = `5.5 kg`; `10 ft` → `3.048 m`; cross-unit-group sum rejects (`5 kg + 10 m` errors)
10v. **Duration ISO 8601**: `P1Y6M` parsed; `addToDate(now, "P3M")` returns date 3 months later; humanized formatter возвращает «1 year 6 months» / «1 год 6 месяцев»
10w. **DateRange overlap**: two ranges с overlap → helper returns true; sequential без overlap → false
10x. **Status state machine**: legal transition (`draft` → `submitted`) → success + audit row + OnEnter callback fired; illegal (`draft` → `paid`) → 400 «illegal transition»
10y. **TreePath operations**: insert child, descendants() returns subtree; move subtree atomic; depth column auto-updated
10z. **Tags autocomplete**: type «react» → suggestions из existing tags; new tag created если AllowNew
10aa. **Slug auto-generation**: `title="Hello World!"` → `slug="hello-world"`; conflict с existing → `hello-world-2`; transliteration «Привет мир» → `privet-mir`
10ab. **SequentialCode**: pattern `PO-{YYYY}-{NNNN}` дает `PO-2026-0001`, `PO-2026-0002`; year change → counter resets если `ResetYearly`; concurrent allocations atomic (no duplicates)
10ac. **Barcode validation**: EAN-13 `4006381333931` valid (checksum); same с одной digit changed → rejected; format auto-detection из length+checksum
10ad. **Markdown vs RichText**: markdown stored raw, rendered preview matches; RichText sanitized HTML (bluemonday); FTS search работает на rendered text для markdown
10ae. **Color**: hex `#FF5733` accepted; invalid `#GGGGGG` rejected; SDK formatter возвращает RGB equivalent
10af. **Cron**: cron expression validated; UI builder выдаёт natural language («Every Monday at 9 AM»)

### QR code

10ag. **QR encode-from**: field с `EncodeFrom("payment_url")` — value автоматически берётся из source field; change source → QR re-renders
10ah. **QR formats**: PNG/SVG/PDF endpoints возвращают correct MIME, scanner reads value back
10ai. **QR ECC levels**: Low/Medium/High/VeryHigh — все рендерятся; logo overlay требует ≥ Medium (rejected на Low)
10aj. **QR caching**: повторный request за same image → cache hit (sub-50ms); change source value → cache invalidated
10ak. **QR scan endpoint**: POST `/api/qr/scan` с value resolves в record через indexed field
10al. **National payment QR (с plugin)**: СБП payload validates per ЦБ РФ spec; EPC validates per EU spec; invalid → 400 «invalid payment QR format»

### Hierarchical patterns

10am. **Adjacency list children**: 5-level tree → recursive CTE returns all descendants; depth limit honored
10an. **Adjacency list cycle prevention**: попытка set parent на собственного descendant → 400 «cycle detected»
10ao. **Materialized path move**: subtree move обновляет paths всех descendants атомарно; concurrent reads видят consistent state
10ap. **Materialized path depth**: depth column auto-maintained на insert/move
10aq. **Nested set range query**: subtree aggregation `SELECT SUM(amount) WHERE lft BETWEEN ?...?` returns correct total
10ar. **Nested set insert**: insert shifts left/right values atomically; tree остаётся consistent
10as. **Closure table descendants**: O(1) lookup через JOIN closure
10at. **Closure table multi-parent (DAG)**: node с 2 parents → both ancestors lists возвращают node
10au. **DAG cycle prevention**: попытка add edge creating cycle → 400; HasCycle() returns true if existing data corrupted
10av. **DAG topological sort**: returns valid build order (all parents before children)
10aw. **Ordered children**: drag-drop reorder updates sort_index; positions stable across reload
10ax. **Tree integrity job**: nightly check detects orphans/cycles/depth-mismatches; reports в admin UI
11. **View collection**: `active_users` view возвращает данные через GET, no CRUD endpoints, realtime работает
12. **Computed fields**: stored vs non-stored variants работают
13. **Pagination cursor stability**: insert/delete во время пагинации → cursor продолжает работать без duplicates/skips
14. **Pagination offset (PB-compat)**: `?page=2&perPage=20` возвращает ожидаемое
15. **Batch operations**: 50 операций в одной транзакции; rollback на failure; audit per-op + batch row
16. **Filter parser security**: попытка SQL injection через filter (`'; DROP TABLE`) → 400 parse error, ничего не выполнено
17. **Filter native extensions**: `@me`, `IN (...)`, `BETWEEN`, `IS NULL` работают
18. **Multi-tenancy compile-time**: попытка вызвать tenant-query без TenantID → compile error; попытка cross-tenant query без `WithSiteScope` → runtime panic в dev
19. **Multi-tenancy runtime**: создать 2 tenants, запросить из контекста tenant A → записи tenant B не видны

## Auth & identity tests

20. **Multiple auth collections**: `users` и `sellers` collections, signin через каждую отдельно, sessions не пересекаются
21. **OAuth providers**: signin через Google, GitHub, Apple (с client_secret rotation)
22. **OIDC**: generic OIDC provider configured через UI, signin успешен
23. **OAuth2 callback**: state validation, CSRF protection
24. **External auth linking**: existing user добавляет GitHub provider; sign-in через GitHub находит existing user (не дубликат)
25. **OTP / magic link**: request OTP via email → consume code → session
26. **MFA flow**: password → email OTP → TOTP → session (multiple factors)
27. **2FA TOTP**: enable, verify, recovery codes generation, recovery code use invalidates
28. **WebAuthn registration + signin**: passkey registered, used для passwordless signin
29. **Password reset flow**: request → email → confirm с new password → all sessions revoked
30. **Email change flow**: request → confirm на new email → email updated, sessions revoked
31. **Email verification**: signup → email → confirm → `verified=true`
32. **Auth methods endpoint**: returns enabled providers только
33. **Session refresh**: sliding window работает
34. **Devices**: trust 30 days, revoke kicks all sessions
35. **Auth origins**: signin from new country → email alert + audit
36. **Impersonation**: admin impersonates user → audit start/stop + every action
37. **API tokens**: create with scope, use as bearer, revoke; expired token fails
38. **Record tokens**: file access token TTL works; verification token single-use
39. **RBAC site scope**: site role grants apply globally
40. **RBAC tenant scope**: tenant role grants per-tenant; same user different roles в разных tenants
41. **RBAC deny**: попытка action без grant → 403 + audit-row
42. **RBAC matrix UI**: bulk grant works через admin UI

## Realtime tests

43. **Realtime subscribe `*`**: PB JS SDK `pb.collection('posts').subscribe('*', cb)` работает в strict mode
44. **Realtime subscribe filter (native)**: `subscribe('*', cb, { filter: "status='published'" })` доставляет только matching events
45. **Realtime expand**: `subscribe('*', cb, { expand: ['author'] })` — события содержат expanded relations с RBAC-check на каждой
46. **Realtime resume**: клиент disconnect, reconnect через 3 sec → пропущенные events доставляются по resume token
47. **Realtime backpressure**: медленный subscriber > 1MB queue → drop с audit `realtime.backpressure_disconnect`
48. **Realtime cluster (с plugin)**: 2 instances + `railbase-cluster`, instance1 publishes → instance2 delivers
49. **Realtime RBAC**: user без `posts.list` permission не получает posts events
50. **Realtime custom topic**: `$app.realtime().publish("system.alert", ...)` доставляется subscribers
51. **Realtime SSE fallback**: SSE-клиент с тем же scenario как WS
52. **Realtime latency**: события доставляются за < 100ms (single-node)

## Hooks tests

53. **Hooks hot-reload**: edit `pb_hooks/test.pb.js` — sub-1s применяется
54. **Hooks sandbox timeout**: hook с `while(true){}` — убивается за 5s, runtime recycled, метрики `railbase_hook_timeout_total` инкрементятся
55. **Hooks sandbox memory**: hook с large allocation → OOM detected, killed, не валит process
56. **Hooks panic isolation**: hook throws → isolated, request returns 500, server continues
57. **Hooks JSVM bindings**: каждый из `$app/$apis/$http/$os/$security/$template/$tokens/$filesystem/$mailer/$dbx/$inflector` testable
58. **Hooks ordering**: `BeforeCreate` runs внутри tx; `AfterCreate` runs после commit
59. **Hooks PB compat**: все PB hook names работают (60+ hook events)
60. **Custom routes**: `routerAdd("GET", "/foo", ...)` — endpoint доступен; middleware (auth, body limit) работают
61. **Cron**: `cronAdd("digest", "0 9 * * *", ...)` — runs at scheduled time

## File handling tests

62. **File upload**: multipart upload → file stored, hash computed, metadata in record
63. **Image thumbnails**: upload JPEG → thumb `100x100` генерится lazy на первый GET; cache hit на следующий запрос
64. **File MIME validation**: upload disallowed MIME → 400
65. **File size limit**: upload > max → 413
66. **Signed URL**: generate → access works; expired → 403
67. **S3 driver**: same tests но с S3 backend

## Document tests

68. **Document upload — versioning**: upload `msa.pdf` к vendor X с title="MSA" → создан doc v1; повторный upload c тем же title → v2, v1 не теряется, current_version_id указывает на v2
69. **Document immutable repository**: попытка hard-DELETE через REST → 405; `archive` работает; после archive list без `includeArchived` не возвращает; `restore` восстанавливает
70. **Document polymorphic owner**: collection `vendors` с `.AllowsDocuments()` → endpoints `GET /api/collections/vendors/{id}/documents` работают; collection без → 404
71. **Document quota**: tenant с max_bytes=100MB пытается upload 200MB файл → 413 + audit `quota.exceeded`; usage bar обновляется
72. **Document legal hold**: set legal_hold=true → archive blocked с error «under legal hold»; unset → archive работает
73. **Document retention**: set retention_until=прошлая дата + run job → auto-archived; permanent purge через CLI с confirm
74. **Document FTS**: upload doc title="Master Service Agreement Acme" → search?q=acme finds; archived docs не возвращаются без флага
75. **Document access log**: view + download + preview каждое создаёт row в `_document_access_log`
76. **Document text extraction (с flag)**: PDF upload → background job extracts text → search по тексту работает

## Generation tests

77. **Export XLSX**: коллекция с 100k записей, `.Export(ExportXLSX{...})` через REST: streaming, peak memory < 256 MB, файл валиден (открывается в Excel/LibreOffice)
78. **Export PDF (native)**: markdown template с loop, helpers (`date`/`money`), таблицы; рендер за < 2s, файл открывается в любом PDF-viewer
79. **Async export**: request на 1M rows автоматически шунтируется в jobs, `job_id` возвращается, готовый файл качается через signed URL
80. **Export RBAC**: пользователь без `posts.list` permission получает 403 на `posts/export.xlsx`; audit-row пишется
81. **Export quotas**: превышение memory ceiling → 413 + audit; превышение per-tenant rate → 429
82. **Export charts**: XLSX с chart создаётся

## Mailer tests

83. **Mailer SMTP**: send test email через configured SMTP, message arrives
84. **Mailer template hot-reload**: edit `email_templates/signup.md`, новый signup → обновлённый text без рестарта
85. **Mailer i18n**: user с `language=ru` → `signup.ru.md`, fallback на `.en.md`
86. **Mailer rate limit per-recipient**: 6 emails to same address за час → 6th throttled
87. **Mailer console driver (dev)**: emails печатаются в stdout вместо send
88. **Mailer attachment**: send email с PDF attachment → received correctly

## Audit & observability tests

89. **Audit auth events**: signin/signout/password_change all logged
90. **Audit RBAC denies**: deny → audit row + 403
91. **Audit before/after diff**: update record → audit row contains both states
92. **Audit chain integrity (с opt-in sealing)**: applied migration, изменён RBAC, делегировано impersonation — все события в `audit_log` с валидной hash-chain (`./railbase audit verify`)
93. **Audit retention**: events older than retention auto-archived
94. **Logs as records**: log entries appear in `_logs` table; admin UI viewer works
95. **Logs filtering**: filter by level/request_id works
96. **Telemetry**: `/metrics` exposes Prometheus metrics; OTel traces emitted
97. **Healthz / readyz**: 200 при healthy; 503 при DB unavailable

## Lifecycle tests

98. **First-run wizard**: fresh install → setup wizard → admin создан с 2FA → tour completed → can access dashboard
99. **Graceful shutdown**: SIGINT → inflight requests завершаются < 30s; sessions не теряются
100. **Backup → restore round-trip**: backup → restore on fresh instance → all data + settings + hooks восстановлены
101. **Backup auto-upload to S3**: scheduled backup → uploaded to S3
102. **Settings hot reload**: change SMTP setting через admin UI → следующий email через новый SMTP без рестарта
103. **Plugin install**: `railbase plugin install railbase-orgs` — bundle подтягивается, `/api/orgs/...` endpoints появляются после рестарта
104. **Plugin crash isolation**: plugin crashes → core продолжает работать; plugin auto-restart с backoff

## Auth providers detailed tests

105. **Apple sign-in client_secret rotation**: `railbase auth apple-secret` generates valid JWT; periodic rotation works
106. **All 35+ OAuth providers**: each provider successfully signs user in (smoke test minimal)

## Plugin tests

107. **Stripe webhook (с plugin)**: Stripe test webhook → signature verified → subscription record updated → `subscription.created` event published
108. **Authority policy + request (с plugin)**: payment с amount=100k matches policy с chain [controller, cfo] → request submitted, status=pending; record создаётся со status=pending_approval
109. **Authority self-approve block**: initiator имеет роль из chain → попытка approve собственного request → 403 «cannot self-approve» (R22a)
110. **Authority delegation**: user A делегирует user B на 7 дней; user B approves request на роли A → decision записан с on_behalf_of=A
111. **Authority overlapping policies**: два policies match same payload → submit returns 500 «overlapping policies»
112. **Authority chain completion**: последний step approved → onAuthorityApproved hook fires → record status=approved, side effect (e.g. payment processing) запускается
113. **Authority cancel**: requester cancels before first decision → status=cancelled
114. **SAML SSO (plugin)**: redirect to IdP, signed assertion received, JIT user provisioned
115. **SCIM provisioning (plugin)**: SCIM client creates user → user appears в Railbase

## Admin UI tests

116. **Admin UI command palette**: `⌘K` открывает; «posts» приводит на коллекцию posts; «backup now» инициирует backup
117. **Admin UI realtime collaboration**: два admin'а в record editor одной записи: каждый видит presence avatar другого; на save при collision — merge prompt
118. **Admin UI bulk operations**: select 100 records → bulk delete → undo toast 5 sec → undo восстанавливает; full delete после timeout пишет audit
119. **Admin UI inline edit**: `⌘E` на ячейке → editable → optimistic update; backend reject → rollback с error toast
120. **Admin UI dogfooding**: admin UI использует ту же SDK что generated для пользователей; SDK regen → admin UI пересобран без runtime errors
121. **Admin UI per-screen RBAC**: `system_readonly` admin может видеть Settings, но кнопки «Save» disabled; попытка POST через DevTools → 403 + audit
122. **Admin UI mobile**: на tablet 768+ полная функциональность; на phone — login + dashboard + audit read работают, edit blocked с UX hint
123. **Admin UI hooks editor**: создать новый hook через UI, save → hot reload toast в < 1s

## Templates tests

124. **All 4 templates** (`basic/saas/mobile/ai`) запускаются и предоставляют ожидаемую функциональность

## LLM-tooling tests

125. **`railbase generate schema-json`**: даёт valid JSON; agent (Claude/GPT) может прочитать и предложить новую коллекцию
126. **`railbase-mcp` plugin** (v1.2): MCP client (Claude Code) connects, lists collections, queries data, creates record (с RBAC check)

## Notifications, webhooks, i18n, caching, testing, security, streaming

132. **Notifications channels**: event triggers in-app row + email send + push (если plugin); user preferences disable email → только in-app + push
133. **Notifications real-time delivery**: WS subscriber на `users.{id}.notifications` получает event сразу при создании; admin UI shows unread badge
134. **Notifications quiet hours**: priority=low в quiet hours → buffered до конца window; priority=urgent passes immediately
135. **Outbound webhook**: webhook configured для `record.created.posts` → POST к URL с `X-Railbase-Signature`; receiver verifies HMAC; retry 5 раз с exponential backoff на 5xx; final fail → dead-letter
136. **Outbound webhook replay protection**: timestamp > 5 min old → receiver SHOULD reject (helper provides verification example)
137. **Outbound webhook anti-SSRF**: попытка configure webhook URL на `127.0.0.1` или `10.0.0.1` в prod → rejected; в dev — allowed
138. **i18n locale resolution**: user.language=ru → admin UI на русском, server errors на русском; fallback на en для missing keys
139. **i18n translatable field**: `.Translatable()` на title → запись с titles в ru/en/de хранятся в `_translations`; SDK отдаёт по locale
140. **i18n RTL**: locale=ar → admin UI applies `dir="rtl"`; Tailwind variants работают
141. **i18n pluralization**: `comments.count` с count=0/1/5 → корректные plural forms per locale
142. **Cache hit ratio**: повторный list тех же posts → cache hit; UPDATE на posts → cache invalidated; rbac role grant change → permission cache flushed
143. **Cache stampede**: 100 concurrent requests на same key → только 1 backend call (singleflight)
144. **Data import CSV**: `railbase import collection users --from data.csv --dry-run` показывает 100 valid + 5 errors с деталями; commit без --dry-run применяет валидные с conflict policy
145. **Data import bulk**: > 10k rows → routed через jobs queue с progress tracking
146. **Testing helpers**: `railbase.NewTestApp(t)` создаёт isolated env; fixture loads users; test creates post через `app.AsUser(...).Post(...)`; tearing down clears
147. **Testing fixtures**: YAML fixtures загружаются с relations; `app.Seed()` генерирует валидные records через schema-aware faker
148. **Testing JS hooks**: `mockApp` + `fireHook` позволяют unit-test hook без HTTP layer
149. **CSRF protection**: POST без CSRF token из browser cookie session → 403; с token → 200; bearer auth не требует CSRF
150. **Security headers**: response carries CSP, HSTS, X-Frame-Options strict для admin UI; configurable для public API
151. **IP allowlist**: request с denied IP → 403 + audit; allowed IP → through
152. **Account lockout**: 10 failed signins → user locked 30 min; signin attempt during lock → 429 + audit + email user; admin может unlock через UI
153. **Trusted proxy**: `X-Forwarded-For` parsed только если from trusted CIDR; spoof attempt из untrusted source → ignored, logged
154. **Encryption field-level**: `.Encrypted()` на ssn field; raw INSERT через bare-pool возвращает encrypted bytes; SDK get returns decrypted; dump database без key показывает ciphertext
155. **Encryption key rotation**: `railbase encryption rotate-key` → metadata DEKs re-encrypted; existing reads work; audit row
156. **Encryption KMS integration**: `kms:vault:transit/keys/...` URL works; key fetched on demand с caching
157. **Streaming response**: POST `/ai/chat` с long-running LLM call → tokens stream к client через SSE; client disconnect → server cancels upstream call
158. **Streaming backpressure**: slow client не блокирует server; flush per-token; buffer drain через context cancellation
159. **Self-update**: `railbase update` downloads new version, swaps, restarts; `railbase rollback` возвращает к previous
160. **Self-update cluster**: rolling update one node at a time через `railbase-cluster` plugin; health-check между nodes; автоматический pause при failure
161. **Self-update breaking change**: pre-update check detects breaking → abort с инструкцией manual upgrade
162. **Soft delete**: DELETE post → deleted_at set; default GET не возвращает; `?include_deleted=true` (admin) возвращает; restore button восстанавливает в 30-day window; после grace → permanent purge job
163. **Soft delete cascade**: parent с soft-delete + cascade-delete relation → child также soft-deleted; restore parent → child restored
164. **Bulk operations**: POST /records/bulk с 100 ops; одна fails → весь rollback (atomic); success → 207 с per-op result; > 1000 ops → 413
165. **Bulk operations non-atomic**: с `?atomic=false` → partial success возвращает 207 с per-op statuses
166. **Workflow run** (с plugin): saga «checkout» — reserve успех, charge fail → compensation реверсит reserve; admin UI shows run timeline с failure point
167. **Workflow long-running wait** (с plugin): `flow.Wait("manual_approval")` → run pauses; `flow.signal(runId, "manual_approval")` → resume; timeout без signal → compensation
168. **Workflow branching** (с plugin): condition predicate routes к correct branch; both compensations registered

## Performance / load tests

169. **Realtime fan-out (LISTEN/NOTIFY backend)**: 10k concurrent subscribers, 100 events/sec — no degradation; backpressure kicks in correctly при slow subscribers
170. **Realtime fan-out (NATS plugin)**: 3 instances, 30k subscribers распределены; cross-instance event delivery latency p99 < 50ms
171. **DB throughput**: 10k writes/sec sustained на single Postgres; concurrent reads не блокируются
172. **RLS overhead**: tenant-scoped query на 10M rows — RLS adds < 5% latency vs raw query (через `EXPLAIN ANALYZE`)
173. **Job queue throughput**: `SKIP LOCKED` claim под 8 workers — 5000 jobs/sec без contention; no double-execution under load
174. **Hook concurrency**: 100 concurrent hook invocations — pool handles, no deadlocks
175. **Document upload concurrency**: 50 concurrent uploads — no corruption, audit consistent

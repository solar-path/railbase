# 08 — Document generation: XLSX, PDF, markdown templates

Из коробки: экспорт записей коллекций в XLSX и PDF, генерация отчётов из шаблонов, доступ из Go API / JS hooks / REST / CLI. Закрывает типичный enterprise/B2B-кейс «дай мне excel» / «сгенерируй PDF-инвойс» без подтягивания внешних сервисов.

## Что в core, что в plugin

| Capability | Где | Library |
|---|---|---|
| XLSX export & generate | core | `xuri/excelize/v2` (pure Go, ~3 MB) |
| Native PDF (programmatic, без HTML) | core | `signintech/gopdf` (pure Go, ~1 MB) |
| Markdown → PDF | core | `gomarkdown/markdown` → native PDF rendering |
| Charts в XLSX | core | excelize встроенно |
| **HTML → PDF (через headless Chrome)** | **plugin `railbase-pdf-html`** | `chromedp/chromedp` (требует chromium binary, ~200 MB) |
| **DOCX export** | **plugin `railbase-docx`** | если понадобится |
| **PDF preview generation для documents** | **plugin `railbase-pdf-preview`** | `pdftoppm` (poppler) sidecar |

Размер core добавки: ~5 MB (excelize + gopdf).

## API surface — шесть точек входа

### 1. Schema-declarative export

Любая коллекция получает экспорт автоматически, если включено:

```go
var Posts = schema.Collection("posts").
    Field(...).
    Export(
        schema.ExportXLSX(schema.XLSXExportConfig{
            Sheet:   "Posts",
            Columns: []string{"title", "status", "author.email", "created"},
            Headers: map[string]string{"author.email": "Author"},
            // Format maps column keys to Excel number-format codes.
            // **Status: applied** (DSL-3, 2026-05-17). The writer
            // registers one excelize CustomNumFmt style per distinct
            // format code and tags each cell in the column via
            // excelize.Cell{StyleID: ...}, so Excel/LibreOffice render
            // the column with the declared formatting (sortable as
            // numbers, locale-aware separators).
            Format:  map[string]string{
                "created":  "yyyy-mm-dd",
                "subtotal": "$#,##0.00",
                "tax_rate": "0.00%",
            },
        }),
        schema.ExportPDF(schema.PDFExportConfig{
            Template: "posts-report.md",   // relative to pb_data/pdf_templates
            Title:    "Posts Report",
        }),
    )
```

> Note: there is **no** `schema.CellFormat` / `schema.DateFormat(...)` /
> `schema.CurrencyFormat(...)` DSL — the type names in earlier drafts of
> this doc were aspirational. `Format` is a `map[string]string` where
> the value is a raw Excel number-format code (`yyyy-mm-dd`,
> `#,##0.00`, `$#,##0.00`, `0.00%`, ...). Codes share styles internally,
> so 50 columns with the same `"yyyy-mm-dd"` code produce one
> `styles.xml` entry, not 50. For PDF templates, use the `currency`
> template helper (see §Template helpers below).

Это автоматически создаёт endpoints:

- `GET /api/collections/posts/export.xlsx?filter=...&sort=...`
- `GET /api/collections/posts/export.pdf?filter=...&sort=...`

С RBAC-проверкой через тот же `ListRule` коллекции.

### 2. Go API

Реальное API в `pkg/railbase/export` (re-export `internal/export`).

```go
import "github.com/railbase/railbase/pkg/railbase/export"

// XLSX — streaming.
xw, _ := export.NewXLSXWriter("Sales", []export.Column{
    {Key: "date",     Header: "Date"},
    {Key: "amount",   Header: "Amount"},
    {Key: "customer", Header: "Customer"},
})
for _, row := range rows {
    _ = xw.AppendRow(map[string]any{
        "date": row.Date, "amount": row.Amount, "customer": row.CustomerName,
    })
}
_ = xw.Finish(w) // io.Writer — buffered until Finish in v1.6.3

// PDF — программный (для нестандартных layout) или через Markdown
// template (рекомендованный для большинства бизнес-документов).

// Программный — табличный отчёт:
pw, _ := export.NewPDFWriter(
    export.PDFConfig{Title: "Sales report"},
    []export.PDFColumn{
        {Key: "date", Header: "Date", Width: 100},
        {Key: "amount", Header: "Amount", Width: 80},
    },
)
for _, row := range rows {
    _ = pw.AppendRow(map[string]any{"date": row.Date, "amount": row.Amount})
}
_ = pw.Finish(w)

// Markdown → PDF (без on-disk template):
pdfBytes, _ := export.RenderMarkdownToPDF([]byte("# {{.Title}}\nTotal: {{.Total}}"),
    map[string]any{"Title": "Q1 Sales", "Total": "$1,234"})

// Embedded font reuse — для embedder'ов, рендерящих свои PDF через
// gopdf напрямую: DefaultFont() отдаёт байты того же Roboto Regular,
// что использует NewPDFWriter, плюс DefaultFontName для регистрации.
// Без этого embedder копировал 170 KB TTF в свой бинарь (FEEDBACK #31).
pdf := &gopdf.GoPdf{}
pdf.Start(gopdf.Config{PageSize: gopdf.Rect{W: 595, H: 842}})
_ = pdf.AddTTFFontData(export.DefaultFontName, export.DefaultFont())
_ = pdf.SetFont(export.DefaultFontName, "", 12)
```

> Все типы — Go-type-aliases на `internal/export`. Код, написанный
> поверх `pkg/railbase/export.PDFWriter`, проходит туда же куда
> внутренний `*internal/export.PDFWriter` — без обёрток / без копий.

### 3. JS hooks API

```js
routerAdd("GET", "/reports/sales", (c) => {
  const rows = $app.dao().findRecordsByFilter("orders", "status='paid'")
  const xlsx = $export.xlsx({
    sheet: "Sales",
    columns: ["date", "amount", "customer.name"],
    rows: rows,
  })
  return c.binary(200, xlsx, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "sales.xlsx")
})

cronAdd("monthly_invoices", "0 9 1 * *", () => {
  $app.dao().findRecordsByFilter("invoices", "month=@previous_month").forEach(inv => {
    const pdf = $export.pdf({ template: "invoice.md", data: { invoice: inv } })
    $app.storage.save(`invoices/${inv.id}.pdf`, pdf)
    $app.mailer.send({ to: inv.customer.email, attachments: [{ filename: "invoice.pdf", content: pdf }] })
  })
})
```

### 4. REST API

- `GET /api/collections/{name}/export.xlsx?filter=...&columns=...` — sync, streaming response
- `GET /api/collections/{name}/export.pdf?template=...&filter=...` — sync
- `GET /api/collections/{name}/{id}/<docname>.pdf` — **per-entity PDF**
  для документов вида «один заказ + N позиций + summary». Регистрируется
  через `.EntityDoc(...)` (см. §7 ниже).
- `POST /api/exports` — async (для больших данных, через jobs queue): возвращает `job_id`, статус через `GET /api/exports/{job_id}`, готовый файл через signed URL с TTL

Файлы получают timestamp в имени — `orders-20260516-143045.xlsx` —
во избежание `orders (1).xlsx` коллизий в браузере при повторных
download'ах. Если SPA использует `<a download="...">`, атрибут
переопределяет server-side имя — это поведение браузера, не сервера.

### 5. CLI

```
railbase export collection posts --format xlsx --filter "status='published'" --out posts.xlsx
railbase export query "SELECT ... FROM ..." --format xlsx --out report.xlsx
railbase export pdf --template invoice.md --data invoice.json --out invoice.pdf
```

### 6. Admin UI

Кнопка «Export → XLSX/PDF» на любой странице коллекции; учитывает текущий filter/sort/selection.

### 7. Per-entity PDFs (`.EntityDoc()`)

`.Export()` отдаёт **плоский table dump** — все строки коллекции,
сгруппированные в один XLSX/PDF. Это не то же самое, что
«счёт-фактура для одного заказа с line items». Для последнего —
`.EntityDoc(...)`:

```go
var Orders = schema.Collection("orders").
    Field("contact_email", schema.Email().Required()).
    Field("total_cents",   schema.Number().Int()).
    EntityDoc(schema.EntityDocConfig{
        Name:     "invoice",            // → URL slug
        Template: "invoice.md",         // pb_data/pdf_templates/invoice.md
        Title:    "Invoice",
        Related: map[string]schema.RelatedSpec{
            "items": {
                Collection:   "order_items",
                ChildColumn:  "order_ref", // FK column in child
                ParentColumn: "id",        // (default "id" — omit if so)
                OrderBy:      "created ASC",
                Limit:        500,         // default 1000
            },
        },
    })
```

Регистрирует route:

```
GET /api/collections/orders/{id}/invoice.pdf
```

Template получает структурный context:

- `.Record` — `map[string]any` родительской строки
- `.Related["items"]` — `[]map[string]any` дочерних строк
- `.Now` — `time.Time` (UTC) на момент запроса
- `.Tenant` — tenant ID когда parent в tenant-scoped collection

Пример `invoice.md`:

```markdown
# Invoice #{{ slice (str .Record.id) 0 8 }}

Date: {{ .Now | date "2006-01-02" }}
Customer: {{ .Record.contact_email }}

| Product | Qty | Price |
|---------|-----|-------|
{{ range .Related.items }}| {{ .product }} | {{ .qty }} | {{ currency .price_cents .currency }} |
{{ end }}

**Total: {{ currency .Record.total_cents .Record.currency }}**
```

Multiple `.EntityDoc(...)` вызовы на одной коллекции дают несколько
routes (`invoice.pdf`, `receipt.pdf`, …). Все используют тот же
template engine + helpers, что `.Export()` (см. §Helpers ниже).

#### Programmatic renderer (DSL-4, 2026-05-17)

Когда Markdown-шаблон не достаточен — нужна Go-функция, которая
читает Record + Related и возвращает готовые PDF-байты (свой layout,
сторонняя библиотека, watermark, цифровая подпись и т.д.) —
заполняйте `Renderer` вместо `Template`:

```go
import "github.com/railbase/railbase/internal/schema/builder"

EntityDoc(schema.EntityDocConfig{
    Name: "invoice",
    Renderer: func(ctx builder.EntityDocContext) ([]byte, error) {
        // ctx.Record / ctx.Related / ctx.Tenant / ctx.Now — те же,
        // что увидел бы шаблон. Верните финальные PDF-байты.
        return generateInvoicePDF(ctx.Record, ctx.Related["items"])
    },
})
```

Renderer **не сериализуется в JSON** (поле `json:"-"`) — динамические
коллекции из `_admin_collections.spec` поднять Go-функцию не могут.
Граница ясная: programmatic путь живёт только в Go-коде, шаблонный —
доступен и через JSON-спеки. Когда `Renderer != nil`, шаблон
игнорируется полностью, инициализация `pb_data/pdf_templates`
embedder'ам с чисто-программными доками не нужна.

**RBAC** (v1.7.50+): handler идёт через ту же compose/build/queryFor
chain, что и обычный `GET /api/collections/{name}/records/{id}` —
ViewRule, tenant scoping и soft-delete filtering применяются единым
кодом. Owner-only инвойс с `viewRule = "customer = @request.auth.id"`
возвращает 404 non-owner'у (тот же existence-hiding контракт, что у
regular GET-one: не 403, чтобы не разглашать существование строки).

> Pre-v1.7.50 history: до этой версии handler делал плоский
> `SELECT * FROM <collection> WHERE id = $1` без `ViewRule` — а
> doc-comment в исходнике лгал, что rule применяется. Это был
> реальный RBAC bypass (любой держатель UUID получал PDF).
> Регрессионный тест — `TestEntityDoc_ViewRuleApplied` в
> `internal/api/rest/entity_doc_test.go`: capturing `pgQuerier`
> подтверждает, что compiled ViewRule fragment (`customer = $N`)
> присутствует в WHERE, и auth-UUID связан как параметр.

### 8. Short-lived signed download URLs

`<a href="/api/.../export.xlsx?token={authToken}" download>` ведёт к
утечке полного session token в browser history / nginx logs /
shared URLs. Для коротко-живущих, path-scope ссылок —
`pkg/railbase/dltoken`:

```go
import "github.com/railbase/railbase/pkg/railbase/dltoken"

// Эндпоинт, минтящий ссылки. Embedder реализует:
r.Post("/api/exports/sign", func(w http.ResponseWriter, r *http.Request) {
    // ... обычная аутентификация ...
    path := r.URL.Query().Get("path") // e.g. "/api/collections/orders/export.xlsx"
    tok, exp, err := dltoken.Sign(app.Secret().HMAC(), path, dltoken.SignOptions{})
    if err != nil {
        http.Error(w, err.Error(), 400)
        return
    }
    json.NewEncoder(w).Encode(map[string]any{
        "download_url": path + "?dt=" + tok,
        "expires_at":   exp,
    })
})
```

```ts
// SPA
const { download_url } = await api.post('/api/exports/sign',
    { path: '/api/collections/orders/export.xlsx' })
window.location.href = download_url   // живёт 60 сек, single shot
```

- **Stateless** — HMAC-SHA-256 (`secret, path|expiry`) + base64url,
  expiry зашит в токен. Без shared storage.
- **Defaults**: TTL 60 s, hard cap 5 min.
- **Path-scoped**: токен валиден только для URL, на который выписан.
  Подмена `path` ломает HMAC → ErrInvalid.
- **Constant-time compare** + HMAC validation ДО expiry check, чтобы
  атакующий не отличал «forged» от «expired» по ответу.
- **Single-use НЕ обеспечен** на этом уровне — replay-окно ограничено
  TTL. Для true one-shot нужен shared store (Redis / DB) — это
  v1.x bonus.

## Templates — лёгкая модель без bloat

PDF templates в core — **markdown с frontmatter** (тот же engine как mailer):

```markdown
---
title: "Invoice {{invoice.number}}"
header: "Acme Corp"
footer: "Page {{page}} of {{total}}"
margins: { top: 20, bottom: 20, left: 25, right: 25 }
---

# Invoice {{invoice.number}}

**Customer**: {{invoice.customer.name}}
**Date**: {{invoice.date | date "2006-01-02"}}

| Item | Qty | Price | Total |
|------|-----|-------|-------|
{{#each invoice.items}}
| {{this.name}} | {{this.qty}} | {{this.price}} | {{this.total}} |
{{/each}}

**Total: {{invoice.total | money}}**
```

Engine: `gomarkdown/markdown` для парсинга + Go `text/template` для variables (стандартная stdlib, не bringing in handlebars/Jinja).

### Helpers

- `date "layout" v` — format `time.Time` with a Go layout (`"2006-01-02"`, `"Jan 2, 2006"`).
- `currency cents code` — integer minor-units → "$1,234.56" / "₽1 234,56".
  Supports USD / EUR / GBP / RUB / JPY / CNY / INR; unknown codes fall back
  to `XYZ 1,234.56`. FEEDBACK #34.
- `money v` — convenience shortcut for `currency v "USD"`.
- `str v` — coerce any value to string. Use this for UUID slicing:
  `{{ slice (str .id) 0 8 }}` produces `fec43944` without an explicit
  `printf "%v"`. FEEDBACK #33.
- `truncate N s` — rune-aware truncate with ellipsis.
- `default fallback v` — fallback value when `v` is zero.
- `each` — alias of stdlib `range` (kept for docs/08 compatibility;
  prefer `{{ range .Items }}` directly).
- `if`, `range`, `with`, `block`, ... — all `text/template` built-ins
  are available.

Context fields (`.`) every template receives:

- `.Records` — `[]map[string]any` of filter-matched rows
- `.Tenant` — tenant ID string (`""` when not tenant-scoped)
- `.Now` — `time.Time` at request time
- `.Filter` — raw filter expression (`""` when none)

The FuncMap is fixed for v1.6.x; arbitrary user-supplied helpers ship
in a follow-up. For now, embedders who need a custom helper write a
plain Go handler that builds the PDF via `pkg/railbase/export.NewPDFWriter`.

Для сложных layout (multi-column, exact pixel placement) — Go API (`export.NewPDF()`) даёт programmatic access к gopdf primitives.

## Streaming & memory pressure

Critical: не тащить весь dataset в память.

- **Streaming writer pattern** — `excelize.StreamWriter` для XLSX (excelize built-in support)
- **Cursor-based DB iteration** — не `SELECT * INTO slice`, а `rows.Next()` + flush per N rows
- **Memory ceiling per request** — soft limit (`EXPORT_MEMORY_LIMIT_MB`, default 256 MB); превышение → kill request, возвращает 413, audit row
- **Async mode для тяжёлого** — экспорт > 100k rows автоматически шунтируется в jobs queue (через `POST /api/exports`); admin UI показывает progress

## RBAC & audit

- Export использует тот же RBAC, что и list: если actor может `posts.list` — он может `posts.export`. Без отдельного permission.
- Каждый export пишется в audit с `event="export"`, `details={format, columns, filter, row_count}`. Compliance-кейсы видят кто что выкачивал.
- Per-tenant quotas (multi-tenant): max exports/hour, max rows/export. Через `railbase-orgs` plugin.

## Что НЕ делаем в core

- Excel macros/VBA generation
- Advanced PDF features: digital signatures (`railbase-esign` plugin), forms, annotations
- DOCX export (нет хорошего pure-Go writer'а; plugin `railbase-docx`)
- ODS / другие spreadsheet форматы
- Multi-page reports с complex layout (Crystal Reports style)

## Open questions

- **HTML→PDF strategy**: plugin `railbase-pdf-html` через chromedp (~200 MB Chrome) — правильный путь, или искать pure-Go HTML→PDF? Альтернатива: docs sidecar контейнер с weasyprint/playwright.
- **Template language**: markdown + Go-template (выбрано) vs Liquid vs Handlebars. Go-template — minimal deps, но менее знакомый.
- **Native chart rendering в PDF**: gopdf не имеет built-in charts; для PDF charts — render в PNG через `go-chart` и embed как image.

## Templates shared с mailer

PDF templates и email templates используют один template engine. Это даёт:

- Consistent syntax между PDF reports и email templates
- Shared helpers
- Один template можно использовать как email и как PDF attachment

См. [09-mailer.md](09-mailer.md) для email-specific extensions.

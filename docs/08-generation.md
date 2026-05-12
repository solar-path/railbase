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
        schema.ExportXLSX(schema.XLSXConfig{
            Sheet:   "Posts",
            Columns: []string{"title", "status", "author.email", "created"},
            Headers: map[string]string{"author.email": "Author"},
            Format:  map[string]schema.CellFormat{"created": schema.DateFormat("YYYY-MM-DD")},
        }),
        schema.ExportPDF(schema.PDFConfig{
            Template: "templates/posts-report.md",
            Title:    "Posts Report",
        }),
    )
```

Это автоматически создаёт endpoints:

- `GET /api/collections/posts/export.xlsx?filter=...&sort=...`
- `GET /api/collections/posts/export.pdf?filter=...&sort=...`

С RBAC-проверкой через тот же `ListRule` коллекции.

### 2. Go API

```go
import "github.com/railbase/railbase/pkg/railbase/export"

writer := export.NewXLSX(config)
writer.Sheet("Sales")
writer.Headers([]string{"Date", "Amount", "Customer"})
for row := range rows {
    writer.AppendRow([]any{row.Date, row.Amount, row.Customer})
}
writer.WriteTo(w)  // io.Writer — streaming-friendly
```

PDF:

```go
pdf := export.NewPDF(export.PDFConfig{
    Template: "invoice.md",
    Data:     map[string]any{"invoice": inv, "company": cmp},
})
pdf.WriteTo(w)
```

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
- `POST /api/exports` — async (для больших данных, через jobs queue): возвращает `job_id`, статус через `GET /api/exports/{job_id}`, готовый файл через signed URL с TTL

### 5. CLI

```
railbase export collection posts --format xlsx --filter "status='published'" --out posts.xlsx
railbase export query "SELECT ... FROM ..." --format xlsx --out report.xlsx
railbase export pdf --template invoice.md --data invoice.json --out invoice.pdf
```

### 6. Admin UI

Кнопка «Export → XLSX/PDF» на любой странице коллекции; учитывает текущий filter/sort/selection.

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

- `date` — formatting с Go layout strings
- `money` — currency formatting (locale-aware)
- `truncate` — string truncation
- `default` — fallback value
- `each` — iteration
- `if` — conditionals

User-extensible через Go-side регистрацию helpers.

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

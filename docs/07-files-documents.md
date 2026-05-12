# 07 — Files & documents

Два разных слоя:

1. **File fields** — inline attachments на коллекции (avatar на user, cover на post). PB-style.
2. **Document management** — отдельный repository логических документов с версиями, polymorphic owner, lifecycle, retention. Rail-style extension.

## File fields (inline)

PB-paritет. См. [03-data-layer.md](03-data-layer.md#field-types) для DSL definition.

### Upload flow

```
POST /api/collections/{name}/records (multipart/form-data)
  → middleware: rate limit, size check, mime check
  → parse multipart → for each file field:
     → validate (MIME, size, count)
     → compute SHA-256 hash
     → write to storage с layout: {tenant_id?}/{collection}/{record_id}/{hash8}_{filename}
  → audit row
  → realtime publish RecordCreated/Updated
```

### Image thumbnails (built-in)

Для image-полей — auto-generated thumbnails:

```go
schema.File().Image().Thumbs(
    schema.Thumb("100x100", schema.FitCover),
    schema.Thumb("400x", schema.FitContain),    // 400 wide, height auto
    schema.Thumb("0x300t", schema.FitTop),      // 300 tall, top-aligned
)
```

URL access: `GET /api/files/{collection}/{record_id}/{filename}?thumb=100x100`

Implementation: `disintegration/imaging` (pure Go, без CGo). Thumbnails generated lazy on-first-request, cached in storage с TTL. Origin file всегда сохраняется.

Supported formats: JPEG, PNG, WebP, GIF (single-frame). Не делаем: HEIC (CGo), TIFF, raw camera formats. AVIF — opt-in plugin.

### File metadata extraction

Optional (`schema.File().ExtractMetadata()`):

- Image: dimensions, EXIF, color profile
- PDF: page count, title, author (если permission)
- Office docs: SKIP в core (heavy library); plugin `railbase-doc-meta`

Stored в record как nested JSON: `{ filename, size, mime, hash, metadata: {...} }`.

### Streaming

- Upload: multipart с MAX_MEMORY=10MB (default), остальное → disk через `multipart.Reader`
- Download: `http.ServeContent` для range requests (video streaming work)
- Concurrent uploads: max-concurrency per-user limit (default 3)

### Storage drivers

| Driver | Где | Use case |
|---|---|---|
| FS (local) | core | Dev, small deploys |
| S3-compatible | core (`minio-go/v7`) | AWS S3, Cloudflare R2, MinIO, Backblaze B2 |
| Azure Blob | plugin | если понадобится |
| GCS | plugin | если понадобится |

Driver selected via `RAILBASE_STORAGE` URL: `fs:./storage`, `s3://bucket?endpoint=...&region=...`.

### Signed URLs

Public access к private files через signed URLs (HMAC-based, expiry):

```
GET /api/files/.../filename?token=...&expires=...
```

JS hooks: `$app.storage.signURL(path, ttl)`.

### Virus scanning

Optional integration через webhooks:

- On upload, file path → POST к configured `VIRUS_SCAN_WEBHOOK`
- If response = malicious → file deleted, record rejected, audit
- ClamAV / VirusTotal / Cloudmersive — на стороне пользователя

Not in core.

### Что НЕ делаем (file fields)

- Image editing (crop/rotate/filters) — admin-UI функция, не backend
- Video transcoding — out of scope
- Office document preview generation — plugin

---

## Document management — first-class repository

PB имеет file fields (плоская модель). Rail добавляет document-as-entity слой ([uploads.documents.service.ts](src/modules/shared/uploads/server/uploads.documents.service.ts)). Railbase портирует в core.

**Не путать с file fields**: file fields — для inline-attachments на коллекции. Documents — отдельный repository с версиями, polymorphic owner, lifecycle, search, retention.

### Концептуальная модель

```
Document (logical entity)         — что пользователь воспринимает как «документ»
  └─ Versions (physical bytes)    — N файлов; currentVersionId → последняя
       └─ Storage (SHA-256 hash)  — content-addressed, deduplicated
```

Один document = один логический файл (e.g. «Vendor Agreement 2026 — Acme Corp»); версии — конкретные uploads. Re-upload с тем же `(owner, title)` → новая версия, не дубль.

### Polymorphic owner — главная ценность

Document может быть прикреплён к ЛЮБОЙ записи:

```
ownerType: "user" | "tenant" | "vendor" | "purchaseOrder" | "vendorInvoice"
         | "contract" | "employee" | "payslip" | "leaveRequest" | "taxRule"
         | "inventoryItem" | "treasuryPaymentBatch" | ... | "misc"
ownerId:   <UUID указывающий на конкретную запись>
```

В rail список захардкожен (45+ owner types для всех ERP-доменов). В Railbase: **owner type выводится из schema-DSL** — любая коллекция, помеченная `.AllowsDocuments()`, автоматически становится валидным owner type.

```go
var Vendors = schema.Collection("vendors").
    Field("name", schema.Text()).
    AllowsDocuments(schema.DocumentsConfig{
        MaxSize:        100 * MB,
        MaxCount:       50,
        AllowedMime:    []string{"application/pdf", "image/*"},
        Categories:     []string{"contract", "tax_form", "registration"},
        RetentionDays:  365 * 7,
    })
```

### Tables (core)

```
_documents               — id, tenant_id (nullable=site-scope), owner_type, owner_id,
                            title, mime_primary, current_version_id, archived_at,
                            category (nullable), retention_until (nullable),
                            legal_hold (bool), created_by, created/updated_at
_document_versions       — id, document_id, version_no, file_size, hash_sha256,
                            storage_key, mime, uploaded_by, uploaded_at, comment
_document_quota          — tenant_id, used_bytes, doc_count
_document_access_log     — document_id, version_id, actor_id, action
                            (view/download/preview), ts, ip, ua
_document_extracted_text — document_id, version_id, text, extracted_at  (если opt-in extraction)
```

### Critical rules (из rail)

1. **Immutable repository — NO DELETE** — только `archivedAt` (soft archive). Compliance-friendly. `--documents-allow-hard-delete` для dev only.
2. **Title uniqueness в scope (tenant + owner_type + owner_id)** — same title = новая версия. Никаких «vendor_v2_FINAL_FINAL.pdf» дубликатов.
3. **Tenant scope** — `tenantId=null` = site-scope (только system admin); `tenantId=X` = workspace-scope.
4. **Cross-workspace admin oversight** — admin может listAll с `crossWorkspace: true` (audit обязательный). Procedure layer enforce'ит RBAC.
5. **Aggregate stats inline** — list view возвращает `versionCount + totalBytes` без N+1 round trips (via groupBy).
6. **Storage = content-addressed** — `pb_data/storage/{tenant_id}/{document_id}/v{N}_{hash8}.{ext}`. SHA-256 хэш = identity; deduplication возможна.
7. **Legal hold** — bool flag блокирует archive до снятия hold. Critical для litigation/compliance.
8. **Retention** — `retention_until` timestamp; auto-archival job; permanent delete только после retention expiry + admin confirmation.

### REST endpoints (core)

```
POST   /api/documents                                 — upload (multipart)
GET    /api/documents?ownerType=...&ownerId=...
GET    /api/documents/{id}                            — metadata + version stats
GET    /api/documents/{id}/versions
POST   /api/documents/{id}/versions                   — добавить новую версию
GET    /api/documents/{id}/versions/{versionNo}/download
GET    /api/documents/{id}/versions/{versionNo}/preview
PATCH  /api/documents/{id}                            — rename, change category, set legal_hold
POST   /api/documents/{id}/archive
POST   /api/documents/{id}/restore                    — отмена archive (если retention не истёк)
GET    /api/documents/quota
GET    /api/documents/search?q=...                    — FTS по title + extracted text
```

### Schema DSL — auto-mounted endpoints

`.AllowsDocuments()` на коллекции → endpoints мигрируют:

```
GET  /api/collections/vendors/{vendorId}/documents
POST /api/collections/vendors/{vendorId}/documents
```

### JS hooks

```js
onDocumentUploaded((e) => {
  if (e.document.ownerType === "vendorInvoice" && e.document.mime === "application/pdf") {
    $jobs.enqueue("extract_invoice_data", { documentId: e.document.id, versionId: e.version.id })
  }
})

onDocumentArchived((e) => {
  $app.realtime().publish(`documents.${e.document.owner_type}.${e.document.owner_id}.archived`, e.document)
})

onDocumentAccessed((e) => {
  // Audit-friendly: track who viewed sensitive docs
})

onRecordAfterDelete("vendors", (e) => {
  $documents.archiveAllByOwner("vendor", e.record.id, { reason: "vendor_deleted" })
})
```

### Go API

```go
import "github.com/railbase/railbase/pkg/railbase/documents"

doc, err := documents.Upload(ctx, documents.UploadInput{
    TenantID:  tenantID,
    OwnerType: "vendor", OwnerID: vendorID,
    Title:     "Master Service Agreement 2026",
    File:      reader, FileName: "msa.pdf",
    UploadedBy: actorID,
})
// если такой (owner, title) уже есть → автоматически становится новой версией
```

### Quotas

Per-tenant квоты (`_document_quota`):

- `max_bytes` — total storage (e.g. 10 GB на free, 1 TB на enterprise)
- `max_doc_count`
- `max_doc_size` — single doc (default 100 MB)

Превышение → 413 + audit `quota.exceeded`. UI shows usage bar.

С `railbase-orgs` plugin — quotas tied to subscription plan; auto-bumped при upgrade.

### Search & extraction

- **FTS на title** — всегда (через core FTS infrastructure)
- **Text extraction из PDF** — opt-in через flag `--documents-extract-text`. Library: `ledongthuc/pdf` (pure Go, basic) для simple PDFs; для OCR / scanned — plugin `railbase-doc-ocr` через Tesseract sidecar.
- **Office docs extraction** — plugin `railbase-doc-office` (libreoffice-headless как sidecar)

Extracted text stored в `_document_extracted_text` (separate table, lazy-populated by background job after upload).

### Preview generation

- **Image** — встроенно через `disintegration/imaging` (thumbnails, как у file fields)
- **PDF** — page-1 preview как image; plugin `railbase-pdf-preview` через `pdftoppm` (poppler) sidecar
- **Office docs** — через `railbase-doc-office` plugin
- **Video** — frame extraction через ffmpeg sidecar (plugin)

В core: только image previews. PDF/Office/Video — opt-in plugins.

### Admin UI screen — Documents

См. [12-admin-ui.md](12-admin-ui.md#21-documents-core).

### Integration с другими системами

- **Audit**: каждый upload/version/archive/restore/access пишется в `_audit_log` + `_document_access_log` (granular, для compliance)
- **Realtime**: events publish; approvers видят upload в real-time
- **Hooks**: `onDocumentUploaded`, `onDocumentVersionCreated`, `onDocumentArchived`, `onDocumentAccessed`
- **Authority** (если plugin): document upload может trigger approval (`AllowsDocuments(...).RequireApproval(policy)`)
- **Export**: collection export `.xlsx` может включать documents references; PDF export для invoice-template может attach related documents

### Retention & compliance

- **`retention_until`** — auto-archival job помечает archived после expiry
- **`legal_hold`** — блокирует archive/delete до снятия (overrides retention)
- **Permanent delete** — только manual через CLI `railbase documents purge --document-id ... --confirm` (audit обязательный, irreversible)
- **GDPR right-to-erasure** — separate flow: `DELETE /api/documents/{id}/erase-pii` обнуляет identifying fields в metadata, но оставляет hash + audit trail

### Schema integration patterns

#### Pattern 1: Documents tab на любой записи

```go
schema.Collection("vendors").AllowsDocuments(...)
```

Admin UI auto-mount Documents tab на vendor detail page.

#### Pattern 2: Required document для completion

```go
schema.Collection("contracts").
    Field("status", schema.Enum("draft", "signed", "active")).
    RequireDocument(schema.RequiredDocument{
        Category: "signed_pdf",
        ForStatus: "signed",  // не позволяет переход в "signed" без attached signed pdf
    })
```

#### Pattern 3: Auto-attach generated PDF

```go
schema.Collection("invoices").
    Hook(schema.AfterCreate, func(ctx, r *Record) error {
        pdf := export.NewPDF(...)
        return documents.Upload(ctx, documents.UploadInput{
            OwnerType: "invoice", OwnerID: r.ID,
            Title: "Invoice " + r.GetString("number"),
            File: pdf,
        })
    })
```

### Что НЕ делает (фиксированный scope core)

- Real-time collaborative editing (Google Docs-style) — out of scope
- Document signing workflows — отдельный plugin `railbase-esign` (DocuSign/HelloSign integrations)
- AI-based summary / extraction — `railbase-mcp` plugin интегрирует LLMs к documents API
- Watermarking — plugin при необходимости
- Encryption at rest для documents — наследует core `--encrypt-storage` flag (open question)

### Open questions

- **Hard-delete для dev**: дать `--documents-allow-hard-delete` flag (true в dev, false в prod) или strict immutable?
- **Text extraction в core или plugin**: pure-Go PDF parsers limited; рекомендация opt-in flag в core с simple PDFs, OCR/Office в plugins
- **GDPR erasure** — semantics: erase metadata vs erase bytes? Bytes erasure ломает hash chain audit; metadata erasure обычно достаточно
- **Cross-tenant document references**: contract между tenants A и B — кто owns?
- **Versioning после rename** — если user rename document, старые versions remember old title или migrate? Recommend: keep version history с per-version title

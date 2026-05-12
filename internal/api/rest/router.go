package rest

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/eventbus"
	"github.com/railbase/railbase/internal/export"
	"github.com/railbase/railbase/internal/hooks"
)

// Mount installs the v0.3.1 generic CRUD routes under the given chi
// router. Routes (PB-compat):
//
//	GET    /api/collections/{name}/records
//	GET    /api/collections/{name}/records/{id}
//	POST   /api/collections/{name}/records
//	PATCH  /api/collections/{name}/records/{id}
//	DELETE /api/collections/{name}/records/{id}
//
// The collection lookup happens per-request against the global
// schema registry — Mount itself is registry-agnostic.
//
// `pool` is anything implementing pgQuerier (real *pgxpool.Pool in
// production; fakes in tests). `log` is non-nil; pass slog.Default()
// if you have nothing better. `hooksRT` is the v1.2.0 JS hooks runtime;
// pass nil to skip hook dispatch. `bus` is the v1.3.0 realtime
// publisher — pass nil to skip realtime publish. `fd` is the v1.3.1
// file-storage deps — pass nil to skip file upload routes (signed
// download routes also skipped).
// `pdfTpl` is the v1.6.4 PDF Markdown-template loader. Pass nil to
// disable template-driven PDF export; the PDF endpoint then always
// renders the v1.6.1 data-table layout. Operators wanting templates
// must provide a loader rooted at their template directory (default
// `pb_data/pdf_templates`).
func Mount(r chi.Router, pool pgQuerier, log *slog.Logger, hooksRT *hooks.Runtime, bus *eventbus.Bus, fd *FilesDeps, pdfTpl *export.PDFTemplates) {
	MountWithAudit(r, pool, log, hooksRT, bus, fd, pdfTpl, nil)
}

// MountWithAudit is Mount + an audit Writer for export-row emission
// (v1.6.5 / v1.6.6 polish). The Writer is optional — pass nil and the
// export handlers silently no-op on audit emission. Kept as a separate
// entry point so the many existing Mount() call sites in tests and
// downstream apps don't need to grow an extra arg.
func MountWithAudit(r chi.Router, pool pgQuerier, log *slog.Logger, hooksRT *hooks.Runtime, bus *eventbus.Bus, fd *FilesDeps, pdfTpl *export.PDFTemplates, auditW *audit.Writer) {
	d := &handlerDeps{pool: pool, log: log, hooks: hooksRT, bus: bus, filesDeps: fd, pdfTemplates: pdfTpl, audit: auditW}

	r.Route("/api/collections/{name}/records", func(r chi.Router) {
		r.Get("/", d.listHandler)
		r.Post("/", d.createHandler)
		// v1.4.13 batch ops: atomic (single tx) or non-atomic (207
		// Multi-Status). Body: `{atomic: bool, ops: [{action, id?, data?}]}`.
		// Registered BEFORE "/{id}" so "batch" doesn't get matched as
		// a record ID.
		r.Post("/batch", d.batchHandler)
		r.Get("/{id}", d.viewHandler)
		r.Patch("/{id}", d.updateHandler)
		r.Delete("/{id}", d.deleteHandler)
		// v1.4.12 soft-delete: restore a tombstoned row. The handler
		// 404s on non-soft-delete collections or on rows that aren't
		// tombstoned. Same UpdateRule applies (need to be authorised
		// to mutate the row).
		r.Post("/{id}/restore", d.restoreHandler)
	})

	// v1.6.0 XLSX export. Sync only — async/.Export()/JS-hook surface
	// lands in v1.6.x follow-ups. ListRule is reused for RBAC (anyone
	// who can list can export), so this route shares the same access
	// decision as listHandler.
	r.Get("/api/collections/{name}/export.xlsx", d.exportXLSXHandler)
	// v1.6.1 PDF export. Same RBAC / filter chain as XLSX. Sync only;
	// row cap is 10x stricter (10k vs 100k) because gopdf holds the
	// whole document in memory until flush.
	r.Get("/api/collections/{name}/export.pdf", d.exportPDFHandler)

	if fd != nil {
		MountFiles(r, d, fd)
	}
}

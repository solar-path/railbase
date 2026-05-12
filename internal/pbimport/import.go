package pbimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// FetchOptions controls the HTTP fetch from a PB instance.
type FetchOptions struct {
	// BaseURL is the PB instance root, e.g. "https://my.pocketbase.io".
	// Trailing slashes are tolerated.
	BaseURL string
	// Token is a PB admin token (`Authorization: Bearer <tok>`). PB's
	// /api/collections is admin-only, so empty token = 403.
	Token string
	// Client overrides the http.Client. Default: http.DefaultClient
	// with a 30s timeout.
	Client *http.Client
}

// Fetch GETs /api/collections from a PB instance and returns the
// parsed list. PB paginates with default perPage 30; we request 200
// (PB's max) and don't bother with multi-page — a typical project has
// < 50 collections.
func Fetch(ctx context.Context, opts FetchOptions) (*CollectionsList, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("pbimport: BaseURL required")
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/api/collections?perPage=200&sort=name", nil)
	if err != nil {
		return nil, fmt.Errorf("pbimport: request: %w", err)
	}
	if opts.Token != "" {
		// PB v0.22+ uses raw Bearer; pre-0.22 used "Admin <tok>". We
		// target 0.22+ for v1.7.8 — pre-0.22 instances would need a
		// `--legacy-auth` flag if anyone asks.
		req.Header.Set("Authorization", opts.Token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pbimport: http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("pbimport: HTTP %d from PB: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var list CollectionsList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("pbimport: decode: %w", err)
	}
	return &list, nil
}

// EmitOptions controls the Go-code emission.
type EmitOptions struct {
	// Package is the package name written at the top of the file.
	// Default "schema".
	Package string
	// Source is a human-readable origin URL placed in the file
	// header comment so future readers know where the schema came
	// from. Default the BaseURL passed to Fetch.
	Source string
}

// Emit translates a PB CollectionsList into a Go source file using
// the Railbase schema builder. Output goes to w; the function returns
// nil on success or a translation error.
//
// The emitted file imports `github.com/railbase/railbase/pkg/railbase/schema`
// and defines an `init()` that registers each collection. Operators
// drop the file into their project and `import _ "..."` from main.
func Emit(w io.Writer, list *CollectionsList, opts EmitOptions) error {
	if opts.Package == "" {
		opts.Package = "schema"
	}
	// Build a name-by-ID map so relation fields can look up the target
	// collection's NAME (PB stores the relation's target as an ID, but
	// our builder takes a name). This is the only cross-collection
	// dependency in the translation.
	nameByID := map[string]string{}
	for _, c := range list.Items {
		nameByID[c.ID] = c.Name
	}

	// System collections from PB (`_admins`, `_otps`, `_externalAuths`,
	// `_mfas`, `_authOrigins`) are skipped — Railbase has its own
	// equivalents wired via system migrations.
	var collections []emittedCollection
	for _, c := range list.Items {
		if c.System {
			continue
		}
		if c.Type == "view" {
			// Railbase doesn't ship view collections in v1. Skip with
			// a banner comment so the operator knows what was dropped.
			collections = append(collections, emittedCollection{
				Name:    c.Name,
				Skipped: "PB view collection — Railbase v1 doesn't ship views",
			})
			continue
		}
		ec, err := translateCollection(c, nameByID)
		if err != nil {
			return fmt.Errorf("pbimport: collection %q: %w", c.Name, err)
		}
		collections = append(collections, ec)
	}
	// Deterministic emission order (alphabetical by name).
	sort.Slice(collections, func(i, j int) bool {
		return collections[i].Name < collections[j].Name
	})

	data := struct {
		Package     string
		Source      string
		GeneratedAt string
		Collections []emittedCollection
	}{
		Package:     opts.Package,
		Source:      opts.Source,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Collections: collections,
	}

	var buf bytes.Buffer
	if err := outputTemplate.Execute(&buf, data); err != nil {
		return fmt.Errorf("pbimport: template: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("pbimport: write: %w", err)
	}
	return nil
}

// emittedCollection is the template's view of one collection. Wider
// shape than schema.Collection so the template can render the auth
// branch differently.
type emittedCollection struct {
	Name       string
	IsAuth     bool
	Fields     []emittedField
	Rules      emittedRules
	AuthOpts   AuthOpts
	Skipped    string // when non-empty, emit a banner comment instead of the builder
}

type emittedField struct {
	// CallChain is the rendered `.Required().Min(...).Pattern(...)`
	// suffix appended after the type constructor. Empty for plain types.
	Name      string
	Type      string // already-rendered Go expression, e.g. `schema.Text()` or `schema.Select("a","b")`
	CallChain string
}

type emittedRules struct {
	List   *string
	View   *string
	Create *string
	Update *string
	Delete *string
}

// translateCollection converts one PB collection into our emit shape.
func translateCollection(c Collection, nameByID map[string]string) (emittedCollection, error) {
	ec := emittedCollection{
		Name:   c.Name,
		IsAuth: c.Type == "auth",
		Rules: emittedRules{
			List:   c.ListRule,
			View:   c.ViewRule,
			Create: c.CreateRule,
			Update: c.UpdateRule,
			Delete: c.DeleteRule,
		},
		AuthOpts: c.Options,
	}
	for _, f := range c.Schema {
		ef, err := translateField(f, nameByID)
		if err != nil {
			return ec, fmt.Errorf("field %q: %w", f.Name, err)
		}
		ec.Fields = append(ec.Fields, ef)
	}
	return ec, nil
}

// translateField is the dispatch over PB field types. For unknown
// types we emit `schema.JSON()` with a TODO comment in the call chain
// so the operator notices.
func translateField(f Field, nameByID map[string]string) (emittedField, error) {
	ef := emittedField{Name: f.Name}
	var chain strings.Builder

	if f.Required {
		chain.WriteString(".Required()")
	}
	if f.Unique {
		// PB pre-v0.22 stored unique flag inline; later versions push
		// into indexes which we ignore. Either way, the builder call is
		// the same.
		chain.WriteString(".Unique()")
	}

	switch f.Type {
	case "text":
		ef.Type = "schema.Text()"
		if min := numFromOpts(f.Options, "min"); min > 0 {
			fmt.Fprintf(&chain, ".Min(%d)", int(min))
		}
		if max := numFromOpts(f.Options, "max"); max > 0 {
			fmt.Fprintf(&chain, ".Max(%d)", int(max))
		}
		if pat, ok := f.Options["pattern"].(string); ok && pat != "" {
			fmt.Fprintf(&chain, ".Pattern(%q)", pat)
		}
	case "number":
		ef.Type = "schema.Number()"
		if min := numFromOpts(f.Options, "min"); !isUnsetNum(f.Options, "min") {
			fmt.Fprintf(&chain, ".Min(%v)", min)
		}
		if max := numFromOpts(f.Options, "max"); !isUnsetNum(f.Options, "max") {
			fmt.Fprintf(&chain, ".Max(%v)", max)
		}
	case "bool":
		ef.Type = "schema.Bool()"
	case "email":
		ef.Type = "schema.Email()"
	case "url":
		ef.Type = "schema.URL()"
	case "date":
		ef.Type = "schema.Date()"
	case "select":
		values := stringsFromOpts(f.Options, "values")
		maxSel := numFromOpts(f.Options, "maxSelect")
		if maxSel > 1 {
			ef.Type = fmt.Sprintf("schema.MultiSelect(%s)", quotedJoin(values))
		} else {
			ef.Type = fmt.Sprintf("schema.Select(%s)", quotedJoin(values))
		}
	case "json":
		ef.Type = "schema.JSON()"
	case "file":
		maxSel := numFromOpts(f.Options, "maxSelect")
		if maxSel > 1 {
			ef.Type = "schema.Files()"
		} else {
			ef.Type = "schema.File()"
		}
		if mimes := stringsFromOpts(f.Options, "mimeTypes"); len(mimes) > 0 {
			fmt.Fprintf(&chain, ".AcceptMIME(%s)", quotedJoin(mimes))
		}
		if maxFileSize := numFromOpts(f.Options, "maxSize"); maxFileSize > 0 {
			fmt.Fprintf(&chain, ".MaxSize(%d)", int(maxFileSize))
		}
	case "relation":
		// PB stores the target as a collectionId. Look up the name.
		targetID, _ := f.Options["collectionId"].(string)
		target, ok := nameByID[targetID]
		if !ok {
			target = "/* TODO: unknown collectionId " + targetID + " */"
		}
		maxSel := numFromOpts(f.Options, "maxSelect")
		if maxSel > 1 {
			ef.Type = fmt.Sprintf("schema.Relations(%q)", target)
		} else {
			ef.Type = fmt.Sprintf("schema.Relation(%q)", target)
		}
	case "editor":
		ef.Type = "schema.RichText()"
	case "password":
		ef.Type = "schema.Password()"
	default:
		// Unknown type — emit JSON with a TODO so it compiles + the
		// operator knows to revisit.
		ef.Type = "schema.JSON() // TODO: PB type " + f.Type + " not translated"
	}
	ef.CallChain = chain.String()
	return ef, nil
}

// numFromOpts safely extracts a float64 from the options map. PB
// emits all numbers as JSON numbers (float64 after unmarshal).
func numFromOpts(o map[string]any, key string) float64 {
	if v, ok := o[key]; ok {
		switch x := v.(type) {
		case float64:
			return x
		case int:
			return float64(x)
		case json.Number:
			f, _ := x.Float64()
			return f
		}
	}
	return 0
}

// isUnsetNum returns true if the options map either has no key or has
// the JSON value `null`. Used to distinguish "min=0 (set)" from "min
// unset" — only the former emits a `.Min(0)` call.
func isUnsetNum(o map[string]any, key string) bool {
	v, ok := o[key]
	return !ok || v == nil
}

// stringsFromOpts safely extracts a []string. PB emits arrays as
// []any after unmarshal — we re-type each element.
func stringsFromOpts(o map[string]any, key string) []string {
	v, ok := o[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// quotedJoin renders a Go-source-friendly comma-joined list of quoted
// strings. Example: ["a", "b"] → `"a", "b"`.
func quotedJoin(values []string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(parts, ", ")
}

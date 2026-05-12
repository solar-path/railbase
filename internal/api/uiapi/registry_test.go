package uiapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
)

// fakeFS builds a fstest.MapFS that mimics the shape of admin.UIKit():
//
//	src/styles.css
//	src/lib/ui/cn.ts
//	src/lib/ui/icons.tsx
//	src/lib/ui/_primitives/portal.tsx
//	src/lib/ui/_primitives/popper.tsx
//	src/lib/ui/button.ui.tsx
//	src/lib/ui/dropdown-menu.ui.tsx
//
// The contents are intentionally tiny — we test the classification +
// fanout logic, not the real component bodies. Each file carries just
// enough `from '...'` lines to exercise the relative+alias paths the
// real components use.
func fakeFS() fstest.MapFS {
	return fstest.MapFS{
		"src/styles.css":                 &fstest.MapFile{Data: []byte("/* tokens */")},
		"src/lib/ui/cn.ts":               &fstest.MapFile{Data: []byte(`export const cn = (...a:string[]) => a.join(' ')`)},
		"src/lib/ui/icons.tsx":           &fstest.MapFile{Data: []byte(`export const Foo = () => null`)},
		"src/lib/ui/_primitives/portal.tsx": &fstest.MapFile{Data: []byte(
			"import { useEffect } from 'preact/hooks'\nexport const Portal = () => null\n")},
		"src/lib/ui/_primitives/popper.tsx": &fstest.MapFile{Data: []byte(
			"import { computePosition } from '@floating-ui/dom'\nexport const Popper = () => null\n")},
		"src/lib/ui/button.ui.tsx": &fstest.MapFile{Data: []byte(strings.Join([]string{
			"import { cva } from 'class-variance-authority'",
			"import { cn } from './cn'",
			"export const Button = () => null",
			"",
		}, "\n"))},
		"src/lib/ui/dropdown-menu.ui.tsx": &fstest.MapFile{Data: []byte(strings.Join([]string{
			"import { Portal } from './_primitives/portal'",
			"import { cn } from './cn'",
			"import { Foo } from './icons'",
			"import { Button } from './button.ui'",
			"export const Menu = () => null",
			"",
		}, "\n"))},
	}
}

func TestScan_FindsComponentsAndPrimitives(t *testing.T) {
	withFakeFS(t)
	m := Snapshot()
	if len(m.Components) != 2 {
		t.Fatalf("want 2 components, got %d", len(m.Components))
	}
	if len(m.Primitives) != 2 {
		t.Fatalf("want 2 primitives, got %d", len(m.Primitives))
	}
	// KitBase: cn.ts + icons.tsx (NOT button.ui.tsx — those are
	// components, NOT styles.css — that lives at src/ root).
	if len(m.KitBase) != 2 {
		t.Fatalf("want 2 KitBase files, got %d (%v)", len(m.KitBase), m.KitBase)
	}
}

func TestClassify_LocalSiblingViaRelativeImport(t *testing.T) {
	withFakeFS(t)
	dm, ok := LookupComponent("dropdown-menu")
	if !ok {
		t.Fatal("dropdown-menu missing from registry")
	}
	if got, want := dm.Local, []string{"button"}; !equalSlice(got, want) {
		t.Fatalf("dropdown-menu.Local = %v, want %v", got, want)
	}
	if got, want := dm.Primitives, []string{"portal"}; !equalSlice(got, want) {
		t.Fatalf("dropdown-menu.Primitives = %v, want %v", got, want)
	}
}

func TestClassify_KitBaseImportsAreIgnored(t *testing.T) {
	withFakeFS(t)
	// dropdown-menu imports `./cn` and `./icons` — both kit-base
	// files. Neither should appear in dropdown-menu.Local.
	dm, _ := LookupComponent("dropdown-menu")
	for _, l := range dm.Local {
		switch l {
		case "cn", "icons", "theme", "index":
			t.Fatalf("kit-base file %q leaked into Local list", l)
		}
	}
}

func TestPeers_IncludesPrimitiveDeps(t *testing.T) {
	withFakeFS(t)
	m := Snapshot()
	wantPeer := "@floating-ui/dom"
	found := false
	for _, p := range m.Peers {
		if p == wantPeer {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("peers should include %q (from popper.tsx); got %v", wantPeer, m.Peers)
	}
}

func TestPeers_IncludesCnContributors(t *testing.T) {
	withFakeFS(t)
	m := Snapshot()
	// These three feed cn.ts + styles.css and are added unconditionally.
	want := map[string]struct{}{"clsx": {}, "tailwind-merge": {}, "tw-animate-css": {}}
	for _, p := range m.Peers {
		delete(want, p)
	}
	if len(want) != 0 {
		t.Fatalf("missing seed peers: %v", want)
	}
}

func TestHandler_Manifest_ReturnsFullShape(t *testing.T) {
	withFakeFS(t)
	r := chi.NewRouter()
	Mount(r)

	req := httptest.NewRequest("GET", "/api/_ui/manifest", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got Manifest
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Components) != 2 {
		t.Fatalf("want 2 components in manifest, got %d", len(got.Components))
	}
	if got.Cn == "" || got.Styles == "" {
		t.Fatal("manifest missing Cn / Styles top-level shortcuts")
	}
}

func TestHandler_ComponentSource_ReturnsRawTSX(t *testing.T) {
	withFakeFS(t)
	r := chi.NewRouter()
	Mount(r)

	req := httptest.NewRequest("GET", "/api/_ui/components/button/source", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "export const Button") {
		t.Fatalf("expected button source in response, got %q", string(body))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("want text/plain, got %q", ct)
	}
}

func TestHandler_UnknownComponent_404(t *testing.T) {
	withFakeFS(t)
	r := chi.NewRouter()
	Mount(r)
	req := httptest.NewRequest("GET", "/api/_ui/components/nonexistent", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestHandler_Peers_TextResponse(t *testing.T) {
	withFakeFS(t)
	r := chi.NewRouter()
	Mount(r)
	req := httptest.NewRequest("GET", "/api/_ui/peers", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.HasPrefix(string(body), "npm install ") {
		t.Fatalf("expected `npm install ...`, got %q", string(body))
	}
}

func TestHandler_Peers_JSONResponse(t *testing.T) {
	withFakeFS(t)
	r := chi.NewRouter()
	Mount(r)
	req := httptest.NewRequest("GET", "/api/_ui/peers", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got []string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least the seed peers")
	}
}

func TestScan_NilFS_NoPanic(t *testing.T) {
	// SetFS(nil) + Snapshot() must not panic — the dev/test path
	// without an embed FS should yield an empty registry, not a
	// segfault. Use a fresh once each test.
	regOnce = sync.Once{}
	regVal = nil
	SetFS(nil)
	m := Snapshot()
	if len(m.Components) != 0 {
		t.Fatalf("want 0 components for nil FS, got %d", len(m.Components))
	}
}

// withFakeFS installs the fake FS and resets the registry so the
// next Snapshot() call rebuilds against it. t.Cleanup ensures the
// global is reset before the next test runs.
func withFakeFS(t *testing.T) {
	t.Helper()
	regOnce = sync.Once{}
	regVal = nil
	SetFS(fakeFS())
	t.Cleanup(func() {
		regOnce = sync.Once{}
		regVal = nil
		regFS = nil
	})
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

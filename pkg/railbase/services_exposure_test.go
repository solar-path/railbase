// Regression tests for FEEDBACK Class A — `app.Mailer()`,
// `app.Stripe()`, `app.Jobs()`, `app.Realtime()`, `app.Settings()`,
// `app.Audit()` accessors. The shopper feedback called out 7
// services that were constructed in Run() but never published —
// embedders couldn't call them from custom routes / hooks without
// rebuilding the same service from raw SQL (mailer) or hand-rolled
// time.Ticker loops (jobs/cron).
//
// We can't easily test the publish-during-Run path without
// embed_pg + a full server lifecycle. Instead this file asserts
// the two cheap-but-important invariants:
//
//  1. PRE-Run: getter returns nil (so an embedder constructing an
//     App for unit-test purposes can detect "not ready" without a
//     panic).
//  2. The atomic.Pointer field round-trips correctly — Store(x)
//     followed by Load() returns x. This proves the publish would
//     work if Run() reached the publish point.
//
// Full publish-during-Run is covered by the embed_pg-tagged e2e
// tests under pkg/railbase/principal_e2e_test.go's runtime path.
package railbase

import (
	"testing"

	"github.com/railbase/railbase/internal/audit"
	"github.com/railbase/railbase/internal/jobs"
	"github.com/railbase/railbase/internal/mailer"
	"github.com/railbase/railbase/internal/realtime"
	"github.com/railbase/railbase/internal/settings"
	"github.com/railbase/railbase/internal/stripe"
)

func newAppForTest(t *testing.T) *App {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DSN = "postgres://test:test@localhost:5432/test?sslmode=disable"
	cfg.DataDir = t.TempDir()
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

// TestServicesAccessors_NilPreRun — getters must return nil before
// Run() has been called. An embedder spinning up an App for unit
// tests (without calling Run) can nil-check the getter and skip
// the test path that depends on the service.
func TestServicesAccessors_NilPreRun(t *testing.T) {
	app := newAppForTest(t)
	if app.Mailer() != nil {
		t.Error("Mailer() should be nil pre-Run")
	}
	if app.Stripe() != nil {
		t.Error("Stripe() should be nil pre-Run")
	}
	if app.Jobs() != nil {
		t.Error("Jobs() should be nil pre-Run")
	}
	if app.Realtime() != nil {
		t.Error("Realtime() should be nil pre-Run")
	}
	if app.Settings() != nil {
		t.Error("Settings() should be nil pre-Run")
	}
	if app.Audit() != nil {
		t.Error("Audit() should be nil pre-Run")
	}
}

// TestServicesAccessors_RoundTrip — the atomic.Pointer fields publish
// correctly. Simulates the moment in Run() where each service has
// been constructed and stored, and the getter is called.
func TestServicesAccessors_RoundTrip(t *testing.T) {
	app := newAppForTest(t)

	mlr := mailer.New(mailer.Options{}) // console driver — no SMTP needed
	app.mailer.Store(mlr)
	if got := app.Mailer(); got != mlr {
		t.Errorf("Mailer() round-trip: got %p, want %p", got, mlr)
	}

	stp := &stripe.Service{}
	app.stripe.Store(stp)
	if got := app.Stripe(); got != stp {
		t.Errorf("Stripe() round-trip: got %p, want %p", got, stp)
	}

	jbs := jobs.NewRegistry(nil)
	app.jobs.Store(jbs)
	if got := app.Jobs(); got != jbs {
		t.Errorf("Jobs() round-trip: got %p, want %p", got, jbs)
	}

	rt := realtime.NewBroker(nil, nil)
	app.realtime.Store(rt)
	if got := app.Realtime(); got != rt {
		t.Errorf("Realtime() round-trip: got %p, want %p", got, rt)
	}

	st := settings.New(settings.Options{})
	app.settings.Store(st)
	if got := app.Settings(); got != st {
		t.Errorf("Settings() round-trip: got %p, want %p", got, st)
	}

	aud := audit.NewWriter(nil)
	app.audit.Store(aud)
	if got := app.Audit(); got != aud {
		t.Errorf("Audit() round-trip: got %p, want %p", got, aud)
	}
}

// TestServicesAccessors_RaceFreeReads — multiple goroutines reading
// the getter concurrently with a publish must not data-race. Run
// with `go test -race ./pkg/railbase/` to catch a regression where
// someone changes atomic.Pointer to a plain field.
func TestServicesAccessors_RaceFreeReads(t *testing.T) {
	app := newAppForTest(t)
	mlr := mailer.New(mailer.Options{})

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			_ = app.Mailer() // read
		}
		close(done)
	}()
	app.mailer.Store(mlr) // publish concurrent with reads
	<-done

	if app.Mailer() != mlr {
		t.Error("final read after publish should see the stored pointer")
	}
}

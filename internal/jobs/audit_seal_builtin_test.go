package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// fakeAuditSealer captures SealUnsealed invocations + lets a test
// inject an error to assert error propagation. Mirrors fakeMailer /
// fakeBackupRunner in the sibling builtin tests.
type fakeAuditSealer struct {
	calls       int
	returnCount int
	err         error
}

func (f *fakeAuditSealer) SealUnsealed(_ context.Context) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	return f.returnCount, nil
}

// TestRegisterAuditSealBuiltins_NilSealerNoop verifies the
// registration is skipped when sealer is nil. Production wiring:
// when operators haven't run `railbase audit seal-keygen` (and the
// app is in production mode so auto-create is refused), the kind
// simply isn't registered. Any enqueued job dies as "unknown kind"
// — better than an NPE.
func TestRegisterAuditSealBuiltins_NilSealerNoop(t *testing.T) {
	reg := NewRegistry(newSilentLog())
	RegisterAuditSealBuiltins(reg, nil, newSilentLog())
	if h := reg.Lookup("audit_seal"); h != nil {
		t.Fatalf("expected audit_seal NOT registered when sealer is nil")
	}
}

// TestAuditSeal_HappyPath — registers the builtin, runs the handler
// once, asserts SealUnsealed was invoked exactly once. The handler
// is fire-and-forget by design: the job payload is unused (cron
// triggers it with empty payload).
func TestAuditSeal_HappyPath(t *testing.T) {
	s := &fakeAuditSealer{returnCount: 42}
	reg := NewRegistry(newSilentLog())
	RegisterAuditSealBuiltins(reg, s, newSilentLog())

	h := reg.Lookup("audit_seal")
	if h == nil {
		t.Fatalf("audit_seal not registered")
	}
	job := &Job{ID: uuid.New(), Kind: "audit_seal"}
	if err := h(context.Background(), job); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if s.calls != 1 {
		t.Errorf("SealUnsealed calls = %d, want 1", s.calls)
	}
}

// TestAuditSeal_Error_Propagates — errors from the sealer surface
// through the handler so the queue's retry machinery sees them.
// Mirrors the equivalent test for scheduled_backup / send_email_async.
func TestAuditSeal_Error_Propagates(t *testing.T) {
	sentinel := errors.New("db unreachable")
	s := &fakeAuditSealer{err: sentinel}
	reg := NewRegistry(newSilentLog())
	RegisterAuditSealBuiltins(reg, s, newSilentLog())

	h := reg.Lookup("audit_seal")
	job := &Job{Kind: "audit_seal"}
	err := h(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain doesn't include sentinel: %v", err)
	}
}

// TestAuditSeal_AppearsInDefaultSchedules — pin the cron-row
// registration so future refactors don't drop the schedule by
// accident. Operators boot fresh systems and the schedule must
// be upserted at first-boot.
func TestAuditSeal_AppearsInDefaultSchedules(t *testing.T) {
	var found *DefaultSchedule
	for i := range DefaultSchedules() {
		s := DefaultSchedules()[i]
		if s.Kind == "audit_seal" {
			found = &s
			break
		}
	}
	if found == nil {
		t.Fatal("audit_seal not in DefaultSchedules()")
	}
	// 05:00 daily per plan §3.7.5.3 (after cleanup-* which top out at 04:45).
	if found.Expression != "0 5 * * *" {
		t.Errorf("audit_seal expression = %q, want %q", found.Expression, "0 5 * * *")
	}
	if found.Name != "audit_seal" {
		t.Errorf("audit_seal name = %q, want audit_seal", found.Name)
	}
}

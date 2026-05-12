package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeBackupRunner captures the last Create call. createFile (when
// non-empty) is touched at dest so the handler's stat-for-size path
// has something real to look at. err short-circuits Create for the
// error-propagation test.
type fakeBackupRunner struct {
	calls      int
	gotOutDir  string
	createFile string
	createBody []byte
	err        error
}

func (f *fakeBackupRunner) Create(_ context.Context, outDir string) (string, error) {
	f.calls++
	f.gotOutDir = outDir
	if f.err != nil {
		return "", f.err
	}
	name := f.createFile
	if name == "" {
		name = "backup-19700101-000000.tar.gz"
	}
	if err := os.WriteFile(filepath.Join(outDir, name), f.createBody, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// TestRegisterBackupBuiltins_NilRunnerNoop — nil runner = kind not
// registered, mirroring RegisterMailerBuiltins. Operators without a
// backup destination get "unknown kind" at dispatch, not an NPE.
func TestRegisterBackupBuiltins_NilRunnerNoop(t *testing.T) {
	reg := NewRegistry(newSilentLog())
	RegisterBackupBuiltins(reg, nil, "/tmp", newSilentLog())
	if h := reg.Lookup("scheduled_backup"); h != nil {
		t.Fatalf("expected scheduled_backup NOT registered when runner is nil")
	}
}

// TestScheduledBackup_HappyPath — payload parses, runner.Create
// invoked with the payload's out_dir, filename logged.
func TestScheduledBackup_HappyPath(t *testing.T) {
	dir := t.TempDir()
	r := &fakeBackupRunner{createFile: "backup-20260512-020000.tar.gz", createBody: []byte("hello")}
	reg := NewRegistry(newSilentLog())
	RegisterBackupBuiltins(reg, r, "/should-not-be-used", newSilentLog())

	h := reg.Lookup("scheduled_backup")
	if h == nil {
		t.Fatalf("scheduled_backup not registered")
	}
	job := &Job{
		ID:      uuid.New(),
		Kind:    "scheduled_backup",
		Payload: []byte(`{"out_dir":"` + dir + `"}`),
	}
	if err := h(context.Background(), job); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("expected 1 Create call, got %d", r.calls)
	}
	if r.gotOutDir != dir {
		t.Errorf("outDir = %q, want %q", r.gotOutDir, dir)
	}
}

// TestScheduledBackup_DefaultOutDir — empty payload falls back to the
// constructor-supplied default destination.
func TestScheduledBackup_DefaultOutDir(t *testing.T) {
	dir := t.TempDir()
	r := &fakeBackupRunner{}
	reg := NewRegistry(newSilentLog())
	RegisterBackupBuiltins(reg, r, dir, newSilentLog())

	h := reg.Lookup("scheduled_backup")
	cases := [][]byte{
		nil,
		[]byte(``),
		[]byte(`{}`),
	}
	for i, p := range cases {
		r.calls = 0
		r.gotOutDir = ""
		job := &Job{Kind: "scheduled_backup", Payload: p}
		if err := h(context.Background(), job); err != nil {
			t.Fatalf("case %d: handler returned error: %v", i, err)
		}
		if r.gotOutDir != dir {
			t.Errorf("case %d: outDir = %q, want %q (default)", i, r.gotOutDir, dir)
		}
	}
}

// TestScheduledBackup_Retention_DeletesOldFiles seeds three archives
// with mtimes of 1d / 10d / 30d ago and asserts the 10d + 30d are
// removed once retention_days=7. The runner itself is a no-op for
// this test — we're exercising sweepOldBackups, not the dump logic.
func TestScheduledBackup_Retention_DeletesOldFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	seeds := []struct {
		name string
		age  time.Duration
	}{
		{"backup-fresh.tar.gz", 1 * 24 * time.Hour},
		{"backup-mid.tar.gz", 10 * 24 * time.Hour},
		{"backup-ancient.tar.gz", 30 * 24 * time.Hour},
	}
	for _, s := range seeds {
		path := filepath.Join(dir, s.name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
		mtime := now.Add(-s.age)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", s.name, err)
		}
	}
	// A sibling file that does NOT match the pattern — must survive
	// the sweep even when aged.
	unrelated := filepath.Join(dir, "operator-notes.txt")
	if err := os.WriteFile(unrelated, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("seed unrelated: %v", err)
	}
	oldTime := now.Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(unrelated, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes unrelated: %v", err)
	}

	r := &fakeBackupRunner{createFile: "backup-new.tar.gz"}
	reg := NewRegistry(newSilentLog())
	RegisterBackupBuiltins(reg, r, dir, newSilentLog())
	h := reg.Lookup("scheduled_backup")

	job := &Job{
		Kind:    "scheduled_backup",
		Payload: []byte(`{"retention_days":7}`),
	}
	if err := h(context.Background(), job); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	must := func(name string, shouldExist bool) {
		_, err := os.Stat(filepath.Join(dir, name))
		exists := err == nil
		if exists != shouldExist {
			t.Errorf("%s exists=%v, want=%v", name, exists, shouldExist)
		}
	}
	must("backup-fresh.tar.gz", true)
	must("backup-mid.tar.gz", false)
	must("backup-ancient.tar.gz", false)
	must("operator-notes.txt", true)
	// Just-written archive must survive (mtime is "now").
	must("backup-new.tar.gz", true)
}

// TestScheduledBackup_RunnerError — Create errors propagate so the
// queue's retry machinery can see them.
func TestScheduledBackup_RunnerError(t *testing.T) {
	sentinel := errors.New("pool acquire failed")
	r := &fakeBackupRunner{err: sentinel}
	reg := NewRegistry(newSilentLog())
	RegisterBackupBuiltins(reg, r, t.TempDir(), newSilentLog())
	h := reg.Lookup("scheduled_backup")

	job := &Job{Kind: "scheduled_backup", Payload: []byte(`{}`)}
	err := h(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain doesn't include sentinel: %v", err)
	}
}

// TestScheduledBackup_BadPayload_Permanent asserts a malformed payload
// wraps ErrPermanent (v1.7.32 — same treatment as send_email_async).
// Retrying a syntactically-broken JSON blob accomplishes nothing.
func TestScheduledBackup_BadPayload_Permanent(t *testing.T) {
	r := &fakeBackupRunner{}
	reg := NewRegistry(newSilentLog())
	RegisterBackupBuiltins(reg, r, t.TempDir(), newSilentLog())
	h := reg.Lookup("scheduled_backup")

	job := &Job{Kind: "scheduled_backup", Payload: []byte(`not json at all`)}
	err := h(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error for malformed payload")
	}
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("error should wrap ErrPermanent: %v", err)
	}
	if r.calls != 0 {
		t.Errorf("runner should NOT be called when payload is malformed: got %d calls", r.calls)
	}
}

// TestScheduledBackup_NoOutDir_Permanent asserts the "no out_dir
// configured" path also wraps ErrPermanent — operator misconfiguration
// retrying can't fix.
func TestScheduledBackup_NoOutDir_Permanent(t *testing.T) {
	r := &fakeBackupRunner{}
	reg := NewRegistry(newSilentLog())
	RegisterBackupBuiltins(reg, r, "" /* no default outDir */, newSilentLog())
	h := reg.Lookup("scheduled_backup")

	job := &Job{Kind: "scheduled_backup", Payload: []byte(`{}`)} // empty payload
	err := h(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error for missing out_dir")
	}
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("error should wrap ErrPermanent: %v", err)
	}
}

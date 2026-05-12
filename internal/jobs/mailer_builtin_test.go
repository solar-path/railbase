package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
)

// fakeMailer captures the last SendTemplate call so the test can
// assert payload parsing + recipient translation succeeded.
type fakeMailer struct {
	gotName  string
	gotTo    []MailerAddress
	gotData  map[string]any
	gotCalls int
	err      error
}

func (f *fakeMailer) SendTemplate(_ context.Context, name string, to []MailerAddress, data map[string]any) error {
	f.gotName = name
	f.gotTo = append([]MailerAddress(nil), to...)
	f.gotData = data
	f.gotCalls++
	return f.err
}

func newSilentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRegisterMailerBuiltins_NilMailerNoop verifies the registration
// is skipped when mailer is nil. Production wiring: when operators
// haven't configured a mailer, the kind simply isn't registered, and
// any enqueued job dies as "unknown kind" — better than an NPE.
func TestRegisterMailerBuiltins_NilMailerNoop(t *testing.T) {
	reg := NewRegistry(newSilentLog())
	RegisterMailerBuiltins(reg, nil, newSilentLog())
	if h := reg.Lookup("send_email_async"); h != nil {
		t.Fatalf("expected send_email_async NOT registered when mailer is nil")
	}
}

// TestSendEmailAsync_HappyPath asserts the canonical wire payload
// shape parses + translates through to the mailer call cleanly.
func TestSendEmailAsync_HappyPath(t *testing.T) {
	m := &fakeMailer{}
	reg := NewRegistry(newSilentLog())
	RegisterMailerBuiltins(reg, m, newSilentLog())

	h := reg.Lookup("send_email_async")
	if h == nil {
		t.Fatalf("send_email_async not registered")
	}

	job := &Job{
		ID:      uuid.New(),
		Kind:    "send_email_async",
		Payload: []byte(`{"template":"welcome","to":[{"email":"a@b.co","name":"Alice"},{"email":"c@d.co"}],"data":{"name":"Alice","plan":"pro"}}`),
	}
	if err := h(context.Background(), job); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if m.gotCalls != 1 {
		t.Fatalf("expected 1 send, got %d", m.gotCalls)
	}
	if m.gotName != "welcome" {
		t.Errorf("template = %q, want welcome", m.gotName)
	}
	if len(m.gotTo) != 2 {
		t.Fatalf("recipients = %d, want 2", len(m.gotTo))
	}
	if m.gotTo[0].Email != "a@b.co" || m.gotTo[0].Name != "Alice" {
		t.Errorf("recipient[0] = %+v", m.gotTo[0])
	}
	if m.gotTo[1].Email != "c@d.co" || m.gotTo[1].Name != "" {
		t.Errorf("recipient[1] = %+v", m.gotTo[1])
	}
	if v, _ := m.gotData["name"].(string); v != "Alice" {
		t.Errorf("data.name = %v", m.gotData["name"])
	}
}

// TestSendEmailAsync_MissingTemplate asserts a payload missing the
// required `template` field fails synchronously.
func TestSendEmailAsync_MissingTemplate(t *testing.T) {
	m := &fakeMailer{}
	reg := NewRegistry(newSilentLog())
	RegisterMailerBuiltins(reg, m, newSilentLog())

	h := reg.Lookup("send_email_async")
	job := &Job{
		Kind:    "send_email_async",
		Payload: []byte(`{"to":[{"email":"x@y.co"}]}`),
	}
	err := h(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error for missing template")
	}
	if m.gotCalls != 0 {
		t.Fatalf("mailer should NOT be called when validation fails (got %d calls)", m.gotCalls)
	}
}

// TestSendEmailAsync_MissingRecipients asserts an empty `to` list
// fails synchronously rather than calling the mailer with [].
func TestSendEmailAsync_MissingRecipients(t *testing.T) {
	m := &fakeMailer{}
	reg := NewRegistry(newSilentLog())
	RegisterMailerBuiltins(reg, m, newSilentLog())

	h := reg.Lookup("send_email_async")
	job := &Job{
		Kind:    "send_email_async",
		Payload: []byte(`{"template":"welcome","to":[]}`),
	}
	if err := h(context.Background(), job); err == nil {
		t.Fatalf("expected error for empty recipients")
	}
	if m.gotCalls != 0 {
		t.Fatalf("mailer should NOT be called when recipients empty")
	}
}

// TestSendEmailAsync_BadJSON asserts malformed payload returns an
// error that wraps ErrPermanent (v1.7.31 sentinel) so the retry
// engine treats it as terminal failure instead of looping the doomed
// payload through MaxAttempts.
func TestSendEmailAsync_BadJSON(t *testing.T) {
	m := &fakeMailer{}
	reg := NewRegistry(newSilentLog())
	RegisterMailerBuiltins(reg, m, newSilentLog())

	h := reg.Lookup("send_email_async")
	job := &Job{
		Kind:    "send_email_async",
		Payload: []byte(`not json at all`),
	}
	err := h(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error for malformed payload")
	}
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("error should wrap ErrPermanent so runner permanent-fails: got %v", err)
	}
}

// TestSendEmailAsync_MissingTemplate_Permanent asserts the missing-
// template path also wraps ErrPermanent (no point retrying a fix-the-
// payload bug).
func TestSendEmailAsync_MissingTemplate_Permanent(t *testing.T) {
	m := &fakeMailer{}
	reg := NewRegistry(newSilentLog())
	RegisterMailerBuiltins(reg, m, newSilentLog())

	h := reg.Lookup("send_email_async")
	job := &Job{
		Kind:    "send_email_async",
		Payload: []byte(`{"to":[{"email":"x@y.co"}]}`),
	}
	err := h(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error for missing template")
	}
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("error should wrap ErrPermanent: got %v", err)
	}
}

// TestSendEmailAsync_MailerError verifies that mailer-layer errors
// propagate (so the queue's retry engine can see them).
func TestSendEmailAsync_MailerError(t *testing.T) {
	sentinel := errors.New("smtp boom")
	m := &fakeMailer{err: sentinel}
	reg := NewRegistry(newSilentLog())
	RegisterMailerBuiltins(reg, m, newSilentLog())

	h := reg.Lookup("send_email_async")
	job := &Job{
		Kind:    "send_email_async",
		Payload: []byte(`{"template":"welcome","to":[{"email":"a@b.co"}]}`),
	}
	err := h(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain doesn't include sentinel: %v", err)
	}
}

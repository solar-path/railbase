package railbase

import (
	"context"

	"github.com/railbase/railbase/internal/mailer"
	"github.com/railbase/railbase/internal/notifications"
)

// notificationsMailerAdapter satisfies notifications.Mailer by
// translating the single-recipient signature the notifications
// package wants into mailer.SendTemplate's []Address shape. v1.7.34
// — quiet-hours + digest flush uses this to send rendered digest
// emails through the same code path operator transactional templates
// use.
//
// Sibling to mailerSendAdapter in mailer_wiring.go: the jobs package
// (RegisterMailerBuiltins) and the notifications package
// (notifications.Service.Mailer) each have their own minimal Mailer
// interface so neither has to import internal/mailer. This adapter
// bridges the gap at the wiring layer.
type notificationsMailerAdapter struct{ m *mailer.Mailer }

func (a notificationsMailerAdapter) SendTemplate(ctx context.Context, to string, template string, data map[string]any) error {
	return a.m.SendTemplate(ctx, template, []mailer.Address{{Email: to}}, data)
}

// notificationFlusherAdapter satisfies jobs.NotificationFlusher by
// delegating to notifications.Service.FlushDeferred. The wrapper is
// trivial (one-method passthrough) but keeps the interface seam at
// the right place — internal/jobs can't import internal/notifications
// without creating a cycle through the wider wiring graph, so the
// adapter lives here in the composition root.
type notificationFlusherAdapter struct{ s *notifications.Service }

func (a notificationFlusherAdapter) FlushDeferred(ctx context.Context) (int, error) {
	return a.s.FlushDeferred(ctx)
}

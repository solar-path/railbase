package ts

import "strings"

// EmitNotifications renders notifications.ts: typed wrappers for the
// public notification surface (internal/api/notifications). Like
// stripe.ts this emitter is schema-independent — the endpoints are
// fixed, not derived from CollectionSpec.
//
// Surface (all require an authenticated principal):
//
//   - GET    /api/notifications/?unread=&limit=   list
//   - GET    /api/notifications/unread-count       unread tally
//   - POST   /api/notifications/{id}/read          mark one read
//   - POST   /api/notifications/mark-all-read      mark all read
//   - DELETE /api/notifications/{id}               delete one
//   - GET    /api/notifications/preferences        per-kind channel prefs
//   - PATCH  /api/notifications/preferences        set one preference
func EmitNotifications() string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(`// notifications.ts — typed wrappers for the notification endpoints.

import type { HTTPClient } from "./index.js";

export type NotificationChannel = "inapp" | "email" | "push";
export type NotificationPriority = "low" | "normal" | "high" | "urgent";

/** One in-app notification row. */
export interface Notification {
  id: string;
  user_id: string;
  tenant_id?: string;
  kind: string;
  title: string;
  body?: string;
  data?: Record<string, unknown>;
  priority: NotificationPriority;
  /** RFC3339 timestamp, or absent when unread. */
  read_at?: string;
  expires_at?: string;
  created_at: string;
}

/** A per-kind, per-channel delivery preference. */
export interface NotificationPreference {
  user_id: string;
  kind: string;
  channel: NotificationChannel;
  enabled: boolean;
  updated_at: string;
}

/** Notification wrappers for the currently-authenticated principal.
 *  Every call is scoped to the token's user — there is no cross-user
 *  surface here (that lives behind the admin API). */
export function notificationsClient(http: HTTPClient) {
  return {
    /** GET /api/notifications/ — newest first. */
    list(opts: { unread?: boolean; limit?: number } = {}): Promise<Notification[]> {
      const q = new URLSearchParams();
      if (opts.unread) q.set("unread", "true");
      if (opts.limit != null) q.set("limit", String(opts.limit));
      const qs = q.toString();
      return http
        .request<{ items: Notification[] }>("GET", "/api/notifications/" + (qs ? "?" + qs : ""))
        .then((r) => r.items ?? []);
    },

    /** GET /api/notifications/unread-count — number of unread rows. */
    unreadCount(): Promise<number> {
      return http
        .request<{ unread: number }>("GET", "/api/notifications/unread-count")
        .then((r) => r.unread);
    },

    /** POST /api/notifications/{id}/read — mark one notification read. */
    markRead(id: string): Promise<void> {
      return http.request("POST", "/api/notifications/" + encodeURIComponent(id) + "/read");
    },

    /** POST /api/notifications/mark-all-read — returns the count marked. */
    markAllRead(): Promise<number> {
      return http
        .request<{ marked: number }>("POST", "/api/notifications/mark-all-read")
        .then((r) => r.marked);
    },

    /** DELETE /api/notifications/{id} — delete one notification. */
    delete(id: string): Promise<void> {
      return http.request("DELETE", "/api/notifications/" + encodeURIComponent(id));
    },

    /** GET /api/notifications/preferences — every per-kind channel pref. */
    preferences(): Promise<NotificationPreference[]> {
      return http
        .request<{ items: NotificationPreference[] }>("GET", "/api/notifications/preferences")
        .then((r) => r.items ?? []);
    },

    /** PATCH /api/notifications/preferences — set one per-kind channel pref. */
    setPreference(input: {
      kind: string;
      channel: NotificationChannel;
      enabled: boolean;
    }): Promise<void> {
      return http.request("PATCH", "/api/notifications/preferences", { body: input });
    },
  };
}
`)
	return b.String()
}

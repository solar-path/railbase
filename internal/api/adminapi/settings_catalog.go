package adminapi

// v1.x — settings catalog.
//
// The bare key/value editor at /settings was a discoverability black
// hole: operators had to know magic strings like "security.cors.
// allowed_origins" by heart, and nothing in the UI hinted at the
// type, default, or which subsystem reads it. This catalog inverts
// the model — the backend declares every known setting up-front, the
// SPA renders typed form controls grouped by feature, and the raw
// key/value editor stays available as an "Advanced" fallback for
// keys outside the catalog.
//
// Adding a new setting:
//
//   1. Declare it in `settingsCatalog` below — pick a group, a type,
//      a sensible default, and write a one-line description. The
//      description shows up under the form field; don't ship terse
//      placeholders.
//
//   2. Wire it on the consumer side via `readSetting(...)` /
//      Manager.Get*; the catalog doesn't enforce anything beyond the
//      UI shape, so a typo in the consumer's key string is still a
//      bug only tests will catch.
//
// What the catalog deliberately DOESN'T include:
//
//   - `mailer.*` — owned by the Mailer settings screen.
//   - `oauth.*` / `webauthn.*` / `auth.*` — owned by Auth methods.
//   - `security.cors.*` — actually we DO include these because
//     there's no dedicated CORS screen and they're security-critical.
//
// Settings the catalog includes but no current code reads: NONE. If
// you find an entry here without a consumer, delete it — drift
// between "what the UI offers" and "what the backend respects" is
// the worst class of operator footgun.

import (
	"context"
	"encoding/json"
	"net/http"
)

// SettingReload tells the SPA whether changing this setting takes
// effect immediately or requires a server restart. The catalog is
// honest about this — operators were getting "Save"-then-nothing
// before; now restart-only settings render a visible badge so the
// expectation matches reality.
//
// Stable wire values:
//
//   - "live"    — at least one consumer subscribes to settings.TopicChanged
//                 and re-reads on the fly. The change applies within
//                 milliseconds of Save.
//   - "restart" — every known consumer reads the value once at boot
//                 and holds it in a closure / struct field. Save persists
//                 to `_settings` (so the next boot picks it up) but no
//                 currently-running code path observes the change.
//
// When a setting has MIXED reload behaviour (some consumers live,
// some boot-only — `site.name` is the canonical example: the admin
// UI re-renders live but the in-memory mailer service holds the old
// value) we mark it "restart" and call out the partial liveness in
// the Description. Marking it "live" would mislead operators into
// thinking emails would carry the new site name immediately.
type SettingReload string

const (
	SettingReloadLive    SettingReload = "live"
	SettingReloadRestart SettingReload = "restart"
)

// SettingType is the shape of the value the form control returns.
// Stable wire values — the SPA switches on these literal strings.
type SettingType string

const (
	// SettingTypeString is a free-form single line.
	SettingTypeString SettingType = "string"
	// SettingTypeBool renders as a Switch; JSON is true/false.
	SettingTypeBool SettingType = "bool"
	// SettingTypeInt renders as <input type=number>; JSON is a
	// number (int64 range — Go side stores via GetInt).
	SettingTypeInt SettingType = "int"
	// SettingTypeCSV renders as a string input with a "comma-
	// separated" hint. Backend stores the raw string (NOT a parsed
	// array) — every consumer already calls splitCSV() so JSONB
	// shape "a,b,c" is the canonical form.
	SettingTypeCSV SettingType = "csv"
	// SettingTypeDuration renders as a string input with placeholder
	// "30s / 5m / 1h". Backend stores the raw string (consumers parse
	// with time.ParseDuration).
	SettingTypeDuration SettingType = "duration"
	// SettingTypeJSON renders as a textarea with monospace font.
	// Catch-all for nested structures.
	SettingTypeJSON SettingType = "json"
)

// SettingDef describes ONE catalog entry. The Default value is what
// the consumer falls back to when the row is absent from _settings
// — it's NOT auto-inserted on first boot, but rendered by the UI so
// the operator can see "the implicit default for this is X."
type SettingDef struct {
	Key         string      `json:"key"`
	Type        SettingType `json:"type"`
	Group       string      `json:"group"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Default     any         `json:"default,omitempty"`
	Placeholder string      `json:"placeholder,omitempty"`
	// Reload signals whether a change takes effect live or needs a
	// server restart. The SPA renders a "restart required" badge for
	// SettingReloadRestart so operators don't expect Save to fix
	// anything until they actually bounce the process.
	Reload SettingReload `json:"reload"`
	// Secret hints the SPA to render the value masked + reveal-on-
	// click. Used for tokens / keys the operator might paste; even
	// here the value stays in plain JSONB on the server.
	Secret bool `json:"secret,omitempty"`
	// EnvVar names the matching RAILBASE_* env-var when present —
	// rendered as a hint so operators know they can also set it that
	// way. Empty when no env-var path exists.
	EnvVar string `json:"env_var,omitempty"`
}

// Group ordering for the SPA. Rendered top-to-bottom in this order.
var settingsGroups = []string{
	"Application",
	"Storage",
	"Network access",
	"CORS",
	"Rate limiting",
	"Anti-bot & logs",
	"Compatibility",
}

// settingsCatalog is the source of truth. Keep entries ALPHABETICAL
// within each group so reviews catch order-skew accidents.
var settingsCatalog = []SettingDef{
	// ───── Application ─────────────────────────────────────────────
	{
		Key:         "site.name",
		Type:        SettingTypeString,
		Group:       "Application",
		Label:       "Site name",
		Description: "Display name used as the admin UI brand (live), in email subject lines, browser titles, and the bootstrap welcome banner. The admin sidebar re-renders immediately; mailer + WebAuthn pick up the new value on the next restart.",
		Default:     "Railbase",
		Reload:      SettingReloadRestart,
		EnvVar:      "RAILBASE_SITE_NAME",
	},
	{
		Key:         "site.url",
		Type:        SettingTypeString,
		Group:       "Application",
		Label:       "Site URL",
		Description: "Public origin of this deployment (e.g. https://app.example.com). Used to build links in mailer templates and OAuth redirects.",
		Placeholder: "https://app.example.com",
		Reload:      SettingReloadRestart,
		EnvVar:      "RAILBASE_SITE_URL",
	},

	// ───── Storage ─────────────────────────────────────────────────
	{
		Key:         "storage.dir",
		Type:        SettingTypeString,
		Group:       "Storage",
		Label:       "Storage directory",
		Description: "Filesystem path for uploaded files. Override only if you need to point at a different volume — the default lives under the data dir.",
		Placeholder: "<data_dir>/files",
		Reload:      SettingReloadRestart,
		EnvVar:      "RAILBASE_STORAGE_DIR",
	},
	{
		Key:         "storage.max_upload_bytes",
		Type:        SettingTypeInt,
		Group:       "Storage",
		Label:       "Max upload size (bytes)",
		Description: "Largest accepted file upload. The HTTP body is hard-capped at this value; bigger requests fail with 413 before hitting disk.",
		Default:     float64(50 << 20), // 50 MiB
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_STORAGE_MAX_UPLOAD_BYTES",
	},
	{
		Key:         "storage.url_ttl",
		Type:        SettingTypeDuration,
		Group:       "Storage",
		Label:       "Signed URL TTL",
		Description: "How long a signed file URL stays valid. Shorter = safer if a URL leaks; too short breaks slow clients.",
		Default:     "5m",
		Placeholder: "5m",
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_STORAGE_URL_TTL",
	},

	// ───── Network access ──────────────────────────────────────────
	{
		Key:         "security.allow_ips",
		Type:        SettingTypeCSV,
		Group:       "Network access",
		Label:       "Allowed IPs (CIDR)",
		Description: "Comma-separated CIDR allowlist. When non-empty, requests from IPs outside the list are refused with 403. Useful for fronted-by-VPN deployments.",
		Placeholder: "10.0.0.0/8, 192.168.1.0/24",
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_ALLOW_IPS",
	},
	{
		Key:         "security.deny_ips",
		Type:        SettingTypeCSV,
		Group:       "Network access",
		Label:       "Denied IPs (CIDR)",
		Description: "Comma-separated CIDR denylist. Always applied AFTER allow-list. Bans a known-bad subnet without disrupting normal traffic.",
		Placeholder: "1.2.3.0/24",
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_DENY_IPS",
	},
	{
		Key:         "security.trusted_proxies",
		Type:        SettingTypeCSV,
		Group:       "Network access",
		Label:       "Trusted proxies (CIDR)",
		Description: "Comma-separated CIDRs whose X-Forwarded-For header is honoured for client-IP extraction. Without this, rate limit + IP filter see the proxy IP, not the real client. Live via runtimeconfig — IPFilter swaps the trusted-proxy list atomically on Save.",
		Placeholder: "10.0.0.0/8",
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_TRUSTED_PROXIES",
	},

	// ───── CORS ────────────────────────────────────────────────────
	{
		Key:         "security.cors.allow_credentials",
		Type:        SettingTypeBool,
		Group:       "CORS",
		Label:       "Allow credentials",
		Description: "Permit cookie-auth across origins. Required for SPAs on a separate domain that use session cookies. Forces Allow-Origin to be exact (no wildcard).",
		Default:     false,
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_CORS_ALLOW_CREDENTIALS",
	},
	{
		Key:         "security.cors.allowed_origins",
		Type:        SettingTypeCSV,
		Group:       "CORS",
		Label:       "Allowed origins",
		Description: "Comma-separated exact origins permitted to call this API from a browser. Empty list = middleware inert (default for same-origin deployments). \"*\" allowed only when Allow credentials is off.",
		Placeholder: "https://app.example.com",
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_CORS_ALLOWED_ORIGINS",
	},

	// ───── Rate limiting ───────────────────────────────────────────
	{
		Key:         "security.rate_limit.per_ip",
		Type:        SettingTypeString,
		Group:       "Rate limiting",
		Label:       "Per-IP rule",
		Description: "Format: \"N/window\" (e.g. \"60/m\", \"1000/h\"). Empty disables the per-IP axis. Token-bucket; bursts up to N then refills.",
		Placeholder: "60/m",
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_RATE_LIMIT_PER_IP",
	},
	{
		Key:         "security.rate_limit.per_user",
		Type:        SettingTypeString,
		Group:       "Rate limiting",
		Label:       "Per-user rule",
		Description: "Format: \"N/window\". Empty disables the axis. Counted against the authenticated user's id — guests fall back to per-IP.",
		Placeholder: "300/m",
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_RATE_LIMIT_PER_USER",
	},
	{
		Key:         "security.rate_limit.per_tenant",
		Type:        SettingTypeString,
		Group:       "Rate limiting",
		Label:       "Per-tenant rule",
		Description: "Format: \"N/window\". Empty disables the axis. Counted against the X-Tenant header.",
		Placeholder: "1000/m",
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_RATE_LIMIT_PER_TENANT",
	},

	// ───── Anti-bot & logs ─────────────────────────────────────────
	{
		Key:         "security.antibot.enabled",
		Type:        SettingTypeBool,
		Group:       "Anti-bot & logs",
		Label:       "Anti-bot middleware",
		Description: "Honeypot form field + UA sanity check on auth / OAuth paths. Adds a small JS-rendered field bots auto-fill; rejects requests that fill it.",
		Default:     true,
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_ANTIBOT_ENABLED",
	},
	{
		Key:         "logs.persist",
		Type:        SettingTypeBool,
		Group:       "Anti-bot & logs",
		Label:       "Persist application logs",
		Description: "Write structured slog records to the `_logs` table for the in-admin log browser. Off = stdout-only. Live via runtimeconfig — flipping this toggles the in-process sink immediately, no restart.",
		Default:     true,
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_LOGS_PERSIST",
	},

	// ───── Compatibility ───────────────────────────────────────────
	{
		Key:         "compat.mode",
		Type:        SettingTypeString,
		Group:       "Compatibility",
		Label:       "Compatibility mode",
		Description: "Wire-shape preset for legacy clients: \"strict\" (default — Railbase native), \"pocketbase\" (PB-compatible JSON envelope). Affects every /api/* response.",
		Default:     "strict",
		Placeholder: "strict",
		Reload:      SettingReloadLive,
		EnvVar:      "RAILBASE_COMPAT_MODE",
	},
}

// catalogedKeys is the set of keys the catalog claims, lookup-fast.
// Built once on package init. The /settings/catalog response uses
// it to mark whether a /settings/list row is "known" or falls under
// the Advanced fallback.
var catalogedKeys = func() map[string]struct{} {
	m := make(map[string]struct{}, len(settingsCatalog))
	for _, d := range settingsCatalog {
		m[d.Key] = struct{}{}
	}
	return m
}()

// SettingsEnvMap returns the catalog's key → env-var mapping. Used
// by pkg/railbase/app.go to wire runtimeconfig with pre-boot env
// fallbacks so `RAILBASE_*` operator overrides keep their documented
// effect when the corresponding setting hasn't been persisted to
// `_settings` yet. Returns a fresh map per call (caller-owned).
func SettingsEnvMap() map[string]string {
	out := make(map[string]string, len(settingsCatalog))
	for _, d := range settingsCatalog {
		if d.EnvVar != "" {
			out[d.Key] = d.EnvVar
		}
	}
	return out
}

// settingsCatalogResponse is what GET /api/_admin/settings/catalog
// returns. The SPA reads `groups` (ordered) and `entries` (grouped
// list of {def, value}) and renders typed controls. `unknown_keys`
// is the set of persisted keys that DON'T appear in the catalog —
// the SPA shows these under "Advanced" so legacy settings still get
// edited via the raw key/value editor.
type settingsCatalogResponse struct {
	Groups      []string                  `json:"groups"`
	Entries     []settingsCatalogEntryDTO `json:"entries"`
	UnknownKeys []string                  `json:"unknown_keys"`
}

// settingsCatalogEntryDTO bundles the spec with the current value
// (nil when unset — UI renders the default). Returned per-key so
// the SPA doesn't need to do a second round-trip for values.
type settingsCatalogEntryDTO struct {
	Def   SettingDef `json:"def"`
	Value any        `json:"value,omitempty"`
	IsSet bool       `json:"is_set"`
}

// settingsCatalogHandler — GET /api/_admin/settings/catalog.
// Read-gated by settings.read (same as the bare list). Returns the
// full catalog plus current values; the SPA renders typed forms
// from this single response.
func (d *Deps) settingsCatalogHandler(w http.ResponseWriter, r *http.Request) {
	if d.Settings == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(settingsCatalogResponse{
			Groups:      settingsGroups,
			Entries:     []settingsCatalogEntryDTO{},
			UnknownKeys: []string{},
		})
		return
	}
	all, err := d.Settings.List(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	entries := make([]settingsCatalogEntryDTO, 0, len(settingsCatalog))
	for _, def := range settingsCatalog {
		v, set := all[def.Key]
		entries = append(entries, settingsCatalogEntryDTO{
			Def:   def,
			Value: v,
			IsSet: set,
		})
	}
	unknown := make([]string, 0)
	for k := range all {
		if _, known := catalogedKeys[k]; known {
			continue
		}
		// Skip groups that own their own screens — surfacing them under
		// "Advanced" would be confusing because there's a dedicated UI
		// for them already. Anything else (custom operator settings,
		// keys from plugins) falls through and gets edited via the raw
		// fallback.
		if hasPrefix(k, "mailer.") || hasPrefix(k, "oauth.") ||
			hasPrefix(k, "webauthn.") || hasPrefix(k, "auth.") ||
			hasPrefix(k, "notifications.") || hasPrefix(k, "stripe.") {
			continue
		}
		unknown = append(unknown, k)
	}
	sortStrings(unknown)
	resp := settingsCatalogResponse{
		Groups:      settingsGroups,
		Entries:     entries,
		UnknownKeys: unknown,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// hasPrefix is a tiny strings.HasPrefix shadow so this file doesn't
// reach for "strings" just for one call.
func hasPrefix(s, p string) bool {
	if len(s) < len(p) {
		return false
	}
	return s[:len(p)] == p
}

// _ silences "settingsGroups unused" if a refactor accidentally
// removes the only reference. The exported response uses it; keep
// the sentinel until the field is removed.
var _ = context.Background

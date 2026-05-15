package runtimeconfig

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLoader is the in-memory Loader stand-in. Tests mutate `vals`
// directly to simulate operator edits, then call cfg.Notify(key) to
// re-pull through the dispatcher — the same shape a real
// settings.TopicChanged subscriber would feed in.
type fakeLoader struct {
	vals map[string]any
}

func (f *fakeLoader) GetString(_ context.Context, k string) (string, bool, error) {
	v, ok := f.vals[k]
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	return s, ok, nil
}
func (f *fakeLoader) GetInt(_ context.Context, k string) (int64, bool, error) {
	v, ok := f.vals[k]
	if !ok {
		return 0, false, nil
	}
	switch n := v.(type) {
	case int:
		return int64(n), true, nil
	case int64:
		return n, true, nil
	case float64:
		return int64(n), true, nil
	}
	return 0, false, nil
}
func (f *fakeLoader) GetBool(_ context.Context, k string) (bool, bool, error) {
	v, ok := f.vals[k]
	if !ok {
		return false, false, nil
	}
	b, ok := v.(bool)
	return b, ok, nil
}

func TestConfig_DefaultsWhenEmpty(t *testing.T) {
	c := New(&fakeLoader{vals: map[string]any{}}, nil)

	if got := c.SiteName(); got != "Railbase" {
		t.Errorf("SiteName empty = %q; want default \"Railbase\"", got)
	}
	if got := c.MaxUploadBytes(); got != defaultStorageMaxUploadBytes {
		t.Errorf("MaxUploadBytes empty = %d; want %d", got, defaultStorageMaxUploadBytes)
	}
	if got := c.StorageURLTTL(); got != defaultStorageURLTTL {
		t.Errorf("StorageURLTTL empty = %s; want %s", got, defaultStorageURLTTL)
	}
	if got := c.CompatMode(); got != "strict" {
		t.Errorf("CompatMode empty = %q; want \"strict\"", got)
	}
	if !c.AntibotEnabled() {
		t.Error("AntibotEnabled empty = false; want true (default)")
	}
	if !c.LogsPersist() {
		t.Error("LogsPersist empty = false; want true (default)")
	}
	if c.AllowedIPs() != nil {
		t.Errorf("AllowedIPs empty = %v; want nil", c.AllowedIPs())
	}
	if c.CORSAllowCredentials() {
		t.Error("CORSAllowCredentials empty = true; want false")
	}
}

func TestConfig_LoadsFromLoader(t *testing.T) {
	f := &fakeLoader{vals: map[string]any{
		"site.name":                        "Sentinel",
		"site.url":                         "https://sentinel.example",
		"storage.max_upload_bytes":         int64(1 << 20),
		"storage.url_ttl":                  "30s",
		"security.allow_ips":               "10.0.0.0/8, 192.168.1.0/24",
		"security.cors.allowed_origins":    "https://a.example, https://b.example",
		"security.cors.allow_credentials":  true,
		"security.rate_limit.per_ip":       "60/m",
		"security.antibot.enabled":         false,
		"logs.persist":                     false,
		"compat.mode":                      "pocketbase",
	}}
	c := New(f, nil)

	if got := c.SiteName(); got != "Sentinel" {
		t.Errorf("SiteName = %q; want \"Sentinel\"", got)
	}
	if got := c.SiteURL(); got != "https://sentinel.example" {
		t.Errorf("SiteURL = %q", got)
	}
	if got := c.MaxUploadBytes(); got != 1<<20 {
		t.Errorf("MaxUploadBytes = %d; want %d", got, 1<<20)
	}
	if got := c.StorageURLTTL(); got != 30*time.Second {
		t.Errorf("StorageURLTTL = %s; want 30s", got)
	}
	if got := c.AllowedIPs(); len(got) != 2 || got[0] != "10.0.0.0/8" || got[1] != "192.168.1.0/24" {
		t.Errorf("AllowedIPs = %v", got)
	}
	if got := c.CORSAllowedOrigins(); len(got) != 2 {
		t.Errorf("CORSAllowedOrigins = %v", got)
	}
	if !c.CORSAllowCredentials() {
		t.Error("CORSAllowCredentials = false; want true")
	}
	if got := c.RateLimitPerIP(); got != "60/m" {
		t.Errorf("RateLimitPerIP = %q", got)
	}
	if c.AntibotEnabled() {
		t.Error("AntibotEnabled = true; want false (was explicitly disabled in fixture)")
	}
	if c.LogsPersist() {
		t.Error("LogsPersist = true; want false (was explicitly disabled in fixture)")
	}
	if got := c.CompatMode(); got != "pocketbase" {
		t.Errorf("CompatMode = %q", got)
	}
}

func TestConfig_NotifyRePullsSingleKey(t *testing.T) {
	f := &fakeLoader{vals: map[string]any{
		"site.name": "First",
	}}
	c := New(f, nil)
	if c.SiteName() != "First" {
		t.Fatalf("seed: SiteName = %q", c.SiteName())
	}

	// Operator edits the value through Manager.Set; the bus subscriber
	// in app.go pumps the key into Notify.
	f.vals["site.name"] = "Second"
	c.Notify(context.Background(), "site.name")

	if got := c.SiteName(); got != "Second" {
		t.Fatalf("after Notify: SiteName = %q; want \"Second\"", got)
	}
}

func TestConfig_OnChangeFiresForRegisteredKeys(t *testing.T) {
	f := &fakeLoader{vals: map[string]any{}}
	c := New(f, nil)

	var calls atomic.Int32
	c.OnChange([]string{"site.name", "site.url"}, func() {
		calls.Add(1)
	})

	// site.name → matches → fires.
	c.Notify(context.Background(), "site.name")
	if got := calls.Load(); got != 1 {
		t.Errorf("after site.name notify: calls = %d; want 1", got)
	}

	// security.cors.allowed_origins → not in registered set → no fire.
	c.Notify(context.Background(), "security.cors.allowed_origins")
	if got := calls.Load(); got != 1 {
		t.Errorf("after unrelated notify: calls = %d; want still 1", got)
	}

	// site.url → matches → fires.
	c.Notify(context.Background(), "site.url")
	if got := calls.Load(); got != 2 {
		t.Errorf("after site.url notify: calls = %d; want 2", got)
	}
}

func TestConfig_KeysCoversEveryDispatchCase(t *testing.T) {
	// Catches drift: if a new case is added to reloadKey but the
	// authoring forgot to add the key to Keys(), or vice versa, this
	// test will fail because boot ReloadAll won't initialise the
	// orphaned slot.
	keys := Keys()
	if len(keys) < 10 {
		t.Errorf("Keys() returned %d keys; suspiciously small", len(keys))
	}
	seen := map[string]struct{}{}
	for _, k := range keys {
		if _, dup := seen[k]; dup {
			t.Errorf("Keys() lists %q twice", k)
		}
		seen[k] = struct{}{}
	}
}

func TestConfig_EnvFallbackWhenSettingAbsent(t *testing.T) {
	// Empty loader: env vars are the only source the dispatcher has.
	// Validates the pre-boot RAILBASE_* override path that
	// pkg/railbase/app.go relies on as fallback below the live
	// settings.Manager surface.
	t.Setenv("RAILBASE_LOGS_PERSIST", "false")
	t.Setenv("RAILBASE_CORS_ALLOWED_ORIGINS", "https://x.example,https://y.example")
	t.Setenv("RAILBASE_SITE_NAME", "EnvName")
	t.Setenv("RAILBASE_MAX_UPLOAD_BYTES", "1048576")

	c := New(&fakeLoader{vals: map[string]any{}}, map[string]string{
		"logs.persist":                  "RAILBASE_LOGS_PERSIST",
		"security.cors.allowed_origins": "RAILBASE_CORS_ALLOWED_ORIGINS",
		"site.name":                     "RAILBASE_SITE_NAME",
		"storage.max_upload_bytes":      "RAILBASE_MAX_UPLOAD_BYTES",
	})

	if c.LogsPersist() {
		t.Error("LogsPersist with env=false: got true; want false")
	}
	if got := c.CORSAllowedOrigins(); len(got) != 2 || got[0] != "https://x.example" {
		t.Errorf("CORSAllowedOrigins from env = %v", got)
	}
	if got := c.SiteName(); got != "EnvName" {
		t.Errorf("SiteName from env = %q", got)
	}
	if got := c.MaxUploadBytes(); got != 1<<20 {
		t.Errorf("MaxUploadBytes from env = %d", got)
	}
}

func TestConfig_LoaderTakesPrecedenceOverEnv(t *testing.T) {
	// Both env and Manager have a value → Manager wins. The unified-
	// runtimeconfig contract is "UI is the live source of truth"; env
	// is the BOOT-time override for when the operator hasn't saved
	// anything yet.
	t.Setenv("RAILBASE_SITE_NAME", "EnvLoses")
	c := New(&fakeLoader{vals: map[string]any{"site.name": "ManagerWins"}},
		map[string]string{"site.name": "RAILBASE_SITE_NAME"})

	if got := c.SiteName(); got != "ManagerWins" {
		t.Errorf("SiteName = %q; want \"ManagerWins\" (env override should not win when setting persisted)", got)
	}
}

func TestConfig_NotifyUnknownKeyIsNoOp(t *testing.T) {
	// Settings outside the runtimeconfig surface (mailer.*, etc.)
	// reach Notify too — the dispatcher is wired ONCE for the whole
	// bus. Unknown keys must NOT panic and must NOT clobber any slot.
	f := &fakeLoader{vals: map[string]any{"site.name": "Initial"}}
	c := New(f, nil)

	c.Notify(context.Background(), "mailer.driver")
	c.Notify(context.Background(), "totally.unknown.key")

	if got := c.SiteName(); got != "Initial" {
		t.Errorf("SiteName after unknown-key notify = %q; want unchanged \"Initial\"", got)
	}
}

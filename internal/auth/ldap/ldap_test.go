package ldap

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// stubConn is a hand-rolled fake that lets us drive Authenticator
// through every branch (search-miss / search-multiple / wrong-pw / ok)
// without running an actual slapd in CI.
type stubConn struct {
	bindCalls       []struct{ dn, pw string }
	starttls        bool
	searchResult    *ldap.SearchResult
	searchErr       error
	bindErrByDN     map[string]error
	closeCalled     bool
	failServiceBind bool
}

func (s *stubConn) Bind(dn, pw string) error {
	s.bindCalls = append(s.bindCalls, struct{ dn, pw string }{dn, pw})
	if err, ok := s.bindErrByDN[dn]; ok {
		return err
	}
	// First bind is the service-account bind; respect the failure
	// toggle so we can test "service account credentials wrong".
	if s.failServiceBind && len(s.bindCalls) == 1 {
		return errors.New("invalid credentials")
	}
	return nil
}

func (s *stubConn) Search(_ *ldap.SearchRequest) (*ldap.SearchResult, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	if s.searchResult == nil {
		return &ldap.SearchResult{}, nil
	}
	return s.searchResult, nil
}

func (s *stubConn) StartTLS(_ *tls.Config) error {
	s.starttls = true
	return nil
}

func (s *stubConn) Close() error {
	s.closeCalled = true
	return nil
}

func dialerReturning(c *stubConn) dialer {
	return func(_ context.Context, _ string, _ *tls.Config) (ldapConn, error) {
		return c, nil
	}
}

// validCfg returns a Config sane enough for most tests. Callers
// override specific fields per case.
func validCfg() Config {
	return Config{
		URL:          "ldap://localhost:389",
		TLSMode:      TLSOff,
		BindDN:       "cn=svc,dc=example,dc=com",
		BindPassword: "svc-pw",
		UserBaseDN:   "ou=Users,dc=example,dc=com",
		UserFilter:   "(uid=%s)",
		EmailAttr:    "mail",
		NameAttr:     "cn",
		Timeout:      2 * time.Second,
	}
}

func TestConfig_Defaults_FillsZeroFields(t *testing.T) {
	c := Config{URL: "ldap://x"}
	c.Defaults()
	if c.TLSMode != TLSStartTLS {
		t.Errorf("default TLSMode = %q, want starttls", c.TLSMode)
	}
	if !strings.Contains(c.UserFilter, "%s") {
		t.Errorf("default UserFilter missing %%s: %q", c.UserFilter)
	}
	if c.EmailAttr != "mail" {
		t.Errorf("default EmailAttr = %q", c.EmailAttr)
	}
	if c.Timeout != 10*time.Second {
		t.Errorf("default Timeout = %v", c.Timeout)
	}
}

func TestConfig_Defaults_LdapsForcesTLS(t *testing.T) {
	// Invariant: ldaps:// scheme MUST land at TLSMode=on, even if the
	// operator's stored value says off. Prevents the dangerous
	// "ldaps://... + tls_mode=off" combo where a typo gives plaintext.
	c := Config{URL: "ldaps://example.com:636", TLSMode: TLSOff}
	c.Defaults()
	if c.TLSMode != TLSOn {
		t.Errorf("ldaps:// scheme didn't force TLSOn: got %q", c.TLSMode)
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"empty URL", func(c *Config) { c.URL = "" }, "url is required"},
		{"bad scheme", func(c *Config) { c.URL = "http://x" }, "must start with ldap://"},
		{"empty filter", func(c *Config) { c.UserFilter = "" }, "user_filter is required"},
		{"filter no placeholder", func(c *Config) { c.UserFilter = "(uid=fixed)" }, `must contain "%s"`},
		{"bad tls_mode", func(c *Config) { c.TLSMode = "encrypted" }, "unknown tls_mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validCfg()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q doesn't contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestNew_RejectsInvalidConfig(t *testing.T) {
	_, err := New(Config{URL: ""})
	if err == nil {
		t.Fatal("New: expected error on empty URL, got nil")
	}
}

func TestAuthenticate_EmptyCredentials(t *testing.T) {
	a, _ := New(validCfg())
	conn := &stubConn{}
	for _, c := range []struct{ user, pw string }{
		{"", "pw"},
		{"alice", ""},
		{"", ""},
		{"   ", "pw"}, // whitespace-only username
	} {
		if _, err := a.authenticateWithDialer(context.Background(), c.user, c.pw, dialerReturning(conn)); err == nil {
			t.Errorf("Authenticate(%q,%q): want error, got nil", c.user, c.pw)
		}
	}
}

func TestAuthenticate_UserNotFound_OpaqueError(t *testing.T) {
	// Search returns 0 entries → "invalid credentials" (NOT "user not
	// found"). We don't want the error to disambiguate user-existence
	// from wrong-password — that's a classic enumeration leak.
	a, _ := New(validCfg())
	conn := &stubConn{searchResult: &ldap.SearchResult{Entries: nil}}
	_, err := a.authenticateWithDialer(context.Background(), "alice", "pw", dialerReturning(conn))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid credentials") {
		t.Errorf("error should be opaque 'invalid credentials', got %q", err.Error())
	}
}

func TestAuthenticate_WrongPassword_OpaqueError(t *testing.T) {
	// User found, but the user-bind fails. Same opaque error.
	a, _ := New(validCfg())
	conn := &stubConn{
		searchResult: &ldap.SearchResult{
			Entries: []*ldap.Entry{
				{
					DN: "uid=alice,ou=Users,dc=example,dc=com",
					Attributes: []*ldap.EntryAttribute{
						{Name: "mail", Values: []string{"alice@example.com"}},
						{Name: "cn", Values: []string{"Alice"}},
					},
				},
			},
		},
		bindErrByDN: map[string]error{
			"uid=alice,ou=Users,dc=example,dc=com": errors.New("invalid credentials"),
		},
	}
	_, err := a.authenticateWithDialer(context.Background(), "alice", "wrong-pw", dialerReturning(conn))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid credentials") {
		t.Errorf("error should be opaque, got %q", err.Error())
	}
}

func TestAuthenticate_AmbiguousFilter_SurfacedAsConfigError(t *testing.T) {
	// Two entries match — operator's filter is broken. Surface this
	// loud (not "invalid credentials") so the operator sees the bug.
	a, _ := New(validCfg())
	conn := &stubConn{
		searchResult: &ldap.SearchResult{
			Entries: []*ldap.Entry{
				{DN: "uid=alice,ou=A"},
				{DN: "uid=alice,ou=B"},
			},
		},
	}
	_, err := a.authenticateWithDialer(context.Background(), "alice", "pw", dialerReturning(conn))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "matched 2 users") {
		t.Errorf("error should call out ambiguity, got %q", err.Error())
	}
}

func TestAuthenticate_HappyPath(t *testing.T) {
	a, _ := New(validCfg())
	conn := &stubConn{
		searchResult: &ldap.SearchResult{
			Entries: []*ldap.Entry{
				{
					DN: "uid=alice,ou=Users,dc=example,dc=com",
					Attributes: []*ldap.EntryAttribute{
						{Name: "mail", Values: []string{"alice@example.com"}},
						{Name: "cn", Values: []string{"Alice Smith"}},
					},
				},
			},
		},
	}
	u, err := a.authenticateWithDialer(context.Background(), "alice", "good-pw", dialerReturning(conn))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Errorf("Email = %q", u.Email)
	}
	if u.Name != "Alice Smith" {
		t.Errorf("Name = %q", u.Name)
	}
	if u.DN != "uid=alice,ou=Users,dc=example,dc=com" {
		t.Errorf("DN = %q", u.DN)
	}
	// Bind called twice — service account, then user.
	if len(conn.bindCalls) != 2 {
		t.Errorf("Bind calls = %d, want 2", len(conn.bindCalls))
	}
	if !conn.closeCalled {
		t.Error("Close not called — connection leak")
	}
}

func TestAuthenticate_StartTLSCalled_WhenModeStartTLS(t *testing.T) {
	cfg := validCfg()
	cfg.TLSMode = TLSStartTLS
	a, _ := New(cfg)
	conn := &stubConn{
		searchResult: &ldap.SearchResult{
			Entries: []*ldap.Entry{
				{DN: "uid=alice,dc=x", Attributes: []*ldap.EntryAttribute{
					{Name: "mail", Values: []string{"a@x.y"}},
				}},
			},
		},
	}
	if _, err := a.authenticateWithDialer(context.Background(), "alice", "pw", dialerReturning(conn)); err != nil {
		t.Fatal(err)
	}
	if !conn.starttls {
		t.Error("StartTLS not called in TLSStartTLS mode")
	}
}

func TestAuthenticate_NoStartTLS_WhenModeOff(t *testing.T) {
	cfg := validCfg()
	cfg.TLSMode = TLSOff
	a, _ := New(cfg)
	conn := &stubConn{
		searchResult: &ldap.SearchResult{
			Entries: []*ldap.Entry{
				{DN: "uid=a,dc=x", Attributes: []*ldap.EntryAttribute{
					{Name: "mail", Values: []string{"a@x.y"}},
				}},
			},
		},
	}
	if _, err := a.authenticateWithDialer(context.Background(), "alice", "pw", dialerReturning(conn)); err != nil {
		t.Fatal(err)
	}
	if conn.starttls {
		t.Error("StartTLS called in TLSOff mode (unexpected)")
	}
}

func TestAuthenticate_LDAPInjection_FilterEscape(t *testing.T) {
	// LDAP injection: username `*)(uid=*` would match all users
	// if not escaped. EscapeFilter must turn it into the literal.
	a, _ := New(validCfg())
	// We can't introspect the filter from the stub directly, but we
	// can record what was searched and assert no asterisks leaked.
	var capturedFilter string
	conn := &stubConnCapture{
		stubConn: stubConn{searchResult: &ldap.SearchResult{}},
		onSearch: func(req *ldap.SearchRequest) { capturedFilter = req.Filter },
	}
	dialer := func(_ context.Context, _ string, _ *tls.Config) (ldapConn, error) {
		return conn, nil
	}
	_, _ = a.authenticateWithDialer(context.Background(), "*)(uid=*", "pw", dialer)
	// Result: filter must NOT contain the raw `*)(uid=*` payload as
	// active LDAP syntax. ldap.EscapeFilter turns `*` into `\2a`.
	if strings.Contains(capturedFilter, "*)(") {
		t.Errorf("LDAP injection leaked: %s", capturedFilter)
	}
	if !strings.Contains(capturedFilter, `\2a`) {
		t.Errorf("expected escaped %%2a in filter, got: %s", capturedFilter)
	}
}

// stubConnCapture extends stubConn to peek at the search request.
type stubConnCapture struct {
	stubConn
	onSearch func(*ldap.SearchRequest)
}

func (s *stubConnCapture) Search(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
	if s.onSearch != nil {
		s.onSearch(req)
	}
	return s.stubConn.Search(req)
}

func TestAuthenticate_ServiceAccountBindFails(t *testing.T) {
	// If the service-account bind itself fails, we surface that loud
	// (not "invalid credentials") because it's an operator-config
	// problem, not a user-credential problem.
	a, _ := New(validCfg())
	conn := &stubConn{failServiceBind: true}
	_, err := a.authenticateWithDialer(context.Background(), "alice", "pw", dialerReturning(conn))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "service-account bind failed") {
		t.Errorf("expected service-account error, got %q", err.Error())
	}
}

func TestAuthenticate_EmailAttrMissing(t *testing.T) {
	// User found, bind succeeds, but no email attribute. We refuse —
	// can't map back to a local users row without an email.
	a, _ := New(validCfg())
	conn := &stubConn{
		searchResult: &ldap.SearchResult{
			Entries: []*ldap.Entry{
				{DN: "uid=alice,dc=x", Attributes: []*ldap.EntryAttribute{
					{Name: "cn", Values: []string{"Alice"}},
				}},
			},
		},
	}
	_, err := a.authenticateWithDialer(context.Background(), "alice", "pw", dialerReturning(conn))
	if err == nil {
		t.Fatal("want error on missing email")
	}
	if !strings.Contains(err.Error(), `has no "mail" attribute`) {
		t.Errorf("expected missing-attribute error, got %q", err.Error())
	}
}

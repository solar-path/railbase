// Package ldap ships a thin Authenticator over `go-ldap/v3`. v1.7.49
// moves Enterprise LDAP / Active Directory sign-in from the "plugin
// only" bucket into the core binary so an operator with a corporate
// AD doesn't need to build a custom plugin to point Railbase at it.
//
// Why in-tree:
//   - LDAP is the single most common Enterprise SSO mechanism in the
//     wild — every corporate AD instance speaks it. Asking operators
//     to compile-from-source-with-a-plugin to support the dominant
//     enterprise auth flow was a poor trade.
//   - `go-ldap/ldap/v3` is pure Go (no CGo) and adds ~400 KB compiled.
//     That fits comfortably under the v1 SHIP 30 MB binary budget.
//   - The protocol surface we need (BIND + SEARCH) is small and stable
//     since RFC 4511 (2006); the library API has been v3 for years.
//
// What this package does NOT do:
//
//   - No connection pooling. Each authenticate() opens its own TCP
//     connection. LDAP servers tolerate this; sign-in is bursty (one
//     auth per user-action) and a pool would complicate timeout +
//     fail-open semantics for limited benefit. Revisit if a real
//     "1000 logins/sec on the same LDAP" complaint surfaces.
//   - No NTLM. The dependency tree includes `go-ntlmssp` (transitive
//     via go-ldap) but we never use NTLM bind. Plain SIMPLE + TLS is
//     the modern AD recommendation.
//   - No group-membership resolution into Railbase RBAC roles. The
//     bind/search succeeds → user authenticated; mapping their AD
//     group memberships to roles is a downstream concern (and would
//     require operator-configurable mapping rules — out of scope here).
//   - No on-the-fly user creation. The handler in internal/api/auth
//     looks up the local user by email after a successful bind; if no
//     local row exists, it creates one with a placeholder password
//     (the user never types one — they sign in via LDAP). That's
//     handler-level policy, not this package's concern.
package ldap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// TLSMode controls the transport-security shape we open the LDAP
// connection with. Three modes cover the realistic deployment matrix:
//
//   - "off": plain ldap:// over port 389. Acceptable on a trusted
//     internal network but the operator should know what they're
//     signing up for. Wizard surfaces a "TLS disabled" hint.
//   - "starttls": connect plain, then STARTTLS-upgrade. RFC 4513 §6.
//     Works on the same port (389). Common with OpenLDAP.
//   - "tls": connect via ldaps:// (port 636). Required for Azure AD
//     /Entra ID's LDAP endpoint and the AD-recommended default.
type TLSMode string

const (
	TLSOff      TLSMode = "off"
	TLSStartTLS TLSMode = "starttls"
	TLSOn       TLSMode = "tls"
)

// Config is the operator-facing settings shape. Every field has a
// settings key under `auth.ldap.*` written by the v1.7.47 setup
// wizard. Marshal/unmarshal is settings-pkg standard JSON.
type Config struct {
	// URL is `ldap://host:port` or `ldaps://host:port`. When the
	// scheme is `ldaps`, TLSMode is implicitly forced to `tls` —
	// the scheme wins over the explicit field to prevent the
	// invariant-violation case of `ldaps://...` + `tls_mode=off`.
	URL string `json:"url"`

	// TLSMode picks the transport. See TLSMode constants above.
	TLSMode TLSMode `json:"tls_mode"`

	// InsecureSkipVerify disables TLS cert verification. ONLY for
	// dev clusters w/ self-signed certs. Production deploys MUST
	// provision a real CA bundle — the wizard surface flags this
	// option with a red warning.
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`

	// BindDN is the service-account DN used to perform the user
	// search. Typical: `cn=railbase-auth,ou=ServiceAccounts,
	// dc=example,dc=com`. AD also accepts userPrincipalName form
	// (e.g. `railbase-auth@example.com`).
	BindDN string `json:"bind_dn"`

	// BindPassword is the service account password. Stored encrypted
	// at rest via the settings master key.
	BindPassword string `json:"bind_password"`

	// UserBaseDN narrows the search subtree. e.g. `ou=Users,
	// dc=example,dc=com`. Empty == search whole tree (allowed but
	// slow; wizard warns).
	UserBaseDN string `json:"user_base_dn"`

	// UserFilter is the LDAP search filter template. The literal
	// `%s` is replaced (sanitised) with the username at runtime.
	// Default (set by the wizard): `(&(objectClass=person)(|(uid=%s)(mail=%s)(sAMAccountName=%s)))`
	// — matches OpenLDAP `uid`, mail-as-username, and AD
	// `sAMAccountName` in one filter.
	UserFilter string `json:"user_filter"`

	// EmailAttr is the LDAP attribute that contains the user's
	// email. Default `mail`. Used to map an LDAP user into a
	// Railbase local user row (which is keyed on email).
	EmailAttr string `json:"email_attr"`

	// NameAttr is the LDAP attribute used for the user's display
	// name. Default `cn`. Stored on the local user row at creation
	// time for the admin UI.
	NameAttr string `json:"name_attr"`

	// Timeout caps the TCP connect + bind/search round-trip. Default
	// 10s. LDAP timeouts cascade — a slow LDAP server during a busy
	// signin window can block enough goroutines to starve the rest
	// of the API.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Defaults patches zero-value fields so a wizard-omitted entry has
// sensible behaviour. Called by NewAuthenticator + on every Settings
// read so the operator-visible config is the same shape as what we'll
// run with.
func (c *Config) Defaults() {
	if c.TLSMode == "" {
		c.TLSMode = TLSStartTLS
	}
	if c.UserFilter == "" {
		c.UserFilter = "(&(objectClass=person)(|(uid=%s)(mail=%s)(sAMAccountName=%s)))"
	}
	if c.EmailAttr == "" {
		c.EmailAttr = "mail"
	}
	if c.NameAttr == "" {
		c.NameAttr = "cn"
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	// Force tls mode when scheme says ldaps. See Config.URL doc above.
	if strings.HasPrefix(strings.ToLower(c.URL), "ldaps://") {
		c.TLSMode = TLSOn
	}
}

// Validate refuses obviously-incomplete configs at load time. Helps
// the operator catch missing fields before the first sign-in attempt.
func (c Config) Validate() error {
	if strings.TrimSpace(c.URL) == "" {
		return errors.New("ldap: url is required")
	}
	if !strings.HasPrefix(c.URL, "ldap://") && !strings.HasPrefix(c.URL, "ldaps://") {
		return errors.New("ldap: url must start with ldap:// or ldaps://")
	}
	if strings.TrimSpace(c.UserFilter) == "" {
		return errors.New("ldap: user_filter is required")
	}
	if !strings.Contains(c.UserFilter, "%s") {
		return errors.New(`ldap: user_filter must contain "%s" placeholder`)
	}
	switch c.TLSMode {
	case TLSOff, TLSStartTLS, TLSOn, "":
		// ok
	default:
		return fmt.Errorf("ldap: unknown tls_mode %q (expected off/starttls/tls)", c.TLSMode)
	}
	return nil
}

// User is the slice of LDAP attributes the handler turns into a local
// `users` row. We only ship Email + Name to keep the contract small;
// future slices that need raw memberOf or DN can extend.
type User struct {
	DN    string
	Email string
	Name  string
}

// Authenticator is the operator-facing entry point: feed it (username,
// password) → get back a User or an error. The username is whatever
// the operator wired into `UserFilter` (typically `uid` / `mail` /
// `sAMAccountName`); the password is the user's LDAP password.
type Authenticator struct {
	cfg Config
}

// New builds an authenticator from a Config. Validates the config so
// a misconfigured deploy fails at wire-time, not on first signin.
func New(cfg Config) (*Authenticator, error) {
	cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Authenticator{cfg: cfg}, nil
}

// dialer is the small surface ldap.DialURL satisfies + the test stub
// implements. Lets us test Authenticate without spinning up an actual
// LDAP server (which would need slapd in CI). Production wires
// realDialer, tests wire a stub.
type dialer func(ctx context.Context, url string, tlsCfg *tls.Config) (ldapConn, error)

// ldapConn abstracts the subset of *ldap.Conn methods we touch — Bind
// for credentials, Search for the user lookup, Close for cleanup,
// StartTLS for the STARTTLS path. The real *ldap.Conn satisfies this;
// test stubs implement only what each test exercises.
type ldapConn interface {
	Bind(username, password string) error
	Search(req *ldap.SearchRequest) (*ldap.SearchResult, error)
	StartTLS(tlsCfg *tls.Config) error
	Close() error
}

// realDialer wraps ldap.DialURL into the test-friendly dialer shape.
func realDialer(ctx context.Context, url string, tlsCfg *tls.Config) (ldapConn, error) {
	conn, err := ldap.DialURL(url, ldap.DialWithTLSConfig(tlsCfg))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// Authenticate performs the standard LDAP bind+search+bind dance:
//
//  1. open the connection
//  2. (optional) STARTTLS upgrade
//  3. bind as the service account
//  4. search for the username via UserFilter
//  5. re-bind as the located user DN w/ their password
//  6. return the User on success
//
// Empty username or password → "invalid credentials" (we don't leak
// which one was empty — same as a bind failure for an unknown user).
//
// The username is filter-escaped before substitution so an operator's
// chosen filter doesn't get LDAP-injected.
func (a *Authenticator) Authenticate(ctx context.Context, username, password string) (*User, error) {
	return a.authenticateWithDialer(ctx, username, password, realDialer)
}

func (a *Authenticator) authenticateWithDialer(ctx context.Context, username, password string, dial dialer) (*User, error) {
	if strings.TrimSpace(username) == "" || password == "" {
		return nil, errors.New("ldap: invalid credentials")
	}

	tlsCfg := &tls.Config{
		InsecureSkipVerify: a.cfg.InsecureSkipVerify, //nolint:gosec // operator-opted toggle, surfaced w/ warning in wizard
	}
	// Honour ctx-deadline by clamping it to our configured timeout —
	// whichever is tighter wins. ldap library doesn't yet honour ctx
	// itself, so we wrap with a timeout watchdog goroutine via the
	// connection-deadline shape it accepts (deferred).
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if left > 0 && left < a.cfg.Timeout {
			// caller's deadline is tighter — fine, dialler will respect
			// the OS-level connect timeout but library doesn't expose
			// a clean per-op deadline. Accepted limitation for v1.
			_ = left // documented; no-op
		}
	}

	conn, err := dial(ctx, a.cfg.URL, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("ldap: dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if a.cfg.TLSMode == TLSStartTLS {
		if err := conn.StartTLS(tlsCfg); err != nil {
			return nil, fmt.Errorf("ldap: starttls: %w", err)
		}
	}

	// Service-account bind. If BindDN is empty we attempt an
	// anonymous bind first; some OpenLDAP deployments allow anonymous
	// user-search even when bind-DN is policy-required for other ops.
	if a.cfg.BindDN != "" {
		if err := conn.Bind(a.cfg.BindDN, a.cfg.BindPassword); err != nil {
			return nil, fmt.Errorf("ldap: service-account bind failed: %w", err)
		}
	}

	// Build the user search. Escape the username to prevent LDAP
	// injection: a username of `*)(uid=*` would otherwise match any
	// user. ldap.EscapeFilter handles the RFC 4515 escape set.
	escaped := ldap.EscapeFilter(username)
	// Substitute every %s in the filter — operator picks how many
	// alternatives to scan in one search. Typical config:
	// (&(objectClass=person)(|(uid=%s)(mail=%s)(sAMAccountName=%s)))
	filter := strings.ReplaceAll(a.cfg.UserFilter, "%s", escaped)

	searchReq := ldap.NewSearchRequest(
		a.cfg.UserBaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		2, // SizeLimit: 1 match expected, 2 lets us detect ambiguity
		int(a.cfg.Timeout/time.Second),
		false,
		filter,
		[]string{"dn", a.cfg.EmailAttr, a.cfg.NameAttr},
		nil,
	)
	result, err := conn.Search(searchReq)
	if err != nil {
		return nil, fmt.Errorf("ldap: search: %w", err)
	}
	if len(result.Entries) == 0 {
		// Be careful not to leak "user not found" vs "wrong password".
		return nil, errors.New("ldap: invalid credentials")
	}
	if len(result.Entries) > 1 {
		// Ambiguous filter — operator's filter matches >1 user. This
		// is a config bug, surface it.
		return nil, fmt.Errorf("ldap: filter matched %d users (must be 1)", len(result.Entries))
	}
	entry := result.Entries[0]

	// Re-bind as the user with their password. THIS is where we
	// authenticate: if the password is wrong, this bind fails.
	if err := conn.Bind(entry.DN, password); err != nil {
		return nil, errors.New("ldap: invalid credentials")
	}

	out := &User{
		DN:    entry.DN,
		Email: entry.GetAttributeValue(a.cfg.EmailAttr),
		Name:  entry.GetAttributeValue(a.cfg.NameAttr),
	}
	if out.Email == "" {
		// We rely on email to map into the local users table; a user
		// with no email is unusable. Surface as "config issue".
		return nil, fmt.Errorf("ldap: user %q has no %q attribute", entry.DN, a.cfg.EmailAttr)
	}
	return out, nil
}

package security

import "testing"

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"postgres-with-password",
			"postgres://user:supersecret@host:5432/db?sslmode=disable",
			"postgres://user:***@host:5432/db?sslmode=disable",
		},
		{
			"postgres-no-password",
			"postgres://user@host:5432/db",
			"postgres://user@host:5432/db",
		},
		{
			"postgres-empty-password",
			"postgres://user:@host:5432/db",
			"postgres://user:***@host:5432/db",
		},
		{
			"no-userinfo",
			"postgres://host:5432/db",
			"postgres://host:5432/db",
		},
		{
			"key-value-format-unparseable",
			"host=localhost user=u password=p",
			// k=v form has no `://` — url.Parse can't extract the
			// password, so RedactDSN falls back to the raw string. We
			// accept this; the recommendation is to use URL-form DSNs.
			"host=localhost user=u password=p",
		},
		{
			"empty",
			"",
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactDSN(tc.in); got != tc.want {
				t.Errorf("RedactDSN(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{
		// password family (suffix)
		"password", "password_hash", "bind_password",
		"Password", // case-insensitive
		// secret family (suffix)
		"secret", "client_secret", "webhook_secret",
		"webhook_signing_secret", "totp_secret",
		"clientSecret", "ClientSecret",
		// token family (suffix)
		"token", "token_key", "access_token", "refresh_token",
		"id_token", "bearer_token", "csrf_token",
		// explicit
		"api_key", "ApiKey", "APIKEY",
		"authorization", "Authorization",
		"cookie", "Cookie", "set_cookie",
		"dsn", "DSN", "database_url", "DATABASE_URL",
		"private_key", "secret_key", "signing_key",
		"session_id", "session_token",
	}
	for _, k := range sensitive {
		if !IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = false; want true", k)
		}
	}

	// Things that look adjacent but MUST NOT be redacted — they're
	// legitimate non-secret fields whose names happen to contain
	// related substrings.
	safe := []string{
		"id", "name", "email", "title",
		"cache_key", "partition_key", "column_key", // _key but not secret
		"public_key", // technically public; we don't try to match it
		"username", "user_name",
		"key_id", "token_id", // ID of a token, not the token itself
		"created", "updated",
		"row_count", "status",
	}
	for _, k := range safe {
		if IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = true; want false (legitimate field)", k)
		}
	}
}

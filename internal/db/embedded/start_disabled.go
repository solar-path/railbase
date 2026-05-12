//go:build !embed_pg

// Default build (no `embed_pg` tag): Start always returns
// ErrNotCompiledIn. Users who want the convenience must rebuild with
// `make build-embed` or `go build -tags embed_pg ./cmd/railbase`.

package embedded

import (
	"context"
	"errors"
)

// ErrNotCompiledIn is returned by Start when the binary was built
// without `-tags embed_pg`.
var ErrNotCompiledIn = errors.New(
	"embedded postgres not compiled in: rebuild with -tags embed_pg, " +
		"or unset RAILBASE_EMBED_POSTGRES and provide RAILBASE_DSN",
)

func start(_ context.Context, _ Config) (string, StopFunc, error) {
	return "", nil, ErrNotCompiledIn
}

// Available reports whether this binary was built with `-tags embed_pg`.
// Returns false in the default (production) build so callers — notably
// pkg/railbase/app.Run — can pick the setup-only fallback path instead
// of trying to spawn an embedded subprocess that isn't linked in.
func Available() bool { return false }

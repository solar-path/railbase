// Package sys exposes Railbase's built-in (system) migrations as an
// embed.FS. These are applied at startup, before any user migrations.
//
// Adding a migration: create NNNN_<slug>.up.sql in this directory and
// commit. The embed directive picks it up automatically.
//
// Constraints:
//   - Files MUST be named NNNN_<slug>.up.sql (NNNN = integer, slug =
//     [a-z0-9_]+). See internal/db/migrate package docs.
//   - Once a migration ships in a release, do NOT edit its body —
//     the runner detects content drift via SHA-256 and fails startup
//     unless --allow-drift is passed.
package sys

import "embed"

//go:embed *.sql
var FS embed.FS

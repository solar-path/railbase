// Command railbase is the bare Railbase server — useful for testing
// the binary itself and for scaffolding projects via `railbase init`.
//
// Real applications run their own binary built from the scaffold
// (`railbase init mydemo` → `cd mydemo` → `go build ./cmd/mydemo`).
// That binary embeds the user's schema and serves it; the bare
// railbase binary has no schema registered, so `serve` works but
// exposes no collections.
package main

import "github.com/railbase/railbase/pkg/railbase/cli"

func main() {
	// ExecuteWithInit wires the `init` subcommand. User project
	// binaries should call cli.Execute() instead.
	cli.ExecuteWithInit()
}

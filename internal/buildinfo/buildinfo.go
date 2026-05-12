// Package buildinfo is populated at build time via -ldflags.
// Falls back to "dev" / runtime.Version() when run via `go run`.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Set via: go build -ldflags="-X github.com/railbase/railbase/internal/buildinfo.Commit=$(git rev-parse HEAD) ..."
var (
	Commit = ""
	Date   = ""
	Tag    = ""
)

// String returns a human-readable build identifier suitable for
// `railbase --version` and the X-Railbase-Version response header.
func String() string {
	commit := Commit
	if commit == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" {
					commit = s.Value
					break
				}
			}
		}
	}
	if commit == "" {
		commit = "dev"
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	tag := Tag
	if tag == "" {
		tag = "v0.0.0-dev"
	}
	date := Date
	if date == "" {
		date = "unknown"
	}
	return tag + " (" + commit + ", " + date + ", " + runtime.Version() + ")"
}

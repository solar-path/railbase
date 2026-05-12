// Package railbase exposes the public API surface for embedders.
//
// At v0 only the version constant is stable. The schema DSL, server
// constructor, and plugin host live in internal/* until the v1 freeze.
package railbase

// Version is the running Railbase release. Embedders read this for
// User-Agent strings, telemetry, and migration gates.
const Version = "0.0.0-dev"

// Package embed serves the built-in default translation bundles. The
// railbase binary ships en + ru pre-translated; operators add their
// own languages or override individual keys via files in
// `pb_data/i18n/<lang>.json`.
package embed

import "embed"

//go:embed *.json
var FS embed.FS

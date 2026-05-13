module github.com/railbase/railbase

go 1.26

// v0.1 deps:
//   - chi: HTTP router
//   - pgx/v5: Postgres driver (the only DB backend)
//   - cobra: CLI framework
//   - embedded-postgres: only compiled in with -tags embed_pg, but listed here
//     unconditionally because go mod tidy walks all build tags.
require (
	github.com/fergusstrange/embedded-postgres v1.27.0
	github.com/go-chi/chi/v5 v5.1.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/spf13/cobra v1.8.1
)

require (
	github.com/brianvoe/gofakeit/v7 v7.14.1
	github.com/coder/websocket v1.8.14
	github.com/dop251/goja v0.0.0-20260311135729-065cd970411c
	github.com/fsnotify/fsnotify v1.10.1
	github.com/gomarkdown/markdown v0.0.0-20260417124207-7d523f7318df
	github.com/signintech/gopdf v0.36.0
	github.com/xuri/excelize/v2 v2.10.1
	golang.org/x/crypto v0.48.0
	golang.org/x/term v0.40.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/Azure/go-ntlmssp v0.1.0 // indirect
	github.com/beevik/etree v1.6.0 // indirect
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/go-asn1-ber/asn1-ber v1.5.8-0.20250403174932-29230038a667 // indirect
	github.com/go-ldap/ldap/v3 v3.4.13 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20230207041349-798e818bf904 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lib/pq v1.10.4 // indirect
	github.com/mattermost/xml-roundtrip-validator v0.1.0 // indirect
	github.com/phpdave11/gofpdi v1.0.14-0.20211212211723-1f10f9844311 // indirect
	github.com/pkg/errors v0.8.1 // indirect
	github.com/richardlehane/mscfb v1.0.6 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/russellhaering/gosaml2 v0.11.0 // indirect
	github.com/russellhaering/goxmldsig v1.6.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/xi2/xz v0.0.0-20171230120015-48954b6210f8 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
)

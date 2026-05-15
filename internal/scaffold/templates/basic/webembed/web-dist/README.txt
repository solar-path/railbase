This directory is populated by `railbase build`:

    railbase build --web ./web

That command runs `npm run build` inside ./web, then atomically
replaces this directory's contents with ./web/dist/.

Until you've run a build at least once, web-dist/ contains only this
file. The //go:embed directive in embed.go REQUIRES at least one file
in the embedded subtree (otherwise `go build` fails with "no matching
files found"), which is why this README ships in the scaffold.

If you don't want a frontend at all, delete the webembed/ package and
the `app.ServeStaticFS("/", webembed.FS())` line in your main.go.

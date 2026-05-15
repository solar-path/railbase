// pb_hooks/example.pb.js — sample JS hook. Files matching `*.pb.js` under
// this directory are loaded by the embedded goja runtime at server start
// and hot-reloaded on save (~150 ms debounce). Comment OUT or rename to
// `*.disabled` to skip a file without deleting it.
//
// API (v1.2.0, PB-compatible v2 style):
//
//   $app.onRecordBeforeCreate("collection")
//       .bindFunc((e) => { ...; e.next(); });
//   $app.onRecordAfterCreate("collection")
//       .bindFunc((e) => { ...; e.next(); });
//
// Other lifecycle events: BeforeUpdate / AfterUpdate / BeforeDelete /
// AfterDelete. Throwing inside a Before-hook aborts the request with
// a 400 validation error; After-hook throws are logged but don't undo
// the DB write (already committed).
//
// `e.record` is a plain JS object — read/write fields directly:
//   const title = e.record.title;
//   e.record.title = title.trim();
//
// Also available on $app:
//   $app.routerAdd("GET", "/hello/:name", (c) => c.json(200, {...}))
//   $app.cronAdd("nightly", "0 3 * * *", () => { ... })
//   $app.onRequest((e) => { ...; e.next(); })  // every request, sync
//
// Watchdog: each handler invocation is capped at 5s wall time. Handlers
// hung in a loop won't take the request thread with them — the
// dispatcher times out and returns 500.

// Example: trim + validate a `posts.title` before insert.
$app.onRecordBeforeCreate("posts").bindFunc((e) => {
  const title = (e.record.title || "").trim();
  if (!title) {
    throw new Error("title required");
  }
  e.record.title = title;
  e.next();
});

// Example: log creations (After-hook fires AFTER commit; safe for side
// effects).
$app.onRecordAfterCreate("posts").bindFunc((e) => {
  console.log("post created:", e.record.id);
  e.next();
});

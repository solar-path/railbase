package ts

import "strings"

// EmitRealtime renders realtime.ts: a typed SSE subscription client
// for GET /api/realtime (internal/realtime). Schema-independent — the
// realtime endpoint is fixed, not derived from CollectionSpec.
//
// Why a fetch-stream reader instead of the browser's EventSource:
// EventSource cannot set an Authorization header, and Railbase's
// realtime stream requires an authenticated principal. So the client
// reads the SSE body off an authenticated fetch() — that's what the
// `stream()` method on HTTPClient (index.ts) exists for.
//
// The wrapper yields real topic events only; SSE comment frames
// (heartbeats) and the internal `railbase.*` control events
// (subscribed / replay-truncated) are filtered out. Resume after a
// drop by passing the last seen event `id` as `since`.
func EmitRealtime() string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(`// realtime.ts — typed SSE subscription client for /api/realtime.

import type { HTTPClient } from "./index.js";

/** One realtime event delivered over the SSE stream. */
export interface RealtimeEvent {
  /** Monotonic broker event id. Pass the last one seen as ` + "`since`" + ` to resume. */
  id: number;
  /** The matched topic, e.g. "posts/<uuid>" for a "posts/*" subscription. */
  topic: string;
  /** Decoded event payload (record snapshot etc); raw string if not JSON. */
  data: unknown;
}

export interface RealtimeOptions {
  /** Topic patterns to subscribe to, e.g. ["posts/*", "users/me"]. */
  topics: string[];
  /** Resume from this broker event id (Last-Event-ID semantics). */
  since?: number;
  /** Abort the stream — close the loop and release the connection. */
  signal?: AbortSignal;
}

/** Realtime wrappers. Requires an authenticated principal — set the
 *  client token via createRailbaseClient({ token }) or setToken().
 *
 *    const rb = createRailbaseClient({ baseURL, token });
 *    const ctrl = new AbortController();
 *    for await (const ev of rb.realtime.subscribe({
 *      topics: ["posts/*"], signal: ctrl.signal,
 *    })) {
 *      console.log(ev.topic, ev.data);
 *    }
 *    // ctrl.abort() to stop.
 */
export function realtimeClient(http: HTTPClient) {
  return {
    /** Open an SSE subscription. Returns an async iterator of events —
     *  iterate with ` + "`for await`" + `. The iterator ends when ` + "`signal`" + ` aborts
     *  or the connection drops. */
    async *subscribe(opts: RealtimeOptions): AsyncGenerator<RealtimeEvent> {
      const q = new URLSearchParams({ topics: opts.topics.join(",") });
      if (opts.since != null) q.set("since", String(opts.since));
      const res = await http.stream("/api/realtime?" + q.toString(), {
        method: "GET",
        headers: { Accept: "text/event-stream" },
        signal: opts.signal,
      });
      if (!res.ok || !res.body) {
        throw new Error("realtime: stream failed with status " + res.status);
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = "";
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += decoder.decode(value, { stream: true });
          // SSE frames are separated by a blank line.
          let idx: number;
          while ((idx = buf.indexOf("\n\n")) !== -1) {
            const frame = buf.slice(0, idx);
            buf = buf.slice(idx + 2);
            const ev = parseFrame(frame);
            if (ev) yield ev;
          }
        }
      } finally {
        reader.releaseLock();
      }
    },
  };
}

// parseFrame turns one raw SSE frame into a RealtimeEvent, or null for
// comment/heartbeat frames and the internal railbase.* control events
// (railbase.subscribed, railbase.replay-truncated) — callers iterate
// real topic events only.
function parseFrame(frame: string): RealtimeEvent | null {
  let id = 0;
  let event = "";
  let data = "";
  for (const line of frame.split("\n")) {
    if (line === "" || line.startsWith(":")) continue; // comment / heartbeat
    if (line.startsWith("id:")) id = parseInt(line.slice(3).trim(), 10) || 0;
    else if (line.startsWith("event:")) event = line.slice(6).trim();
    else if (line.startsWith("data:")) data += line.slice(5).trim();
  }
  if (event === "" || event.startsWith("railbase.")) return null;
  let parsed: unknown = data;
  try {
    parsed = JSON.parse(data);
  } catch {
    /* leave as raw string */
  }
  return { id, topic: event, data: parsed };
}
`)
	return b.String()
}

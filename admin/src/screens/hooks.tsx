import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Editor, { type OnMount } from "@monaco-editor/react";
import { adminAPI } from "../api/admin";
import { APIError, isAPIError } from "../api/client";
import type {
  HooksFile,
  HookEventName,
  HookTestRunResult,
} from "../api/types";

// Hooks editor admin screen (v1.7.20 §3.14 #123 / §3.11).
//
// Two-pane layout:
//
//   ┌──── 250px ─────┬───────────────────────────────┐
//   │ + new          │ filename.js     [Saved]       │
//   │                │ ─────────────────────────────── │
//   │ on_post.js  🗑│                                │
//   │ on_user.js  🗑│        Monaco editor          │
//   │ sub/foo.js  🗑│        (vs-dark, javascript)  │
//   │                │                                │
//   └────────────────┴───────────────────────────────┘
//
// Auto-save: debounced 800 ms after every keystroke. Status pill shows
// `idle` → `saving…` → `saved` in the header. Format button delegates
// to Monaco's built-in formatter. Reload button re-fetches the file
// from disk (with a dirty-confirm prompt).
//
// "Hooks directory not configured" empty state: the list endpoint
// returns 503 `unavailable` until pkg/railbase/app.go wires HooksDir.
// We catch the typed error and render the operator-facing hint.

const NEW_FILE_TEMPLATE = `// $app.onRecordBeforeCreate("collection_name", (e) => {
  // your code
})
`;

type SaveStatus = "idle" | "saving" | "saved" | "error";

export function HooksScreen() {
  const qc = useQueryClient();
  const [selected, setSelected] = useState<string | null>(null);
  const [editorValue, setEditorValue] = useState<string>("");
  const [savedValue, setSavedValue] = useState<string>("");
  const [status, setStatus] = useState<SaveStatus>("idle");
  const [statusDetail, setStatusDetail] = useState<string | null>(null);
  const monacoRef = useRef<Parameters<OnMount>[0] | null>(null);
  // Track the in-flight save so we don't fire a redundant PUT when the
  // debounced timer wakes up with the same content we just persisted.
  const debounceRef = useRef<number | null>(null);

  const listQ = useQuery({
    queryKey: ["hooks-files"],
    queryFn: () => adminAPI.hooksFilesList(),
    retry: (_count, err) => !isAPIError(err, "unavailable"),
  });

  const fileQ = useQuery({
    queryKey: ["hooks-file", selected],
    queryFn: () => adminAPI.hooksFileGet(selected!),
    enabled: selected !== null,
    staleTime: Infinity, // only re-fetch on explicit reload — the editor owns the working copy
  });

  // Sync fetched content into the editor on file switch / explicit
  // reload. We guard against overwriting in-progress edits by checking
  // whether the *selected* path matches the file we just loaded.
  useEffect(() => {
    if (fileQ.data && fileQ.data.path === selected) {
      setEditorValue(fileQ.data.content ?? "");
      setSavedValue(fileQ.data.content ?? "");
      setStatus("idle");
      setStatusDetail(null);
    }
  }, [fileQ.data, selected]);

  const saveM = useMutation({
    mutationFn: ({ path, content }: { path: string; content: string }) =>
      adminAPI.hooksFilePut(path, content),
    onMutate: () => {
      setStatus("saving");
      setStatusDetail(null);
    },
    onSuccess: (_data, vars) => {
      setStatus("saved");
      setSavedValue(vars.content);
      // Invalidate the listing so sidebar size/modified columns refresh.
      void qc.invalidateQueries({ queryKey: ["hooks-files"] });
    },
    onError: (err) => {
      setStatus("error");
      setStatusDetail(err instanceof Error ? err.message : String(err));
    },
  });

  const deleteM = useMutation({
    mutationFn: (path: string) => adminAPI.hooksFileDelete(path),
    onSuccess: (_data, path) => {
      void qc.invalidateQueries({ queryKey: ["hooks-files"] });
      if (selected === path) {
        setSelected(null);
        setEditorValue("");
        setSavedValue("");
      }
    },
  });

  // Debounced auto-save. We only schedule the PUT if the current
  // editor value diverges from the last-saved snapshot — switching
  // files clears the debounce naturally because savedValue updates
  // synchronously in the useEffect above.
  useEffect(() => {
    if (selected === null) return;
    if (editorValue === savedValue) return;
    if (debounceRef.current !== null) {
      window.clearTimeout(debounceRef.current);
    }
    debounceRef.current = window.setTimeout(() => {
      saveM.mutate({ path: selected, content: editorValue });
    }, 800);
    return () => {
      if (debounceRef.current !== null) {
        window.clearTimeout(debounceRef.current);
        debounceRef.current = null;
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editorValue, selected, savedValue]);

  const handleNewFile = useCallback(() => {
    const name = window.prompt(
      "New hook filename (must end in .js; nested paths like sub/foo.js are fine):",
      "on_record_create.js",
    );
    if (!name) return;
    const trimmed = name.trim();
    if (!trimmed.endsWith(".js")) {
      window.alert("Filename must end in .js");
      return;
    }
    // Pre-flight check: refuse if already exists. The PUT would happily
    // overwrite, but the UI should treat "new file" and "open existing"
    // as distinct actions.
    const exists = (listQ.data?.items ?? []).some((f) => f.path === trimmed);
    if (exists) {
      if (!window.confirm(`File "${trimmed}" already exists — open it instead?`)) {
        return;
      }
      setSelected(trimmed);
      return;
    }
    saveM.mutate(
      { path: trimmed, content: NEW_FILE_TEMPLATE },
      {
        onSuccess: () => {
          setSelected(trimmed);
          setEditorValue(NEW_FILE_TEMPLATE);
          setSavedValue(NEW_FILE_TEMPLATE);
        },
      },
    );
  }, [listQ.data, saveM]);

  const handleSelect = useCallback(
    (path: string) => {
      if (selected === path) return;
      // If the user has unsaved changes, the debounce timer will fire
      // for the *previous* selected path (we capture `selected` inside
      // the timeout closure). To avoid losing edits, force-save before
      // switching — but only if there's a divergence.
      if (selected !== null && editorValue !== savedValue) {
        if (debounceRef.current !== null) {
          window.clearTimeout(debounceRef.current);
          debounceRef.current = null;
        }
        saveM.mutate({ path: selected, content: editorValue });
      }
      setSelected(path);
    },
    [selected, editorValue, savedValue, saveM],
  );

  const handleDelete = useCallback(
    (path: string) => {
      if (!window.confirm(`Delete hook "${path}"? This cannot be undone.`)) return;
      deleteM.mutate(path);
    },
    [deleteM],
  );

  const handleReload = useCallback(() => {
    if (selected === null) return;
    if (editorValue !== savedValue) {
      if (!window.confirm("Discard unsaved changes and reload from disk?")) return;
    }
    void qc.invalidateQueries({ queryKey: ["hooks-file", selected] });
  }, [selected, editorValue, savedValue, qc]);

  const handleFormat = useCallback(() => {
    const ed = monacoRef.current;
    if (!ed) return;
    void ed.getAction("editor.action.formatDocument")?.run();
  }, []);

  const onEditorMount: OnMount = useCallback((editor) => {
    monacoRef.current = editor;
  }, []);

  // Unavailable detection: the list endpoint returns 503 with
  // code=unavailable when HooksDir is empty. Surface a typed empty
  // state so operators know to set RAILBASE_HOOKS_DIR.
  const isUnavailable =
    listQ.error instanceof APIError && listQ.error.code === "unavailable";

  if (isUnavailable) {
    return <UnavailableState />;
  }

  return (
    <div className="space-y-4 -m-6 flex flex-col" style={{ height: "calc(100vh - 4rem)" }}>
      <div className="px-6 pt-4">
        <header className="flex items-baseline justify-between">
          <div>
            <h1 className="text-2xl font-semibold">Hooks</h1>
            <p className="text-sm text-neutral-500">
              JavaScript hook files in <span className="rb-mono">pb_hooks/</span>.
              Changes hot-reload within 1s.
            </p>
          </div>
        </header>
      </div>

      <div className="flex flex-1 min-h-0 border-t border-neutral-200">
        <FileTree
          items={listQ.data?.items ?? []}
          loading={listQ.isLoading}
          selected={selected}
          onSelect={handleSelect}
          onNew={handleNewFile}
          onDelete={handleDelete}
        />
        <div className="flex-1 min-w-0 flex flex-col">
          {selected === null ? (
            <EmptyEditorState />
          ) : (
            <>
              <EditorToolbar
                filename={selected}
                status={status}
                statusDetail={statusDetail}
                pending={saveM.isPending}
                onFormat={handleFormat}
                onReload={handleReload}
                dirty={editorValue !== savedValue}
              />
              <div className="flex-1 min-h-0">
                {fileQ.isLoading ? (
                  <div className="p-6 text-sm text-neutral-500">Loading…</div>
                ) : (
                  <Editor
                    value={editorValue}
                    onChange={(v) => setEditorValue(v ?? "")}
                    onMount={onEditorMount}
                    language="javascript"
                    theme="vs-dark"
                    options={{
                      minimap: { enabled: false },
                      fontSize: 13,
                      tabSize: 2,
                      automaticLayout: true,
                      scrollBeyondLastLine: false,
                    }}
                  />
                )}
              </div>
              <TestPanel />
            </>
          )}
        </div>
      </div>
    </div>
  );
}

// ---- Test panel (v1.7.20 §3.4.11) ----
//
// Sits below the editor and lets operators fire the runtime against a
// synthetic record without saving + manually triggering a record op.
// Collapsed by default so it doesn't shrink the editor surface when
// not in use.
//
// State lives in the panel rather than the parent because the panel
// is mounted/unmounted alongside the selected file but its inputs
// (event / collection / record-json) are independent of file content
// — operators frequently fire the same record against multiple hook
// files in sequence. Lifting state to HooksScreen would require
// preserving across file switches; keeping it local is simpler and
// the cost of re-entering inputs after a panel-collapse is minimal.

const TEST_PANEL_EVENTS: HookEventName[] = [
  "BeforeCreate",
  "AfterCreate",
  "BeforeUpdate",
  "AfterUpdate",
  "BeforeDelete",
  "AfterDelete",
];

const DEFAULT_RECORD_JSON = `{
  "id": "rec_abc123",
  "title": "Sample record"
}`;

function TestPanel() {
  const [expanded, setExpanded] = useState(false);
  const [event, setEvent] = useState<HookEventName>("BeforeCreate");
  const [collection, setCollection] = useState("");
  const [recordText, setRecordText] = useState(DEFAULT_RECORD_JSON);
  const [recordError, setRecordError] = useState<string | null>(null);
  const [result, setResult] = useState<HookTestRunResult | null>(null);
  const [requestError, setRequestError] = useState<string | null>(null);

  const runM = useMutation({
    mutationFn: async () => {
      let parsed: Record<string, unknown>;
      try {
        parsed = JSON.parse(recordText);
      } catch (e) {
        throw new Error(
          "Record JSON is invalid: " +
            (e instanceof Error ? e.message : String(e)),
        );
      }
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
        throw new Error("Record JSON must be an object (got array / primitive)");
      }
      return adminAPI.runHookTest({
        event,
        collection,
        record: parsed,
      });
    },
    onMutate: () => {
      setRequestError(null);
      setResult(null);
      setRecordError(null);
    },
    onSuccess: (data) => {
      setResult(data);
    },
    onError: (err) => {
      setRequestError(err instanceof Error ? err.message : String(err));
    },
  });

  const validateRecordOnBlur = useCallback(() => {
    if (recordText.trim() === "") {
      setRecordError(null);
      return;
    }
    try {
      JSON.parse(recordText);
      setRecordError(null);
    } catch (e) {
      setRecordError(e instanceof Error ? e.message : String(e));
    }
  }, [recordText]);

  if (!expanded) {
    return (
      <div className="border-t border-neutral-200 bg-neutral-50 px-4 py-2">
        <button
          type="button"
          onClick={() => setExpanded(true)}
          className="text-xs text-neutral-700 hover:text-neutral-900 flex items-center gap-1"
        >
          <span aria-hidden>▸</span> Test panel
          <span className="text-neutral-500 ml-2">
            Fire a hook against a synthetic record (no DB writes)
          </span>
        </button>
      </div>
    );
  }

  return (
    <div className="border-t border-neutral-200 bg-neutral-50 flex flex-col"
         style={{ maxHeight: "40vh" }}>
      <div className="flex items-center justify-between px-4 py-2 border-b border-neutral-200">
        <button
          type="button"
          onClick={() => setExpanded(false)}
          className="text-xs text-neutral-700 hover:text-neutral-900 flex items-center gap-1"
        >
          <span aria-hidden>▾</span> Test panel
        </button>
        <div className="text-[11px] text-neutral-500">
          Fires the runtime against a synthetic record. No DB side effects.
        </div>
      </div>

      <div className="flex flex-1 min-h-0 overflow-auto">
        {/* Left column: inputs */}
        <div className="w-[50%] p-3 border-r border-neutral-200 space-y-2 overflow-auto">
          <div className="flex items-center gap-2">
            <label className="text-xs font-medium text-neutral-700 w-20">Event</label>
            <select
              value={event}
              onChange={(e) => setEvent(e.target.value as HookEventName)}
              className="flex-1 rounded border border-neutral-300 bg-white px-2 py-1 text-xs"
            >
              {TEST_PANEL_EVENTS.map((ev) => (
                <option key={ev} value={ev}>
                  {ev}
                </option>
              ))}
            </select>
          </div>
          <div className="flex items-center gap-2">
            <label className="text-xs font-medium text-neutral-700 w-20">
              Collection
            </label>
            <input
              type="text"
              value={collection}
              onChange={(e) => setCollection(e.target.value)}
              placeholder='"posts" or empty for wildcard'
              className="flex-1 rb-mono rounded border border-neutral-300 bg-white px-2 py-1 text-xs"
            />
          </div>
          <div>
            <label className="text-xs font-medium text-neutral-700 block mb-1">
              Record JSON
            </label>
            <textarea
              value={recordText}
              onChange={(e) => setRecordText(e.target.value)}
              onBlur={validateRecordOnBlur}
              rows={8}
              spellCheck={false}
              className="w-full rb-mono rounded border border-neutral-300 bg-white px-2 py-1 text-[12px] resize-y"
            />
            {recordError && (
              <div className="text-[11px] text-red-700 mt-1">
                JSON parse error: {recordError}
              </div>
            )}
          </div>
          <div className="flex items-center gap-2 pt-1">
            <button
              type="button"
              onClick={() => runM.mutate()}
              disabled={runM.isPending || recordError !== null}
              className="rounded bg-neutral-900 px-3 py-1 text-xs font-medium text-white hover:bg-neutral-700 disabled:bg-neutral-400"
            >
              {runM.isPending ? "Running…" : "Run test"}
            </button>
            {requestError && (
              <span className="text-[11px] text-red-700">{requestError}</span>
            )}
          </div>
        </div>

        {/* Right column: output */}
        <div className="w-[50%] p-3 overflow-auto">
          {result === null && !runM.isPending && (
            <div className="text-xs text-neutral-500 italic">
              No run yet. Configure the inputs on the left and click{" "}
              <span className="rb-mono">Run test</span>.
            </div>
          )}
          {runM.isPending && (
            <div className="text-xs text-neutral-500">Firing handler…</div>
          )}
          {result !== null && <TestResultPanel result={result} />}
        </div>
      </div>
    </div>
  );
}

function TestResultPanel({ result }: { result: HookTestRunResult }) {
  const pillClass =
    result.outcome === "ok"
      ? "border-emerald-200 bg-emerald-50 text-emerald-700"
      : result.outcome === "rejected"
        ? "border-amber-200 bg-amber-50 text-amber-800"
        : "border-red-200 bg-red-50 text-red-700";

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3">
        <span
          className={
            "rounded border px-2 py-0.5 text-[11px] font-medium uppercase tracking-wide " +
            pillClass
          }
        >
          {result.outcome}
        </span>
        <span className="text-[11px] text-neutral-500">
          {result.duration_ms} ms
        </span>
      </div>
      {result.error && (
        <div className="rounded border border-red-200 bg-red-50 p-2 rb-mono text-[11px] text-red-800 whitespace-pre-wrap">
          {result.error}
        </div>
      )}
      <div>
        <div className="text-[11px] font-medium text-neutral-700 mb-1">
          console ({result.console.length})
        </div>
        {result.console.length === 0 ? (
          <div className="text-[11px] text-neutral-500 italic">(no output)</div>
        ) : (
          <div className="rounded border border-neutral-300 bg-neutral-900 text-neutral-100 p-2 rb-mono text-[11px] space-y-0.5 max-h-40 overflow-auto">
            {result.console.map((line, i) => (
              <div key={i}>{line}</div>
            ))}
          </div>
        )}
      </div>
      <div>
        <div className="text-[11px] font-medium text-neutral-700 mb-1">
          modified_record
        </div>
        <pre className="rounded border border-neutral-300 bg-white p-2 rb-mono text-[11px] overflow-auto max-h-40">
          {JSON.stringify(result.modified_record, null, 2)}
        </pre>
      </div>
    </div>
  );
}

function FileTree({
  items,
  loading,
  selected,
  onSelect,
  onNew,
  onDelete,
}: {
  items: HooksFile[];
  loading: boolean;
  selected: string | null;
  onSelect: (path: string) => void;
  onNew: () => void;
  onDelete: (path: string) => void;
}) {
  // Indent by path depth (counting `/` separators). Display the leaf
  // segment; the parent folder is implied by indentation so the tree
  // stays narrow in the 250-px column.
  const rendered = useMemo(() => {
    return items.map((f) => {
      const depth = f.path.split("/").length - 1;
      const leaf = f.path.split("/").pop() ?? f.path;
      return { ...f, depth, leaf };
    });
  }, [items]);

  return (
    <div className="w-[250px] shrink-0 border-r border-neutral-200 bg-neutral-50 flex flex-col">
      <div className="flex items-center justify-between px-3 py-2 border-b border-neutral-200">
        <span className="text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
          pb_hooks/
        </span>
        <button
          type="button"
          onClick={onNew}
          title="New hook file"
          className="rounded border border-neutral-300 bg-white px-1.5 py-0.5 text-xs text-neutral-700 hover:bg-neutral-100"
        >
          + new
        </button>
      </div>
      <div className="flex-1 overflow-auto py-1">
        {loading ? (
          <div className="px-3 py-2 text-xs text-neutral-500">Loading…</div>
        ) : rendered.length === 0 ? (
          <div className="px-3 py-2 text-xs text-neutral-500">
            No hooks yet. Click <span className="rb-mono">+ new</span> to create one.
          </div>
        ) : (
          rendered.map((f) => {
            const active = f.path === selected;
            return (
              <div
                key={f.path}
                className={
                  "group flex items-center justify-between text-sm pr-2 " +
                  (active
                    ? "bg-neutral-900 text-white"
                    : "text-neutral-700 hover:bg-neutral-200")
                }
              >
                <button
                  type="button"
                  onClick={() => onSelect(f.path)}
                  className="flex-1 text-left px-2 py-1 truncate rb-mono text-[12px]"
                  style={{ paddingLeft: 8 + f.depth * 12 }}
                  title={f.path}
                >
                  {f.leaf}
                </button>
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation();
                    onDelete(f.path);
                  }}
                  title="Delete"
                  className={
                    "opacity-0 group-hover:opacity-100 px-1 text-xs " +
                    (active ? "text-white hover:text-red-200" : "text-neutral-500 hover:text-red-600")
                  }
                >
                  🗑
                </button>
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}

function EditorToolbar({
  filename,
  status,
  statusDetail,
  pending,
  onFormat,
  onReload,
  dirty,
}: {
  filename: string;
  status: SaveStatus;
  statusDetail: string | null;
  pending: boolean;
  onFormat: () => void;
  onReload: () => void;
  dirty: boolean;
}) {
  return (
    <div className="flex items-center justify-between px-4 py-2 border-b border-neutral-200 bg-white">
      <div className="flex items-center gap-3 min-w-0">
        <span className="rb-mono text-sm text-neutral-800 truncate" title={filename}>
          {filename}
        </span>
        <StatusPill status={status} pending={pending} detail={statusDetail} dirty={dirty} />
      </div>
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={onFormat}
          className="rounded border border-neutral-300 bg-white px-2 py-0.5 text-xs text-neutral-700 hover:bg-neutral-100"
        >
          Format
        </button>
        <button
          type="button"
          onClick={onReload}
          title="Re-read the file from disk"
          className="rounded border border-neutral-300 bg-white px-2 py-0.5 text-xs text-neutral-700 hover:bg-neutral-100"
        >
          Reload from disk
        </button>
      </div>
    </div>
  );
}

function StatusPill({
  status,
  pending,
  detail,
  dirty,
}: {
  status: SaveStatus;
  pending: boolean;
  detail: string | null;
  dirty: boolean;
}) {
  // Pending wins over the underlying status — once a PUT is in flight
  // we want "saving…" regardless of whether the last terminal state
  // was saved / error.
  if (pending || status === "saving") {
    return (
      <span className="rounded border border-amber-200 bg-amber-50 px-1.5 py-0.5 text-[11px] text-amber-800">
        saving…
      </span>
    );
  }
  if (status === "error") {
    return (
      <span
        title={detail ?? ""}
        className="rounded border border-red-200 bg-red-50 px-1.5 py-0.5 text-[11px] text-red-700"
      >
        save failed
      </span>
    );
  }
  if (dirty) {
    return (
      <span className="rounded border border-neutral-300 bg-neutral-100 px-1.5 py-0.5 text-[11px] text-neutral-700">
        unsaved
      </span>
    );
  }
  if (status === "saved") {
    return (
      <span className="rounded border border-emerald-200 bg-emerald-50 px-1.5 py-0.5 text-[11px] text-emerald-700">
        saved
      </span>
    );
  }
  return (
    <span className="rounded border border-neutral-200 bg-neutral-50 px-1.5 py-0.5 text-[11px] text-neutral-600">
      idle
    </span>
  );
}

function EmptyEditorState() {
  return (
    <div className="p-6 max-w-2xl">
      <div className="rounded-lg border-2 border-dashed border-neutral-300 bg-neutral-50 p-6">
        <div className="text-sm font-medium text-neutral-700">No file selected.</div>
        <div className="text-xs text-neutral-600 mt-2 leading-relaxed">
          Pick a file from the sidebar, or click{" "}
          <span className="rb-mono">+ new</span> to create one. Files are stored
          on disk in <span className="rb-mono">pb_hooks/</span> and the runtime
          hot-reloads them within ~1 s of every save.
        </div>
        <div className="mt-3 text-xs text-neutral-600">
          <div className="font-medium text-neutral-700 mb-1">Available bindings:</div>
          <ul className="rb-mono space-y-0.5 text-[12px]">
            <li>$app.onRecordBeforeCreate("collection", (e) =&gt; …)</li>
            <li>$app.onRecordAfterCreate / Before|AfterUpdate / Before|AfterDelete</li>
            <li>$app.routerAdd("GET", "/path", (c) =&gt; …)</li>
            <li>$app.cronAdd("id", "cron-expr", () =&gt; …)</li>
            <li>$app.realtime().publish("topic", {`{…}`})</li>
          </ul>
        </div>
      </div>
    </div>
  );
}

function UnavailableState() {
  return (
    <div className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold">Hooks</h1>
        <p className="text-sm text-neutral-500">
          JavaScript hook files in <span className="rb-mono">pb_hooks/</span>.
        </p>
      </header>
      <div className="rounded-lg border-2 border-dashed border-amber-300 bg-amber-50 p-6 max-w-2xl">
        <div className="text-sm font-medium text-amber-900">
          Hooks directory not configured.
        </div>
        <div className="text-xs text-amber-800 mt-2 leading-relaxed">
          The admin API has no <span className="rb-mono">HooksDir</span> wired up.
          Set the <span className="rb-mono">RAILBASE_HOOKS_DIR</span> environment
          variable (or pass <span className="rb-mono">--hooks-dir</span> on the
          CLI) and restart the server. The editor will pick up your
          <span className="rb-mono"> pb_hooks/*.js</span> files on next load.
        </div>
      </div>
    </div>
  );
}

import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { OnMount } from "@monaco-editor/react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import { AdminPage } from "../layout/admin_page";

// Monaco is huge (~3 MB raw / 600 KB gzip on its own — most of admin's
// pre-Preact bundle bulk). Lazy-loading it cuts the initial admin bundle
// dramatically: every screen that ISN'T /hooks downloads zero Monaco
// bytes. Suspense renders the "Loading…" fallback while the chunk is in
// flight; on a slow connection that's a few-hundred-ms visible blink,
// vs slowing the whole admin login by 2-3s without lazy.
const Editor = lazy(() => import("@monaco-editor/react"));
import { APIError, isAPIError } from "../api/client";
import type {
  HooksFile,
  HookEventName,
  HookTestRunResult,
} from "../api/types";
import { Badge } from "@/lib/ui/badge.ui";
import { Button } from "@/lib/ui/button.ui";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/lib/ui/collapsible.ui";
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/lib/ui/form.ui";
import { Input } from "@/lib/ui/input.ui";
import { Textarea } from "@/lib/ui/textarea.ui";

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
    <AdminPage>
    <div className="space-y-4 -m-6 flex flex-col" style={{ height: "calc(100vh - 4rem)" }}>
      <div className="px-6 pt-4">
        <header className="flex items-baseline justify-between">
          <div>
            <h1 className="text-2xl font-semibold">Hooks</h1>
            <p className="text-sm text-muted-foreground">
              JavaScript hook files in <span className="font-mono">pb_hooks/</span>.
              Changes hot-reload within 1s.
            </p>
          </div>
        </header>
      </div>

      <div className="flex flex-1 min-h-0 border-t">
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
                  <div className="p-6 text-sm text-muted-foreground">Loading…</div>
                ) : (
                  <Suspense
                    fallback={
                      <div className="p-6 text-sm text-muted-foreground">
                        Loading editor…
                      </div>
                    }
                  >
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
                  </Suspense>
                )}
              </div>
              <TestPanel />
            </>
          )}
        </div>
      </div>
    </div>
    </AdminPage>
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
//
// v1.7.40 — switched the manual expanded/collapsed toggle to the kit
// <Collapsible>, so the chevron + ARIA wiring come from the primitive.

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

// zod schema for the hook test-runner form. recordJson stays as a
// string in the form (textarea-friendly) and is parsed on submit via
// `.refine` — that gives us "object-shaped JSON or rejection" in one
// step without splitting validation across paths.
const testPanelSchema = z.object({
  event: z.enum([
    "BeforeCreate",
    "AfterCreate",
    "BeforeUpdate",
    "AfterUpdate",
    "BeforeDelete",
    "AfterDelete",
  ]),
  collection: z.string(), // empty = wildcard match, ok
  recordJson: z
    .string()
    .min(1, "Record JSON required")
    .refine(
      (s) => {
        try {
          const parsed = JSON.parse(s);
          return (
            typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
          );
        } catch {
          return false;
        }
      },
      { message: "Must be a valid JSON object" },
    ),
});
type TestPanelValues = z.infer<typeof testPanelSchema>;

function TestPanel() {
  // Transient UI state — NOT in the form schema. expanded toggles the
  // collapsible; result/requestError surface the server response.
  // Form values (event/collection/recordJson) live in RHF.
  const [expanded, setExpanded] = useState(false);
  const [result, setResult] = useState<HookTestRunResult | null>(null);
  const [requestError, setRequestError] = useState<string | null>(null);

  const form = useForm<TestPanelValues>({
    resolver: zodResolver(testPanelSchema),
    defaultValues: {
      event: "BeforeCreate",
      collection: "",
      recordJson: DEFAULT_RECORD_JSON,
    },
    mode: "onSubmit",
  });

  const runM = useMutation({
    mutationFn: async (values: TestPanelValues) => {
      // zod's .refine already proved recordJson is a parseable object,
      // so the JSON.parse here can't throw — but keep defensive try/
      // catch for unexpected refine bypass.
      const parsed = JSON.parse(values.recordJson) as Record<string, unknown>;
      return adminAPI.runHookTest({
        event: values.event,
        collection: values.collection,
        record: parsed,
      });
    },
    onMutate: () => {
      setRequestError(null);
      setResult(null);
    },
    onSuccess: (data) => {
      setResult(data);
    },
    onError: (err) => {
      setRequestError(err instanceof Error ? err.message : String(err));
    },
  });

  return (
    <Collapsible open={expanded} onOpenChange={setExpanded} className="border-t bg-muted">
      <div className="flex items-center justify-between px-4 py-2 border-b">
        <CollapsibleTrigger className="text-xs text-foreground hover:text-foreground flex items-center gap-1">
          <span aria-hidden>{expanded ? "▾" : "▸"}</span> Test panel
          {!expanded ? (
            <span className="text-muted-foreground ml-2">
              Fire a hook against a synthetic record (no DB writes)
            </span>
          ) : null}
        </CollapsibleTrigger>
        {expanded ? (
          <div className="text-[11px] text-muted-foreground">
            Fires the runtime against a synthetic record. No DB side effects.
          </div>
        ) : null}
      </div>

      <CollapsibleContent
        className="flex flex-col"
        // Cap the expanded height so the panel doesn't crowd out the
        // editor when the operator opens it on a small viewport.
        style={{ maxHeight: "40vh" }}
      >
        <div className="flex flex-1 min-h-0 overflow-auto">
          {/* Left column: inputs */}
          <Form {...form}>
            <form
              onSubmit={form.handleSubmit((values) => runM.mutate(values))}
              className="w-[50%] p-3 border-r space-y-2 overflow-auto"
            >
              <FormField
                control={form.control}
                name="event"
                render={({ field }) => (
                  <FormItem className="flex items-center gap-2 space-y-0">
                    <FormLabel className="text-xs w-20 m-0">Event</FormLabel>
                    <FormControl>
                      <select
                        {...field}
                        className="flex-1 h-8 rounded border border-input bg-background px-2 text-xs"
                      >
                        {TEST_PANEL_EVENTS.map((ev) => (
                          <option key={ev} value={ev}>
                            {ev}
                          </option>
                        ))}
                      </select>
                    </FormControl>
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name="collection"
                render={({ field }) => (
                  <FormItem className="flex items-center gap-2 space-y-0">
                    <FormLabel className="text-xs w-20 m-0">Collection</FormLabel>
                    <FormControl>
                      <Input
                        type="text"
                        placeholder='"posts" or empty for wildcard'
                        className="flex-1 h-8 font-mono text-xs"
                        {...field}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name="recordJson"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel className="text-xs block mb-1">Record JSON</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={8}
                        spellcheck={false}
                        className="font-mono text-[12px] resize-y bg-background"
                        {...field}
                      />
                    </FormControl>
                    <FormMessage className="text-[11px]" />
                  </FormItem>
                )}
              />
              <div className="flex items-center gap-2 pt-1">
                <Button
                  type="submit"
                  size="sm"
                  disabled={runM.isPending || form.formState.isSubmitting}
                >
                  {runM.isPending ? "Running…" : "Run test"}
                </Button>
                {requestError && (
                  <span className="text-[11px] text-destructive">{requestError}</span>
                )}
              </div>
            </form>
          </Form>

          {/* Right column: output */}
          <div className="w-[50%] p-3 overflow-auto">
            {result === null && !runM.isPending && (
              <div className="text-xs text-muted-foreground italic">
                No run yet. Configure the inputs on the left and click{" "}
                <span className="font-mono">Run test</span>.
              </div>
            )}
            {runM.isPending && (
              <div className="text-xs text-muted-foreground">Firing handler…</div>
            )}
            {result !== null && <TestResultPanel result={result} />}
          </div>
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

function TestResultPanel({ result }: { result: HookTestRunResult }) {
  // Map outcome → Badge tone. We DON'T use kit Badge variants here
  // because the outcome palette (ok / rejected / error) doesn't map
  // 1:1 onto default / secondary / destructive — emerald for "ok",
  // amber for "rejected" (programmatic refusal, not failure), red for
  // "error" (hook threw). Keep the bespoke colours.
  const pillClass =
    result.outcome === "ok"
      ? "border-primary/40 bg-primary/10 text-primary"
      : result.outcome === "rejected"
        ? "border-input bg-muted text-foreground"
        : "border-destructive/30 bg-destructive/10 text-destructive";

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
        <span className="text-[11px] text-muted-foreground">
          {result.duration_ms} ms
        </span>
      </div>
      {result.error && (
        <div className="rounded border border-destructive/30 bg-destructive/10 p-2 font-mono text-[11px] text-destructive whitespace-pre-wrap">
          {result.error}
        </div>
      )}
      <div>
        <div className="text-[11px] font-medium text-foreground mb-1">
          console ({result.console.length})
        </div>
        {result.console.length === 0 ? (
          <div className="text-[11px] text-muted-foreground italic">(no output)</div>
        ) : (
          <div className="rounded border border-input bg-foreground text-background p-2 font-mono text-[11px] space-y-0.5 max-h-40 overflow-auto">
            {result.console.map((line, i) => (
              <div key={i}>{line}</div>
            ))}
          </div>
        )}
      </div>
      <div>
        <div className="text-[11px] font-medium text-foreground mb-1">
          modified_record
        </div>
        <pre className="rounded border border-input bg-background p-2 font-mono text-[11px] overflow-auto max-h-40">
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
    <div className="w-[250px] shrink-0 border-r bg-muted flex flex-col">
      <div className="flex items-center justify-between px-3 py-2 border-b">
        <span className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
          pb_hooks/
        </span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onNew}
          title="New hook file"
          className="h-6 px-1.5 text-xs"
        >
          + new
        </Button>
      </div>
      <div className="flex-1 overflow-auto py-1">
        {loading ? (
          <div className="px-3 py-2 text-xs text-muted-foreground">Loading…</div>
        ) : rendered.length === 0 ? (
          <div className="px-3 py-2 text-xs text-muted-foreground">
            No hooks yet. Click <span className="font-mono">+ new</span> to create one.
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
                    ? "bg-foreground text-background"
                    : "text-foreground hover:bg-muted")
                }
              >
                <button
                  type="button"
                  onClick={() => onSelect(f.path)}
                  className="flex-1 text-left px-2 py-1 truncate font-mono text-[12px]"
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
                    (active ? "text-background hover:text-destructive-foreground" : "text-muted-foreground hover:text-destructive")
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
    <div className="flex items-center justify-between px-4 py-2 border-b bg-background">
      <div className="flex items-center gap-3 min-w-0">
        <span className="font-mono text-sm text-foreground truncate" title={filename}>
          {filename}
        </span>
        <StatusPill status={status} pending={pending} detail={statusDetail} dirty={dirty} />
      </div>
      <div className="flex items-center gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onFormat}
        >
          Format
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onReload}
          title="Re-read the file from disk"
        >
          Reload from disk
        </Button>
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
      <Badge
        variant="outline"
        className="text-[11px] border-input bg-muted text-foreground"
      >
        saving…
      </Badge>
    );
  }
  if (status === "error") {
    return (
      <Badge
        variant="outline"
        title={detail ?? ""}
        className="text-[11px] border-destructive/30 bg-destructive/10 text-destructive"
      >
        save failed
      </Badge>
    );
  }
  if (dirty) {
    return (
      <Badge variant="secondary" className="text-[11px]">
        unsaved
      </Badge>
    );
  }
  if (status === "saved") {
    return (
      <Badge
        variant="outline"
        className="text-[11px] border-primary/40 bg-primary/10 text-primary"
      >
        saved
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="text-[11px] text-muted-foreground">
      idle
    </Badge>
  );
}

function EmptyEditorState() {
  return (
    <div className="p-6 max-w-2xl">
      <div className="rounded-lg border-2 border-dashed border-input bg-muted p-6">
        <div className="text-sm font-medium text-foreground">No file selected.</div>
        <div className="text-xs text-muted-foreground mt-2 leading-relaxed">
          Pick a file from the sidebar, or click{" "}
          <span className="font-mono">+ new</span> to create one. Files are stored
          on disk in <span className="font-mono">pb_hooks/</span> and the runtime
          hot-reloads them within ~1 s of every save.
        </div>
        <div className="mt-3 text-xs text-muted-foreground">
          <div className="font-medium text-foreground mb-1">Available bindings:</div>
          <ul className="font-mono space-y-0.5 text-[12px]">
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
        <p className="text-sm text-muted-foreground">
          JavaScript hook files in <span className="font-mono">pb_hooks/</span>.
        </p>
      </header>
      <div className="rounded-lg border-2 border-dashed border-input bg-muted p-6 max-w-2xl">
        <div className="text-sm font-medium text-foreground">
          Hooks directory not configured.
        </div>
        <div className="text-xs text-muted-foreground mt-2 leading-relaxed">
          The admin API has no <span className="font-mono">HooksDir</span> wired up.
          Set the <span className="font-mono">RAILBASE_HOOKS_DIR</span> environment
          variable (or pass <span className="font-mono">--hooks-dir</span> on the
          CLI) and restart the server. The editor will pick up your
          <span className="font-mono"> pb_hooks/*.js</span> files on next load.
        </div>
      </div>
    </div>
  );
}

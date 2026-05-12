import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";

// Settings panel — single-screen list + add/edit/delete. The
// underlying _settings table stores arbitrary JSONB values, so the
// UI is generic: a textarea for the value, validated client-side
// with `JSON.parse` before submit.

export function SettingsScreen() {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["settings"], queryFn: () => adminAPI.settingsList() });

  const [draftKey, setDraftKey] = useState("");
  const [draftValue, setDraftValue] = useState('""');
  const [err, setErr] = useState<string | null>(null);

  const setMu = useMutation({
    mutationFn: ({ key, value }: { key: string; value: unknown }) =>
      adminAPI.settingsSet(key, value),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["settings"] }),
  });
  const delMu = useMutation({
    mutationFn: (key: string) => adminAPI.settingsDelete(key),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["settings"] }),
  });

  function submitDraft() {
    setErr(null);
    if (!draftKey.trim()) {
      setErr("key is required");
      return;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(draftValue);
    } catch {
      setErr("value must be valid JSON (string, number, bool, object, etc.)");
      return;
    }
    setMu.mutate(
      { key: draftKey.trim(), value: parsed },
      {
        onSuccess: () => {
          setDraftKey("");
          setDraftValue('""');
        },
        onError: (e) => setErr(isAPIError(e) ? e.message : "Failed to save."),
      },
    );
  }

  return (
    <div className="space-y-6 max-w-3xl">
      <header>
        <h1 className="text-2xl font-semibold">Settings</h1>
        <p className="text-sm text-neutral-500">
          Key/value entries persisted in <code className="rb-mono">_settings</code>.
          Values are arbitrary JSON.
        </p>
      </header>

      <section className="rounded border border-neutral-200 bg-white">
        <header className="border-b border-neutral-200 px-4 py-2 text-sm font-medium">
          Add or update a key
        </header>
        <div className="p-4 space-y-3">
          <label className="block">
            <span className="text-sm text-neutral-700">Key</span>
            <input
              type="text"
              value={draftKey}
              onChange={(e) => setDraftKey(e.target.value)}
              placeholder="feature.dark_mode"
              className="mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm rb-mono"
            />
          </label>
          <label className="block">
            <span className="text-sm text-neutral-700">Value (JSON)</span>
            <textarea
              value={draftValue}
              onChange={(e) => setDraftValue(e.target.value)}
              rows={4}
              className="mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm rb-mono"
            />
          </label>
          {err ? (
            <p className="text-sm text-red-700 bg-red-50 border border-red-200 rounded px-3 py-2">
              {err}
            </p>
          ) : null}
          <button
            type="button"
            onClick={submitDraft}
            disabled={setMu.isPending}
            className="rounded bg-neutral-900 text-white px-3 py-2 text-sm font-medium hover:bg-neutral-800 disabled:opacity-50"
          >
            {setMu.isPending ? "Saving…" : "Save"}
          </button>
        </div>
      </section>

      <section className="rounded border border-neutral-200 bg-white">
        <header className="border-b border-neutral-200 px-4 py-2 text-sm font-medium">
          Current settings
        </header>
        {list.isLoading ? (
          <p className="px-4 py-3 text-sm text-neutral-500">Loading…</p>
        ) : (
          <table className="rb-table">
            <thead>
              <tr>
                <th>key</th>
                <th>value</th>
                <th className="w-32"></th>
              </tr>
            </thead>
            <tbody>
              {(list.data?.items ?? []).map((row) => (
                <tr key={row.key}>
                  <td className="rb-mono">{row.key}</td>
                  <td>
                    <pre className="rb-mono text-xs whitespace-pre-wrap break-all">
                      {JSON.stringify(row.value)}
                    </pre>
                  </td>
                  <td>
                    <button
                      type="button"
                      onClick={() => delMu.mutate(row.key)}
                      disabled={delMu.isPending}
                      className="text-xs text-red-700 hover:underline"
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
              {list.data?.items.length === 0 ? (
                <tr>
                  <td colSpan={3} className="text-neutral-400 text-center py-4">
                    No settings yet.
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}

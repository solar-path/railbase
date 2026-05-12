// Shared paginator chip used by the audit / logs / jobs viewers.
//
// Three call sites end up with the same prev/next + "page N / M"
// affordance. v1.7.7 extracted it here so the look stays consistent
// — the previous TODO comments in logs.tsx / audit.tsx flagged the
// duplication.

export function Pager({
  page,
  totalPages,
  onChange,
}: {
  page: number;
  totalPages: number;
  onChange: (p: number) => void;
}) {
  return (
    <div className="flex items-center gap-2 text-sm">
      <button
        type="button"
        disabled={page <= 1}
        onClick={() => onChange(page - 1)}
        className="rounded border border-neutral-300 px-2 py-1 disabled:opacity-30"
      >
        ← prev
      </button>
      <span className="text-neutral-600">
        page {page} / {totalPages}
      </span>
      <button
        type="button"
        disabled={page >= totalPages}
        onClick={() => onChange(page + 1)}
        className="rounded border border-neutral-300 px-2 py-1 disabled:opacity-30"
      >
        next →
      </button>
    </div>
  );
}

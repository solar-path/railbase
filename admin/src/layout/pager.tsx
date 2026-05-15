import { Button } from "@/lib/ui/button.ui";
import { useT } from "../i18n";

// Shared paginator chip used by the audit / logs / jobs viewers.
//
// Three call sites end up with the same prev/next + "page N / M"
// affordance. v1.7.7 extracted it here so the look stays consistent
// — the previous TODO comments in logs.tsx / audit.tsx flagged the
// duplication.
//
// v1.7.40: migrated to the shared kit (`Button` from @/lib/ui), so
// the prev/next chips inherit the same hover/disabled states as every
// other button in the app.

export function Pager({
  page,
  totalPages,
  onChange,
}: {
  page: number;
  totalPages: number;
  onChange: (p: number) => void;
}) {
  const { t } = useT();
  return (
    <div className="flex items-center gap-2 text-sm">
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={page <= 1}
        onClick={() => onChange(page - 1)}
      >
        {t("pager.prev")}
      </Button>
      <span className="text-muted-foreground">
        {t("pager.pageOf", { page, total: totalPages })}
      </span>
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={page >= totalPages}
        onClick={() => onChange(page + 1)}
      >
        {t("pager.next")}
      </Button>
    </div>
  );
}

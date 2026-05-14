import { useT, SUPPORTED_LOCALES } from "../i18n";
import { Check, Globe } from "@/lib/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/lib/ui/dropdown-menu.ui";

// LanguageSwitcher — admin UI language picker, mounted in the Shell
// header next to the ⌘K button. Lists the 10 supported locales by
// endonym; selecting one calls setLocale(), which swaps the dictionary
// signal (re-rendering every useT() consumer), persists the choice to
// localStorage, and updates <html lang/dir>.
export function LanguageSwitcher() {
  const { locale, setLocale, t } = useT();
  const current = SUPPORTED_LOCALES.find((l) => l.code === locale);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          title={t("shell.language")}
          className="flex items-center gap-1 rounded border bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground hover:bg-accent hover:text-accent-foreground"
        >
          <Globe className="size-3" />
          {current?.name ?? locale}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-44">
        {SUPPORTED_LOCALES.map((l) => (
          <DropdownMenuItem
            key={l.code}
            onSelect={() => void setLocale(l.code)}
            className="gap-2"
          >
            <span className="flex-1">{l.name}</span>
            <span className="font-mono text-xs text-muted-foreground">{l.code}</span>
            {l.code === locale ? <Check className="size-4" /> : null}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

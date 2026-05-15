import { useState } from "preact/hooks";
import { Button, Label, Section } from "../lib/ui.js";
import { rb } from "../api.js";
import { userSignal } from "../auth.js";

// appearance.tsx — theme / locale / timezone. These are PROFILE
// fields on your auth collection (declare them in schema/users.go).
// Until you've added them, this section persists them via PATCH /me
// where the backend rejects unknown fields with a clear error — i.e.
// the form will visibly fail until your schema declares e.g.
// `theme TEXT`, `locale TEXT`, `timezone TEXT` columns.
//
// Example schema additions:
//   schemabuilder.NewAuthCollection("users").
//     Field("theme",    schemabuilder.NewSelect("system", "light", "dark")).
//     Field("locale",   schemabuilder.NewText()).
//     Field("timezone", schemabuilder.NewText())

const THEMES = ["system", "light", "dark"] as const;

export function AppearanceSection() {
  const me = userSignal.value;
  const [theme, setTheme] = useState<string>((me?.theme as string) || "system");
  const [locale, setLocale] = useState<string>((me?.locale as string) || "en");
  const [tz, setTz] = useState<string>((me?.timezone as string) || "UTC");
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  const save = async (e: Event) => {
    e.preventDefault();
    setSaving(true);
    setMsg(null);
    try {
      const updated = await rb.account.updateProfile<typeof me>({
        theme, locale, timezone: tz,
      });
      userSignal.value = updated as typeof me;
      setMsg("Saved.");
    } catch (e: unknown) {
      setMsg(e instanceof Error ? e.message : "Save failed (did you add the columns to your auth collection?)");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Section title="Appearance">
      <form onSubmit={save} class="space-y-3">
        <div>
          <Label>Theme</Label>
          <div class="mt-1 flex gap-2">
            {THEMES.map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => setTheme(t)}
                class={`rounded-md border px-3 py-1.5 text-sm capitalize ${
                  theme === t ? "border-slate-900 bg-slate-900 text-white" : "border-slate-300 bg-white"
                }`}
              >
                {t}
              </button>
            ))}
          </div>
        </div>
        <div>
          <Label htmlFor="locale">Locale</Label>
          <input
            id="locale"
            value={locale}
            onInput={(e) => setLocale((e.target as HTMLInputElement).value)}
            class="w-full rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm"
            placeholder="en, ru, de, …"
          />
        </div>
        <div>
          <Label htmlFor="tz">Timezone</Label>
          <input
            id="tz"
            value={tz}
            onInput={(e) => setTz((e.target as HTMLInputElement).value)}
            class="w-full rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm"
            placeholder="Europe/Berlin"
          />
        </div>
        {msg ? <p class="text-xs text-slate-600">{msg}</p> : null}
        <Button type="submit" disabled={saving}>{saving ? "Saving…" : "Save"}</Button>
      </form>
    </Section>
  );
}

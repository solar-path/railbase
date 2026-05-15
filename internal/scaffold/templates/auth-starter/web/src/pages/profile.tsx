import { useState } from "preact/hooks";
import { Button, Input, Label, Section } from "../lib/ui.js";
import { rb } from "../api.js";
import { userSignal } from "../auth.js";

// profile.tsx — display_name + any other declared user fields on the
// auth collection. The minimal scaffold ships with display_name only;
// add more keys to the form below as you add columns to your auth
// collection (avatar_url, phone, etc.) and they'll be PATCH'd by
// rb.account.updateProfile.

export function ProfileSection() {
  const me = userSignal.value;
  const [displayName, setDisplayName] = useState<string>((me?.display_name as string) || "");
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  const save = async (e: Event) => {
    e.preventDefault();
    setSaving(true);
    setMsg(null);
    try {
      const updated = await rb.account.updateProfile<typeof me>({
        display_name: displayName,
      });
      userSignal.value = updated as typeof me;
      setMsg("Saved.");
    } catch (e: unknown) {
      setMsg(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Section title="Profile">
      <form onSubmit={save} class="space-y-3">
        <div>
          <Label htmlFor="display_name">Display name</Label>
          <Input
            id="display_name"
            value={displayName}
            onInput={(e) => setDisplayName((e.target as HTMLInputElement).value)}
          />
        </div>
        {msg ? <p class="text-xs text-slate-600">{msg}</p> : null}
        <Button type="submit" disabled={saving}>
          {saving ? "Saving…" : "Save profile"}
        </Button>
      </form>
    </Section>
  );
}

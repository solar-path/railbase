import { useState } from "preact/hooks";
import { Button, Card } from "../lib/ui.js";
import { ProfileSection } from "./profile.js";
import { SecuritySection } from "./security.js";
import { AppearanceSection } from "./appearance.js";
import { userSignal, signOut } from "../auth.js";

// account.tsx — top-level container for the user's account screen.
// Three tabs mirror air/rail: Profile / Security / Appearance. Each
// tab body is its own component in pages/{profile,security,
// appearance}.tsx for clean splitting.

type Tab = "profile" | "security" | "appearance";

export function AccountPage() {
  const [tab, setTab] = useState<Tab>("profile");
  const me = userSignal.value;
  if (!me) return null; // app.tsx route guard already handles this

  return (
    <div class="mx-auto mt-8 max-w-3xl px-4">
      <div class="mb-4 flex items-center justify-between">
        <h1 class="text-xl font-semibold">Account</h1>
        <Button variant="outline" onClick={() => signOut()}>
          Sign out
        </Button>
      </div>
      <Card class="mb-4">
        <div class="text-sm text-slate-600">Signed in as</div>
        <div class="text-base font-medium">{me.email}</div>
      </Card>
      <div class="mb-4 flex gap-2 border-b border-slate-200">
        {(["profile", "security", "appearance"] as const).map((t) => (
          <button
            key={t}
            class={`-mb-px border-b-2 px-3 py-2 text-sm capitalize transition ${
              tab === t ? "border-slate-900 text-slate-900" : "border-transparent text-slate-500 hover:text-slate-900"
            }`}
            onClick={() => setTab(t)}
          >
            {t}
          </button>
        ))}
      </div>
      {tab === "profile" && <ProfileSection />}
      {tab === "security" && <SecuritySection />}
      {tab === "appearance" && <AppearanceSection />}
    </div>
  );
}

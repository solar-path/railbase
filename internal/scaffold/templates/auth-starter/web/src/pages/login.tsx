import { useState } from "preact/hooks";
import { Button, Card, Input, Label } from "../lib/ui.js";
import { signIn } from "../auth.js";

// login.tsx — minimal email+password signin. Backend: POST
// /api/collections/users/auth-with-password. On 2xx the bearer token
// is persisted by createRailbaseClient automatically (via storage:
// localStorage in api.ts) and userSignal flips to the record; app.tsx
// routes the user to /account.

export function LoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async (e: Event) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await signIn(email, password);
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Sign-in failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="mx-auto mt-16 max-w-sm">
      <Card>
        <h1 class="mb-4 text-lg font-semibold">Sign in</h1>
        <form onSubmit={submit} class="space-y-3">
          <div>
            <Label htmlFor="email">Email</Label>
            <Input
              id="email"
              type="email"
              value={email}
              onInput={(e) => setEmail((e.target as HTMLInputElement).value)}
              required
              autoComplete="email"
            />
          </div>
          <div>
            <Label htmlFor="password">Password</Label>
            <Input
              id="password"
              type="password"
              value={password}
              onInput={(e) => setPassword((e.target as HTMLInputElement).value)}
              required
              autoComplete="current-password"
            />
          </div>
          {err ? <p class="text-xs text-red-600">{err}</p> : null}
          <Button type="submit" disabled={busy} class="w-full">
            {busy ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      </Card>
    </div>
  );
}

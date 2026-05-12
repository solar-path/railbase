import { useState, type FormEvent } from "react";
import { useAuth } from "../auth/context";
import { isAPIError } from "../api/client";

// Login screen — minimal: email + password + sign-in button.
//
// 2FA / WebAuthn / OAuth providers all land in v1.1 (depend on the
// mailer + provider configs). Until then this is the entire admin
// authentication surface.

export function LoginScreen() {
  const { signin } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setErr(null);
    setBusy(true);
    try {
      await signin(email, password);
    } catch (e) {
      setErr(isAPIError(e) ? e.message : "Sign-in failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-neutral-100 p-6">
      <form
        onSubmit={onSubmit}
        className="w-full max-w-sm bg-white rounded-lg shadow border border-neutral-200 p-6 space-y-4"
      >
        <header className="space-y-1">
          <h1 className="text-xl font-semibold">Railbase admin</h1>
          <p className="text-sm text-neutral-500">Sign in to continue.</p>
        </header>

        <label className="block">
          <span className="text-sm font-medium text-neutral-700">Email</span>
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoFocus
            autoComplete="username"
            className="mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-neutral-900"
          />
        </label>

        <label className="block">
          <span className="text-sm font-medium text-neutral-700">Password</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            autoComplete="current-password"
            className="mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-neutral-900"
          />
        </label>

        {err ? (
          <p
            role="alert"
            className="text-sm text-red-700 bg-red-50 border border-red-200 rounded px-3 py-2"
          >
            {err}
          </p>
        ) : null}

        <button
          type="submit"
          disabled={busy}
          className="w-full rounded bg-neutral-900 text-white px-4 py-2 text-sm font-medium hover:bg-neutral-800 disabled:opacity-50"
        >
          {busy ? "Signing in…" : "Sign in"}
        </button>

        <p className="text-xs text-neutral-500">
          No admins yet? Create one with{" "}
          <code className="rb-mono px-1 py-0.5 bg-neutral-100 rounded">
            railbase admin create &lt;email&gt;
          </code>
          {" "}or use the bootstrap wizard.
        </p>
      </form>
    </div>
  );
}

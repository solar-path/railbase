import { useEffect, useState } from "preact/hooks";
import { Button, Input, Label, Section } from "../lib/ui.js";
import { rb } from "../api.js";
import type { Session } from "../_generated/account.js";

// security.tsx — change password + active sessions + 2FA status.
// Each block talks to the v0.4.3 account endpoints:
//   - rb.account.changePassword()        POST /api/auth/change-password
//   - rb.account.listSessions()           GET  /api/auth/sessions
//   - rb.account.revokeSession(id)        DELETE /api/auth/sessions/{id}
//   - rb.account.revokeOtherSessions()    DELETE /api/auth/sessions/others
//   - rb.account.twoFAStatus()            GET  /api/auth/2fa/status
// 2FA mutation endpoints (totpEnrollStart, etc.) live on the
// per-collection auth builder — see ../auth.ts for the convention.

export function SecuritySection() {
  return (
    <div class="space-y-4">
      <ChangePassword />
      <Sessions />
      <TwoFAStatus />
    </div>
  );
}

function ChangePassword() {
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  const submit = async (e: Event) => {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      await rb.account.changePassword({
        current_password: cur,
        new_password: next,
        passwordConfirm: confirm,
      });
      setCur("");
      setNext("");
      setConfirm("");
      setMsg("Password changed. Other sessions have been signed out.");
    } catch (e: unknown) {
      setMsg(e instanceof Error ? e.message : "Change failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section title="Change password">
      <form onSubmit={submit} class="space-y-3">
        <div>
          <Label htmlFor="cur">Current password</Label>
          <Input id="cur" type="password" value={cur} onInput={(e) => setCur((e.target as HTMLInputElement).value)} autoComplete="current-password" required />
        </div>
        <div>
          <Label htmlFor="next">New password</Label>
          <Input id="next" type="password" value={next} onInput={(e) => setNext((e.target as HTMLInputElement).value)} autoComplete="new-password" required />
        </div>
        <div>
          <Label htmlFor="confirm">Confirm new password</Label>
          <Input id="confirm" type="password" value={confirm} onInput={(e) => setConfirm((e.target as HTMLInputElement).value)} autoComplete="new-password" />
        </div>
        {msg ? <p class="text-xs text-slate-600">{msg}</p> : null}
        <Button type="submit" disabled={busy}>{busy ? "Changing…" : "Change password"}</Button>
      </form>
    </Section>
  );
}

function Sessions() {
  const [rows, setRows] = useState<Session[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);

  const load = async () => {
    setLoading(true);
    try {
      setRows(await rb.account.listSessions());
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Failed to load sessions");
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, []);

  const revoke = async (id: string) => {
    try {
      await rb.account.revokeSession(id);
      await load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Revoke failed");
    }
  };
  const rename = async (id: string, current: string | undefined) => {
    const name = window.prompt("Device label", current || "");
    if (name === null) return;
    try {
      await rb.account.updateSession(id, { device_name: name });
      await load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Rename failed");
    }
  };
  const toggleTrust = async (id: string, current: boolean) => {
    try {
      await rb.account.updateSession(id, { is_trusted: !current });
      await load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Toggle failed");
    }
  };
  const revokeOthers = async () => {
    try {
      const { revoked } = await rb.account.revokeOtherSessions();
      setErr(null);
      await load();
      if (revoked > 0) console.log(`signed out from ${revoked} other devices`);
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : "Revoke-others failed");
    }
  };

  return (
    <Section title="Active sessions">
      {loading ? (
        <p class="text-xs text-slate-500">Loading…</p>
      ) : (
        <>
          {err ? <p class="text-xs text-red-600">{err}</p> : null}
          <ul class="divide-y divide-slate-200">
            {rows.map((s) => (
              <li key={s.id} class="flex items-center justify-between py-2 text-sm">
                <div class="min-w-0">
                  <div class="flex items-center gap-2">
                    <span class="truncate font-medium">{s.device_name || "Unlabelled device"}</span>
                    {s.is_trusted ? (
                      <span class="rounded bg-green-100 px-2 py-0.5 text-xs text-green-800">trusted</span>
                    ) : null}
                    {s.current ? (
                      <span class="rounded bg-slate-100 px-2 py-0.5 text-xs">this device</span>
                    ) : null}
                  </div>
                  <div class="font-mono text-xs text-slate-500">
                    {s.ip || "—"} · {s.user_agent || "—"}
                  </div>
                  <div class="text-xs text-slate-400">last active {s.last_active_at}</div>
                </div>
                <div class="flex gap-2">
                  <Button variant="ghost" onClick={() => rename(s.id, s.device_name)}>Rename</Button>
                  <Button variant="ghost" onClick={() => toggleTrust(s.id, s.is_trusted)}>
                    {s.is_trusted ? "Untrust" : "Trust"}
                  </Button>
                  {!s.current ? (
                    <Button variant="ghost" onClick={() => revoke(s.id)}>Revoke</Button>
                  ) : null}
                </div>
              </li>
            ))}
          </ul>
          <div class="mt-3">
            <Button variant="outline" onClick={revokeOthers}>Sign out everywhere else</Button>
          </div>
        </>
      )}
    </Section>
  );
}

function TwoFAStatus() {
  const [enrolled, setEnrolled] = useState<boolean | null>(null);
  useEffect(() => {
    void (async () => {
      try {
        const r = await rb.account.twoFAStatus();
        setEnrolled(r.enrolled);
      } catch {
        setEnrolled(null);
      }
    })();
  }, []);
  return (
    <Section title="Two-factor authentication">
      {enrolled === null ? (
        <p class="text-xs text-slate-500">Checking…</p>
      ) : enrolled ? (
        <p class="text-sm">2FA is <strong>enabled</strong>. Use rb.usersAuth.totpDisable({"{ code }"}) to disable, or totpRegenerateRecoveryCodes() to refresh codes.</p>
      ) : (
        <p class="text-sm">2FA is <strong>off</strong>. Call rb.usersAuth.totpEnrollStart() to get a QR provisioning URI, then totpEnrollConfirm({"{ code }"}) once the user has scanned it.</p>
      )}
    </Section>
  );
}

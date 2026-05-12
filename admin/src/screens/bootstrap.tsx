import { useEffect, useState, type FormEvent } from "react";
import { useLocation } from "wouter";
import { api, isAPIError } from "../api/client";
import { useAuth } from "../auth/context";

// First-run wizard. Reachable when /api/_admin/_bootstrap reports
// `needsBootstrap: true` (LoginGate routes there) OR directly at
// /_/bootstrap.
//
// v1.7.39 split the v0.8 single-step wizard into TWO steps:
//   1. "Database"  — pick a Postgres deployment. Calls the public
//                    /_/setup/{detect,probe-db,save-db} endpoints.
//                    When the operator picks "embedded" we skip
//                    straight to step 2 with no save (the running
//                    process IS embedded; no restart needed).
//                    When they pick local-socket or external we
//                    save the DSN to <DataDir>/.dsn AND tell them to
//                    restart railbase before continuing. The admin
//                    account creation will happen after that restart
//                    (which boots against the real Postgres).
//   2. "Admin"     — existing v0.8 admin-account creation.
//
// The wizard state machine is the small "step" useState below. We
// don't use a router because the wizard is intentionally linear and
// the two steps share a chrome (panel + heading) — flipping the
// rendered component is simpler than maintaining two distinct
// /bootstrap/* routes.

type SocketInfo = { dir: string; path: string; distro: string };
type DetectResponse = {
  configured: boolean;
  // "setup" is returned by production binaries on first boot — embed_pg
  // not compiled in AND no `.dsn` file yet. The setup-mode HTTP server
  // accepts `/_setup/detect` / `/_setup/probe-db` / `/_setup/save-db`
  // and returns 503 for everything else, so the operator MUST complete
  // this step before the admin UI becomes useful.
  current_mode: "embedded" | "external" | "unconfigured" | "setup";
  sockets: SocketInfo[];
  suggested_username: string;
};
type ProbeResponse = {
  ok: boolean;
  dsn?: string;
  version?: string;
  db_exists?: boolean;
  can_create_db?: boolean;
  error?: string;
  hint?: string;
};
type SaveResponse = {
  ok: boolean;
  dsn?: string;
  restart_required: boolean;
  note: string;
};

type DBDriver = "local-socket" | "external" | "embedded";

export function BootstrapScreen() {
  // step 0 = database, step 1 = admin. We start on 0 — the operator
  // sees the database picker first. Auto-advance to step 1 fires when
  // /_setup/detect reports `configured: true` (post in-process reload
  // OR manual restart) AND we haven't already advanced — the operator
  // can always click Back in AdminStep to revisit the DB config.
  const [step, setStep] = useState<0 | 1>(0);
  const [autoAdvanced, setAutoAdvanced] = useState(false);

  // Probe detect once at mount to decide whether to skip step 0.
  // Using a tiny dedicated fetch (not coupled to DatabaseStep's state)
  // keeps the auto-skip decision out of the DatabaseStep render path.
  useEffect(() => {
    if (autoAdvanced) return;
    let cancelled = false;
    fetch("/api/_admin/_setup/detect")
      .then((r) => r.json())
      .then((d: DetectResponse) => {
        if (cancelled) return;
        if (d.configured) {
          setStep(1);
          setAutoAdvanced(true);
        }
      })
      .catch(() => {
        // Detect failures are not fatal — DatabaseStep will surface
        // them when it mounts. We just don't auto-advance.
      });
    return () => {
      cancelled = true;
    };
  }, [autoAdvanced]);

  return (
    <div className="min-h-screen flex items-center justify-center bg-neutral-100 p-6">
      <div className="w-full max-w-2xl space-y-3">
        <Stepper step={step} />
        {step === 0 ? (
          <DatabaseStep onContinue={() => setStep(1)} />
        ) : (
          <AdminStep onBack={() => setStep(0)} />
        )}
      </div>
    </div>
  );
}

function Stepper({ step }: { step: 0 | 1 }) {
  return (
    <ol className="flex items-center gap-3 text-sm text-neutral-500">
      <li className={step === 0 ? "font-semibold text-neutral-900" : ""}>
        1. Database
      </li>
      <li>→</li>
      <li className={step === 1 ? "font-semibold text-neutral-900" : ""}>
        2. Admin account
      </li>
    </ol>
  );
}

// DatabaseStep — calls /_/setup/detect on mount, lets the operator
// pick a driver, exposes Probe + Save controls.
function DatabaseStep({ onContinue }: { onContinue: () => void }) {
  const [detect, setDetect] = useState<DetectResponse | null>(null);
  const [detectErr, setDetectErr] = useState<string | null>(null);

  const [driver, setDriver] = useState<DBDriver>("embedded");
  const [pickedSocket, setPickedSocket] = useState<string>("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [database, setDatabase] = useState("railbase");
  const [sslmode, setSslmode] = useState("disable");
  const [externalDSN, setExternalDSN] = useState("");
  const [createDB, setCreateDB] = useState(false);

  const [probe, setProbe] = useState<ProbeResponse | null>(null);
  const [save, setSave] = useState<SaveResponse | null>(null);
  const [busy, setBusy] = useState<null | "probe" | "save">(null);
  const [err, setErr] = useState<string | null>(null);

  // Detect on mount. The endpoint is public; we use fetch directly
  // rather than api.request because (a) the response shape isn't a
  // typed admin endpoint and (b) we may be reached before the user
  // has any auth state.
  useEffect(() => {
    let cancelled = false;
    fetch("/api/_admin/_setup/detect")
      .then((r) => r.json())
      .then((d: DetectResponse) => {
        if (cancelled) return;
        setDetect(d);
        if (d.sockets.length > 0) {
          setDriver("local-socket");
          setPickedSocket(d.sockets[0].dir);
        } else if (d.current_mode === "setup") {
          // Production binary, no embed_pg, no detected sockets — the
          // embedded driver radio is unavailable in this build. Default
          // to external DSN so the operator isn't pre-selected on a
          // dead option. The radio for embedded is rendered disabled
          // below when current_mode === "setup".
          setDriver("external");
        }
        if (d.suggested_username && !username) {
          setUsername(d.suggested_username);
        }
      })
      .catch((e: unknown) => {
        if (!cancelled) {
          setDetectErr(
            e instanceof Error ? e.message : "Failed to detect local Postgres.",
          );
        }
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function bodyForBackend() {
    return {
      driver,
      socket_dir: pickedSocket,
      username,
      password,
      database,
      sslmode,
      external_dsn: externalDSN,
      create_database: createDB,
    };
  }

  async function doProbe() {
    setErr(null);
    setProbe(null);
    setBusy("probe");
    try {
      const r = await fetch("/api/_admin/_setup/probe-db", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(bodyForBackend()),
      });
      const data = (await r.json()) as ProbeResponse | { error?: { message?: string } };
      if (r.status === 400) {
        const m =
          (data as { error?: { message?: string } }).error?.message ?? "Validation error.";
        setErr(m);
      } else {
        setProbe(data as ProbeResponse);
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Probe failed.");
    } finally {
      setBusy(null);
    }
  }

  async function doSave() {
    setErr(null);
    setSave(null);
    setBusy("save");
    try {
      const r = await fetch("/api/_admin/_setup/save-db", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(bodyForBackend()),
      });
      const data = (await r.json()) as SaveResponse | { error?: { message?: string } };
      if (r.status === 400) {
        const m =
          (data as { error?: { message?: string } }).error?.message ?? "Validation error.";
        setErr(m);
      } else {
        const saveData = data as SaveResponse;
        setSave(saveData);
        // When the server is reloading in-place (setup-mode → normal
        // mode without an operator restart), restart_required is false
        // and we poll readiness from the client. Once the new boot
        // path's /readyz returns 200, the admin SPA reloads itself —
        // /_bootstrap now hits the real handler, currentMode flips to
        // "external", the wizard reopens on the admin-account step.
        if (saveData.ok && saveData.restart_required === false) {
          void waitForReadyThenReload();
        }
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Save failed.");
    } finally {
      setBusy(null);
    }
  }

  // waitForReadyThenReload polls /readyz every 500 ms (cheap — the
  // probe just runs SELECT 1). It tolerates connection refused (the
  // listener is briefly down during the cutover) so the operator
  // doesn't see a spurious error flash. Caps at 30 s; if readiness
  // hasn't returned by then, fall back to a plain reload — the
  // operator can refresh manually if that doesn't catch the new boot.
  async function waitForReadyThenReload() {
    const deadline = Date.now() + 30_000;
    while (Date.now() < deadline) {
      try {
        const r = await fetch("/readyz", { cache: "no-store" });
        if (r.ok) {
          window.location.reload();
          return;
        }
      } catch {
        // listener briefly down — that's expected during the in-process
        // reload. Just keep polling.
      }
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
    // 30s without readiness — reload anyway; admin might just be slow.
    window.location.reload();
  }

  const hasSockets = (detect?.sockets ?? []).length > 0;
  const isEmbedded = driver === "embedded";

  return (
    <form
      onSubmit={(e) => e.preventDefault()}
      className="w-full bg-white rounded-lg shadow border border-neutral-200 p-6 space-y-5"
    >
      <header className="space-y-1">
        <h1 className="text-xl font-semibold">Welcome to Railbase</h1>
        <p className="text-sm text-neutral-500">
          Choose where to store your data. You can change this later by
          editing <code className="rb-mono px-1 py-0.5 bg-neutral-100 rounded">
          &lt;dataDir&gt;/.dsn</code>.
        </p>
      </header>

      {detect?.configured ? (
        <div className="text-sm bg-emerald-50 border border-emerald-200 text-emerald-800 rounded px-3 py-2">
          Database is already configured — running against your external
          PostgreSQL. You can re-run the wizard to change targets, or skip
          straight to <button
            type="button"
            className="underline font-medium"
            onClick={onContinue}
          >admin setup</button>.
        </div>
      ) : null}
      {detectErr ? (
        <p className="text-sm text-red-700 bg-red-50 border border-red-200 rounded px-3 py-2">
          {detectErr}
        </p>
      ) : null}

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium text-neutral-700">
          Database driver
        </legend>
        <DriverRadio
          checked={driver === "local-socket"}
          disabled={!hasSockets}
          onChange={() => setDriver("local-socket")}
          title="Use my local PostgreSQL"
          subtitle={
            hasSockets
              ? `Detected ${detect?.sockets.length} socket${detect && detect.sockets.length === 1 ? "" : "s"}`
              : "No local PostgreSQL detected on this machine"
          }
        />
        <DriverRadio
          checked={driver === "external"}
          onChange={() => setDriver("external")}
          title="Use an external PostgreSQL"
          subtitle="Managed Postgres (Supabase, Neon, RDS, …) or a remote host"
        />
        <DriverRadio
          checked={driver === "embedded"}
          onChange={() => setDriver("embedded")}
          // Embedded postgres is a `-tags embed_pg` build option,
          // intentionally NOT included in release binaries. In
          // setup-mode we surface the row as disabled so the operator
          // sees "this exists but I can't pick it" rather than wonders
          // why their choice silently fails on save.
          disabled={detect?.current_mode === "setup"}
          title={
            detect?.current_mode === "setup"
              ? "Embedded postgres — not available in this build"
              : "Keep embedded (development)"
          }
          subtitle={
            detect?.current_mode === "setup"
              ? "Rebuild with `make build-embed` for the dev-only embedded driver"
              : "Single-machine dev workflow; data lives under pb_data/postgres/"
          }
        />
      </fieldset>

      {driver === "local-socket" && hasSockets ? (
        <div className="space-y-3">
          <div className="space-y-1">
            <span className="text-sm font-medium text-neutral-700">Socket</span>
            <div className="grid grid-cols-1 gap-2">
              {(detect?.sockets ?? []).map((s) => (
                <label
                  key={s.dir}
                  className={`flex items-start gap-2 rounded border px-3 py-2 cursor-pointer ${
                    pickedSocket === s.dir
                      ? "border-neutral-900 bg-neutral-50"
                      : "border-neutral-200"
                  }`}
                >
                  <input
                    type="radio"
                    name="socket"
                    checked={pickedSocket === s.dir}
                    onChange={() => setPickedSocket(s.dir)}
                    className="mt-1"
                  />
                  <span>
                    <span className="block text-sm font-medium">{s.path}</span>
                    <span className="block text-xs text-neutral-500">
                      {s.distro}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <LabeledInput
              label="Username"
              value={username}
              onChange={setUsername}
              autoComplete="off"
            />
            <LabeledInput
              label="Password (optional)"
              type="password"
              value={password}
              onChange={setPassword}
              autoComplete="new-password"
              hint="Leave empty for peer/trust auth (local sockets often don't need a password)."
            />
            <LabeledInput
              label="Database"
              value={database}
              onChange={setDatabase}
            />
            <LabeledInput
              label="sslmode"
              value={sslmode}
              onChange={setSslmode}
            />
          </div>
          <label className="inline-flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={createDB}
              onChange={(e) => setCreateDB(e.target.checked)}
            />
            Create the database if it doesn&apos;t exist
          </label>
        </div>
      ) : null}

      {driver === "external" ? (
        <div className="space-y-2">
          <label className="block">
            <span className="text-sm font-medium text-neutral-700">
              DSN
            </span>
            <input
              type="text"
              value={externalDSN}
              onChange={(e) => setExternalDSN(e.target.value)}
              placeholder="postgres://user:password@host:5432/dbname?sslmode=require"
              className="mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm font-mono"
            />
            <span className="text-xs text-neutral-500">
              Must start with <code className="rb-mono">postgres://</code> or
              <code className="rb-mono ml-1">postgresql://</code>.
            </span>
          </label>
        </div>
      ) : null}

      {driver === "embedded" ? (
        <div className="text-sm bg-amber-50 border border-amber-200 text-amber-900 rounded px-3 py-2 space-y-1">
          <p>
            OK to keep developing locally with embedded postgres. Data lives
            under <code className="rb-mono">&lt;dataDir&gt;/postgres/</code>.
          </p>
          <p className="text-xs text-amber-700">
            Not recommended for production — embedded postgres is dev-only.
            You can re-run the wizard after deploying to point at a managed
            Postgres without losing application schema (Railbase migrations
            are re-applied on first boot).
          </p>
        </div>
      ) : null}

      {probe ? (
        <ProbeResult probe={probe} />
      ) : null}
      {save ? (
        <SaveResult save={save} />
      ) : null}
      {err ? (
        <p className="text-sm text-red-700 bg-red-50 border border-red-200 rounded px-3 py-2">
          {err}
        </p>
      ) : null}

      <div className="flex items-center gap-2 pt-2 border-t border-neutral-100">
        {isEmbedded ? (
          // "Keep embedded" path is only reachable when embed_pg is
          // compiled in (driver radio is disabled in setup-mode). The
          // operator is staying on the current process's BD; admin
          // create is fine without a restart.
          <button
            type="button"
            onClick={onContinue}
            className="rounded bg-neutral-900 text-white px-4 py-2 text-sm font-medium hover:bg-neutral-800"
          >
            Continue to admin setup →
          </button>
        ) : (
          <>
            <button
              type="button"
              disabled={busy !== null || save?.ok === true}
              onClick={doProbe}
              className="rounded border border-neutral-300 bg-white px-3 py-2 text-sm hover:bg-neutral-50 disabled:opacity-50"
            >
              {busy === "probe" ? "Probing…" : "Probe connection"}
            </button>
            <button
              type="button"
              disabled={busy !== null || save?.ok === true}
              onClick={doSave}
              className="rounded bg-neutral-900 text-white px-3 py-2 text-sm font-medium hover:bg-neutral-800 disabled:opacity-50"
            >
              {busy === "save" ? "Saving…" : "Save and restart later"}
            </button>
            {/*
              Once save succeeded, the new DSN is on disk but the current
              process is still bound to whatever DB it booted with
              (setup-mode = no DB, embedded = throwaway dev cluster). An
              admin created NOW lands in the wrong place — empty after
              the restart in setup-mode, or in the old embedded cluster
              that gets shadowed by the new DSN. So we hide the Continue
              button entirely and tell the operator to restart. After
              the restart, /_bootstrap reports needsBootstrap=true with
              currentMode=external, the wizard reopens, the database
              step short-circuits (`configured: true`), and the operator
              lands on the admin step on a fresh process bound to the
              real DB.
            */}
          </>
        )}
      </div>

      {save?.ok && !isEmbedded && save.restart_required === false ? (
        // In-process reload path: server is about to swap from
        // setup-mode to the full boot path on the new DSN. We poll
        // /readyz in the background and reload the page as soon as the
        // new server is up — usually under 2s on a local socket.
        <div className="mt-3 rounded border border-emerald-300 bg-emerald-50 px-3 py-3 text-sm text-emerald-900">
          <strong className="block mb-1 flex items-center gap-2">
            <span className="inline-block h-3 w-3 rounded-full bg-emerald-500 animate-pulse" />
            Reloading on your new database…
          </strong>
          <span className="block">
            The server is applying migrations and restarting in-place.
            This page will refresh automatically once it's ready — no
            terminal commands needed.
          </span>
        </div>
      ) : null}

      {save?.ok && !isEmbedded && save.restart_required === true ? (
        // Manual-restart path: kept as fallback for the rare case the
        // backend can't trigger an in-process reload (e.g. invoked
        // from a normal-boot wizard re-run where the chan is nil).
        <div className="mt-3 rounded border border-amber-300 bg-amber-50 px-3 py-3 text-sm text-amber-900">
          <strong className="block mb-1">Restart railbase to continue.</strong>
          <span className="block">
            The configuration is saved. Re-run the process to pick up
            the new DSN.
          </span>
          <code className="mt-2 block whitespace-pre rounded bg-amber-100 px-2 py-1 text-xs text-amber-900">
            {`# Ctrl-C in the terminal, then:\n./railbase serve`}
          </code>
        </div>
      ) : null}
    </form>
  );
}

function DriverRadio({
  checked,
  disabled,
  onChange,
  title,
  subtitle,
}: {
  checked: boolean;
  disabled?: boolean;
  onChange: () => void;
  title: string;
  subtitle: string;
}) {
  return (
    <label
      className={`flex items-start gap-2 rounded border px-3 py-2 ${
        disabled
          ? "border-neutral-200 bg-neutral-50 opacity-60 cursor-not-allowed"
          : checked
          ? "border-neutral-900 bg-neutral-50 cursor-pointer"
          : "border-neutral-200 cursor-pointer"
      }`}
    >
      <input
        type="radio"
        name="driver"
        checked={checked}
        disabled={disabled}
        onChange={onChange}
        className="mt-1"
      />
      <span>
        <span className="block text-sm font-medium">{title}</span>
        <span className="block text-xs text-neutral-500">{subtitle}</span>
      </span>
    </label>
  );
}

function LabeledInput({
  label,
  value,
  onChange,
  type,
  autoComplete,
  hint,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  autoComplete?: string;
  hint?: string;
}) {
  return (
    <label className="block">
      <span className="text-sm font-medium text-neutral-700">{label}</span>
      <input
        type={type ?? "text"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        autoComplete={autoComplete}
        className="mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm"
      />
      {hint ? (
        <span className="block text-xs text-neutral-500 mt-1">{hint}</span>
      ) : null}
    </label>
  );
}

function ProbeResult({ probe }: { probe: ProbeResponse }) {
  if (probe.ok) {
    return (
      <div className="text-sm bg-emerald-50 border border-emerald-200 text-emerald-800 rounded px-3 py-2 space-y-1">
        <p className="font-medium">Connection OK.</p>
        {probe.version ? (
          <p className="font-mono text-xs">{probe.version}</p>
        ) : null}
        {probe.dsn ? (
          <p className="text-xs">
            DSN: <code className="rb-mono">{probe.dsn}</code>
          </p>
        ) : null}
      </div>
    );
  }
  return (
    <div className="text-sm bg-red-50 border border-red-200 text-red-800 rounded px-3 py-2 space-y-1">
      <p className="font-medium">Connection failed.</p>
      {probe.error ? <p className="font-mono text-xs">{probe.error}</p> : null}
      {probe.hint ? <p className="text-xs">Hint: {probe.hint}</p> : null}
    </div>
  );
}

function SaveResult({ save }: { save: SaveResponse }) {
  if (!save.ok) {
    return (
      <div className="text-sm bg-red-50 border border-red-200 text-red-800 rounded px-3 py-2">
        <p className="font-medium">Save failed.</p>
        <p className="text-xs">{save.note}</p>
      </div>
    );
  }
  return (
    <div className="text-sm bg-emerald-50 border border-emerald-200 text-emerald-800 rounded px-3 py-2 space-y-1">
      <p className="font-medium">Configuration saved.</p>
      <p className="text-xs">{save.note}</p>
      {save.restart_required ? (
        <p className="text-xs">
          After saving, restart railbase. The next boot will use your
          PostgreSQL instead of embedded.
        </p>
      ) : null}
    </div>
  );
}

// AdminStep is the v0.8 admin-account form, lifted out of the original
// BootstrapScreen body. Unchanged behaviour — same POST /_bootstrap
// payload, same redirect to /.
function AdminStep({ onBack }: { onBack: () => void }) {
  const { refresh } = useAuth();
  const [, navigate] = useLocation();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setErr(null);
    if (password !== confirm) {
      setErr("Passwords do not match.");
      return;
    }
    if (password.length < 8) {
      setErr("Password must be at least 8 characters.");
      return;
    }
    setBusy(true);
    try {
      const r = await api.request<{ token: string; record: { id: string } }>(
        "POST",
        "/_bootstrap",
        { body: { email, password } },
      );
      api.setToken(r.token);
      await refresh();
      navigate("/");
    } catch (e) {
      setErr(isAPIError(e) ? e.message : "Bootstrap failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={onSubmit}
      className="w-full bg-white rounded-lg shadow border border-neutral-200 p-6 space-y-4"
    >
      <header className="space-y-1">
        <h1 className="text-xl font-semibold">Create the first admin</h1>
        <p className="text-sm text-neutral-500">
          Subsequent admins are created via{" "}
          <code className="rb-mono px-1 py-0.5 bg-neutral-100 rounded">
            railbase admin create
          </code>
          .
        </p>
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
          className="mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm"
        />
      </label>

      <label className="block">
        <span className="text-sm font-medium text-neutral-700">Password</span>
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
          minLength={8}
          autoComplete="new-password"
          className="mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm"
        />
        <span className="text-xs text-neutral-500">Minimum 8 characters.</span>
      </label>

      <label className="block">
        <span className="text-sm font-medium text-neutral-700">
          Confirm password
        </span>
        <input
          type="password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          required
          autoComplete="new-password"
          className="mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm"
        />
      </label>

      {err ? (
        <p className="text-sm text-red-700 bg-red-50 border border-red-200 rounded px-3 py-2">
          {err}
        </p>
      ) : null}

      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={onBack}
          className="rounded border border-neutral-300 bg-white px-3 py-2 text-sm hover:bg-neutral-50"
        >
          ← Back
        </button>
        <button
          type="submit"
          disabled={busy}
          className="rounded bg-neutral-900 text-white px-4 py-2 text-sm font-medium hover:bg-neutral-800 disabled:opacity-50"
        >
          {busy ? "Creating…" : "Create admin & sign in"}
        </button>
      </div>
    </form>
  );
}

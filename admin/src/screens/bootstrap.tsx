import { useEffect, useState } from "react";
import { useLocation } from "wouter-preact";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { api, isAPIError } from "../api/client";
import { useAuth } from "../auth/context";
import { Button } from "@/lib/ui/button.ui";
import { Card } from "@/lib/ui/card.ui";
import { Checkbox } from "@/lib/ui/checkbox.ui";
import { Input } from "@/lib/ui/input.ui";
import { PasswordInput } from "@/lib/ui/password.ui";
import { RadioGroup, RadioGroupItem } from "@/lib/ui/radio-group.ui";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/lib/ui/form.ui";

// First-run wizard. Reachable when /api/_admin/_bootstrap reports
// `needsBootstrap: true` (LoginGate routes there) OR directly at
// /_/bootstrap.
//
// v1.7.39 split the v0.8 single-step wizard into TWO steps:
//   1. "Database"  — pick a Postgres deployment. Calls the public
//                    /_/setup/{detect,probe-db,save-db} endpoints.
//                    Saves the DSN to <DataDir>/.dsn, then either
//                    triggers an in-process reload (production fast
//                    path) or asks for a manual restart (fallback).
//   2. "Admin"     — admin-account creation.
//
// v1.7.41-followup removed the `embedded` driver radio entirely:
// embedded postgres is a build-time decision (`-tags embed_pg`)
// shipped via `make build-embed` as a separate dev binary that boots
// directly into embedded mode without reaching this wizard. Production
// binaries no longer surface a disabled "embedded — not available"
// row that confuses operators.
//
// v1.7.41 migrates both steps to the kit's <Form> + react-hook-form +
// zod pattern (see login.tsx for the reference). Each step owns its
// OWN useForm() — schemas, defaults, and submit targets diverge enough
// that one mega-form would be more friction than two co-mounted ones.
// Transient UI state (probe banners, save banners, step navigation,
// detect result) stays on plain useState — it isn't form data.

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
  // v1.7.42 foreign-DB safety scan. See setup_db.go::setupProbeResponse
  // docstring for the decision matrix.
  public_table_count?: number;
  is_existing_railbase?: boolean;
  error?: string;
  hint?: string;
};
type SaveResponse = {
  ok: boolean;
  dsn?: string;
  restart_required: boolean;
  note: string;
};

// Discriminated union by driver. Each branch carries only the fields
// the backend will actually consume for that driver — the type system
// (plus form.watch("driver")) drives which fields render. The "local_socket"
// branch keeps `password` optional because peer/trust auth on local
// sockets often does without one.
const dbStepSchema = z.discriminatedUnion("driver", [
  z.object({
    driver: z.literal("local_socket"),
    socket_dir: z.string().min(1, "Pick a socket"),
    username: z.string().min(1, "Username required"),
    password: z.string().optional(),
    database: z.string().min(1, "Database name required"),
    sslmode: z.enum(["disable", "require", "prefer"]),
    create_db: z.boolean(),
  }),
  z.object({
    driver: z.literal("external_dsn"),
    external_dsn: z
      .string()
      .regex(/^postgres(ql)?:\/\//, "Must start with postgres://"),
  }),
  // NOTE: the `embedded` driver is intentionally NOT in this union.
  // Embedded postgres is a build-time decision (`-tags embed_pg`), not
  // an operator-time decision — `make build-embed` produces a separate
  // dev binary that boots directly into embedded mode without ever
  // reaching this wizard. Production binaries don't ship the embed_pg
  // driver, so surfacing it here (even disabled) is misleading UX.
]);

type DBStepValues = z.infer<typeof dbStepSchema>;

const adminStepSchema = z
  .object({
    email: z.string().email("Valid email required"),
    password: z
      .string()
      .min(8, "Min 8 characters")
      .regex(/[A-Z]/, "Need uppercase")
      .regex(/[0-9]/, "Need digit")
      .regex(/[^A-Za-z0-9]/, "Need symbol"),
    confirm: z.string(),
  })
  // Cross-field validation: zod's .refine + path: ["confirm"] surfaces
  // the error under the confirm field's <FormMessage> automatically.
  .refine((d) => d.password === d.confirm, {
    message: "Passwords don't match",
    path: ["confirm"],
  });

type AdminStepValues = z.infer<typeof adminStepSchema>;

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
    <div className="min-h-screen flex items-center justify-center bg-muted p-6">
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
    <ol className="flex items-center gap-3 text-sm text-muted-foreground">
      <li className={step === 0 ? "font-semibold text-foreground" : ""}>
        1. Database
      </li>
      <li>→</li>
      <li className={step === 1 ? "font-semibold text-foreground" : ""}>
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

  const [probe, setProbe] = useState<ProbeResponse | null>(null);
  const [save, setSave] = useState<SaveResponse | null>(null);
  const [busy, setBusy] = useState<null | "probe" | "save">(null);
  const [err, setErr] = useState<string | null>(null);
  // Foreign-DB acknowledgement. When the most recent probe found
  // non-Railbase tables in the target DB, the Save button is locked
  // until the operator explicitly opts in. Reset on every new probe
  // so a "fix the database name → re-probe" flow doesn't leak the
  // acknowledgement from the previous (dangerous) target.
  const [proceedAnyway, setProceedAnyway] = useState(false);

  // Default to "external_dsn" with an empty string — overridden after
  // detect if local sockets exist (preferred for ops on their own
  // box; we still default to external_dsn until detect completes so
  // the radio doesn't flicker between options). zodResolver is fine
  // with the discriminated union: defaultValues only carries the
  // external_dsn branch initially and we reset() into the other
  // branches when the operator picks a different driver.
  const form = useForm<DBStepValues>({
    resolver: zodResolver(dbStepSchema),
    defaultValues: { driver: "external_dsn", external_dsn: "" } as DBStepValues,
    mode: "onSubmit",
  });

  const driver = form.watch("driver");

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
          form.reset({
            driver: "local_socket",
            socket_dir: d.sockets[0].dir,
            username: d.suggested_username ?? "",
            password: "",
            database: "railbase",
            sslmode: "disable",
            create_db: false,
          });
        }
        // No sockets → default external_dsn stays selected. We don't
        // need a special-case branch for current_mode === "setup"
        // anymore because embedded is no longer offered.
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

  // The backend payload predates the discriminated union: it expects a
  // single flat object with driver name + all per-driver fields, where
  // unused branches stay empty. Re-flatten the validated form values
  // into that shape. Driver names also map back to the hyphenated
  // wire format the server still accepts.
  function bodyForBackend(values: DBStepValues) {
    if (values.driver === "local_socket") {
      return {
        driver: "local-socket",
        socket_dir: values.socket_dir,
        username: values.username,
        password: values.password ?? "",
        database: values.database,
        sslmode: values.sslmode,
        external_dsn: "",
        create_database: values.create_db,
      };
    }
    // values.driver === "external_dsn" — only other variant in the
    // union after removing `embedded`. TypeScript's exhaustiveness
    // check makes the cast unnecessary, but keep the explicit
    // narrowing for readability.
    return {
      driver: "external",
      socket_dir: "",
      username: "",
      password: "",
      database: "",
      sslmode: "",
      external_dsn: values.external_dsn,
      create_database: false,
    };
  }

  // Driver radio is a click action on the FORM (not a submit) — switch
  // discriminated-union branch by calling form.reset with the right
  // defaults for that branch. We can't just setValue("driver", …)
  // because the other branch fields would still be invalid/undefined.
  function switchDriver(next: DBStepValues["driver"]) {
    if (next === "local_socket") {
      const first = detect?.sockets[0];
      form.reset({
        driver: "local_socket",
        socket_dir: first?.dir ?? "",
        username: detect?.suggested_username ?? "",
        password: "",
        database: "railbase",
        sslmode: "disable",
        create_db: false,
      });
    } else {
      // next === "external_dsn" — the only other variant in the union.
      form.reset({ driver: "external_dsn", external_dsn: "" });
    }
  }

  // Probe is an action on current form values — explicitly validate
  // first so we don't ship garbage to the backend, then call the
  // probe-db endpoint. Result lives in transient probe state (not form
  // state) — it's a banner above the buttons, not a field.
  async function onProbe() {
    setErr(null);
    setProbe(null);
    setProceedAnyway(false);
    const valid = await form.trigger();
    if (!valid) return;
    const values = form.getValues();
    setBusy("probe");
    try {
      const r = await fetch("/api/_admin/_setup/probe-db", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(bodyForBackend(values)),
      });
      const data = (await r.json()) as
        | ProbeResponse
        | { error?: { message?: string } };
      if (r.status === 400) {
        const m =
          (data as { error?: { message?: string } }).error?.message ??
          "Validation error.";
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

  // Foreign-DB detection:
  //
  //   - foreignDb=true  → probe succeeded, found tables, no marker.
  //                       Save is locked until proceedAnyway=true.
  //   - existingRailbase → probe succeeded, found marker. Green banner;
  //                       Save flows normally.
  //
  // We treat undefined/missing fields as "scan not available" rather
  // than "DB is empty" so backends pre-v1.7.42 (or where the catalog
  // scan errored silently) don't accidentally bypass the gate when
  // their old shape gets cached client-side.
  const foreignDb =
    probe?.ok === true &&
    (probe.public_table_count ?? 0) > 0 &&
    probe.is_existing_railbase === false;
  const existingRailbase =
    probe?.ok === true && probe.is_existing_railbase === true;
  const saveLocked = foreignDb && !proceedAnyway;

  async function onSubmit(values: DBStepValues) {
    setErr(null);
    setSave(null);
    setBusy("save");
    try {
      const r = await fetch("/api/_admin/_setup/save-db", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(bodyForBackend(values)),
      });
      const data = (await r.json()) as
        | SaveResponse
        | { error?: { message?: string } };
      if (r.status === 400) {
        const m =
          (data as { error?: { message?: string } }).error?.message ??
          "Validation error.";
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

  return (
    <Card className="p-6">
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-5">
          <header className="space-y-1">
            <h1 className="text-xl font-semibold">Welcome to Railbase</h1>
            <p className="text-sm text-muted-foreground">
              Choose where to store your data. You can change this later by
              editing{" "}
              <code className="rb-mono px-1 py-0.5 bg-muted rounded">
                &lt;dataDir&gt;/.dsn
              </code>
              .
            </p>
          </header>

          {detect?.configured ? (
            <div className="text-sm bg-emerald-50 border border-emerald-200 text-emerald-800 rounded px-3 py-2">
              Database is already configured — running against your external
              PostgreSQL. You can re-run the wizard to change targets, or skip
              straight to{" "}
              <Button
                type="button"
                variant="link"
                size="sm"
                onClick={onContinue}
                className="h-auto p-0 underline font-medium"
              >
                admin setup
              </Button>
              .
            </div>
          ) : null}
          {detectErr ? (
            <p className="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2">
              {detectErr}
            </p>
          ) : null}

          <FormField
            control={form.control}
            name="driver"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Database driver</FormLabel>
                <FormControl>
                  <RadioGroup
                    value={field.value}
                    onValueChange={(v) => {
                      // Defensive: ignore local_socket when no sockets
                      // were detected — keyboard activation could
                      // otherwise bypass the visual `disabled` cue on
                      // the RadioGroupItem.
                      if (v === "local_socket" && !hasSockets) return;
                      switchDriver(v as DBStepValues["driver"]);
                    }}
                    className="gap-2"
                  >
                    <DriverRadio
                      value="local_socket"
                      checked={field.value === "local_socket"}
                      disabled={!hasSockets}
                      title="Use my local PostgreSQL"
                      subtitle={
                        hasSockets
                          ? `Detected ${detect?.sockets.length} socket${detect && detect.sockets.length === 1 ? "" : "s"}`
                          : "No local PostgreSQL detected on this machine"
                      }
                    />
                    <DriverRadio
                      value="external_dsn"
                      checked={field.value === "external_dsn"}
                      title="Use an external PostgreSQL"
                      subtitle="Managed Postgres (Supabase, Neon, RDS, …) or a remote host"
                    />
                  </RadioGroup>
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          {driver === "local_socket" && hasSockets ? (
            <div className="space-y-3">
              <FormField
                control={form.control}
                name="socket_dir"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Socket</FormLabel>
                    <FormControl>
                      <RadioGroup
                        value={field.value}
                        onValueChange={field.onChange}
                        className="grid grid-cols-1 gap-2"
                      >
                        {(detect?.sockets ?? []).map((s) => (
                          <SocketRadio
                            key={s.dir}
                            value={s.dir}
                            checked={field.value === s.dir}
                            path={s.path}
                            distro={s.distro}
                          />
                        ))}
                      </RadioGroup>
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <div className="grid grid-cols-2 gap-3">
                <FormField
                  control={form.control}
                  name="username"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Username</FormLabel>
                      <FormControl>
                        <Input
                          type="text"
                          autoComplete="off"
                          value={field.value ?? ""}
                          onInput={(e) =>
                            field.onChange(e.currentTarget.value)
                          }
                          onBlur={field.onBlur}
                          name={field.name}
                          ref={field.ref}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="password"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Password (optional)</FormLabel>
                      <FormControl>
                        <Input
                          type="password"
                          autoComplete="new-password"
                          value={field.value ?? ""}
                          onInput={(e) =>
                            field.onChange(e.currentTarget.value)
                          }
                          onBlur={field.onBlur}
                          name={field.name}
                          ref={field.ref}
                        />
                      </FormControl>
                      <FormDescription>
                        Leave empty for peer/trust auth (local sockets often
                        don&apos;t need a password).
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="database"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Database</FormLabel>
                      <FormControl>
                        <Input
                          type="text"
                          value={field.value ?? ""}
                          onInput={(e) =>
                            field.onChange(e.currentTarget.value)
                          }
                          onBlur={field.onBlur}
                          name={field.name}
                          ref={field.ref}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="sslmode"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>sslmode</FormLabel>
                      <FormControl>
                        <Input
                          type="text"
                          value={field.value ?? ""}
                          onInput={(e) =>
                            field.onChange(e.currentTarget.value)
                          }
                          onBlur={field.onBlur}
                          name={field.name}
                          ref={field.ref}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>

              <FormField
                control={form.control}
                name="create_db"
                render={({ field }) => (
                  <FormItem>
                    <label className="inline-flex items-center gap-2 text-sm cursor-pointer">
                      <FormControl>
                        <Checkbox
                          checked={field.value === true}
                          onCheckedChange={(v) => field.onChange(v === true)}
                        />
                      </FormControl>
                      Create the database if it doesn&apos;t exist
                    </label>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          ) : null}

          {driver === "external_dsn" ? (
            <FormField
              control={form.control}
              name="external_dsn"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>DSN</FormLabel>
                  <FormControl>
                    <Input
                      type="text"
                      value={field.value ?? ""}
                      onInput={(e) => field.onChange(e.currentTarget.value)}
                      onBlur={field.onBlur}
                      name={field.name}
                      ref={field.ref}
                      placeholder="postgres://user:password@host:5432/dbname?sslmode=require"
                      className="font-mono text-sm"
                    />
                  </FormControl>
                  <FormDescription>
                    Must start with{" "}
                    <code className="rb-mono">postgres://</code> or
                    <code className="rb-mono ml-1">postgresql://</code>.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          ) : null}

          {probe ? <ProbeResult probe={probe} /> : null}
          {foreignDb ? (
            <ForeignDbWarning
              count={probe?.public_table_count ?? 0}
              proceedAnyway={proceedAnyway}
              onToggle={setProceedAnyway}
            />
          ) : null}
          {existingRailbase ? <ExistingRailbaseNotice /> : null}
          {save ? <SaveResult save={save} /> : null}
          {err ? (
            <p className="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2">
              {err}
            </p>
          ) : null}

          <div className="flex items-center gap-2 pt-2 border-t">
            <Button
              type="button"
              variant="outline"
              disabled={busy !== null || save?.ok === true}
              onClick={onProbe}
            >
              {busy === "probe" ? "Probing…" : "Probe connection"}
            </Button>
            <Button
              type="submit"
              disabled={busy !== null || save?.ok === true || saveLocked}
              title={
                saveLocked
                  ? "Confirm the warning above before saving."
                  : undefined
              }
            >
              {busy === "save" ? "Saving…" : "Save and restart later"}
            </Button>
            {/*
              Once save succeeded, the new DSN is on disk but the current
              process is still bound to setup-mode (no DB). An admin
              created NOW would land in the wrong place — empty after
              the restart. So we hide the Continue button entirely and
              let the in-process reload poll + window.location.reload()
              drive the operator into the admin step on the new DB.
            */}
          </div>

          {save?.ok && save.restart_required === false ? (
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
                This page will refresh automatically once it&apos;s ready — no
                terminal commands needed.
              </span>
            </div>
          ) : null}

          {save?.ok && save.restart_required === true ? (
            // Manual-restart path: kept as fallback for the rare case the
            // backend can't trigger an in-process reload (e.g. invoked
            // from a normal-boot wizard re-run where the chan is nil).
            <div className="mt-3 rounded border border-amber-300 bg-amber-50 px-3 py-3 text-sm text-amber-900">
              <strong className="block mb-1">
                Restart railbase to continue.
              </strong>
              <span className="block">
                The configuration is saved. Re-run the process to pick up the
                new DSN.
              </span>
              <code className="mt-2 block whitespace-pre rounded bg-amber-100 px-2 py-1 text-xs text-amber-900">
                {`# Ctrl-C in the terminal, then:\n./railbase serve`}
              </code>
            </div>
          ) : null}
        </form>
      </Form>
    </Card>
  );
}

function DriverRadio({
  value,
  checked,
  disabled,
  title,
  subtitle,
}: {
  value: string;
  checked: boolean;
  disabled?: boolean;
  title: string;
  subtitle: string;
}) {
  return (
    <label
      className={`flex items-start gap-2 rounded border px-3 py-2 ${
        disabled
          ? "border-input bg-muted opacity-60 cursor-not-allowed"
          : checked
            ? "border-neutral-900 bg-muted cursor-pointer"
            : "border-input cursor-pointer"
      }`}
    >
      <RadioGroupItem value={value} disabled={disabled} className="mt-1" />
      <span>
        <span className="block text-sm font-medium">{title}</span>
        <span className="block text-xs text-muted-foreground">{subtitle}</span>
      </span>
    </label>
  );
}

function SocketRadio({
  value,
  checked,
  path,
  distro,
}: {
  value: string;
  checked: boolean;
  path: string;
  distro: string;
}) {
  return (
    <label
      className={`flex items-start gap-2 rounded border px-3 py-2 cursor-pointer ${
        checked ? "border-neutral-900 bg-muted" : "border-input"
      }`}
    >
      <RadioGroupItem value={value} className="mt-1" />
      <span>
        <span className="block text-sm font-medium">{path}</span>
        <span className="block text-xs text-muted-foreground">{distro}</span>
      </span>
    </label>
  );
}

// ForeignDbWarning — v1.7.42 safety gate. Renders when the probe found
// non-system tables in `public` but no `_migrations` marker, signalling
// that the operator is about to point Railbase at someone else's
// database. We surface the count, explain the risk, and lock the Save
// button until they tick the acknowledgement.
//
// Why a soft block (checkbox) instead of a hard refusal: there are
// legitimate co-location cases (Railbase alongside another app sharing
// a logical DB) where the operator knows exactly what they're doing.
// The boot-time invariant in internal/db/migrate is the second slice
// of the same gate and catches the `.dsn`-edit bypass route.
function ForeignDbWarning({
  count,
  proceedAnyway,
  onToggle,
}: {
  count: number;
  proceedAnyway: boolean;
  onToggle: (v: boolean) => void;
}) {
  return (
    <div className="text-sm bg-amber-50 border border-amber-300 text-amber-900 rounded px-3 py-3 space-y-2">
      <p className="font-medium">This database is not empty.</p>
      <p className="text-xs">
        Found <strong>{count}</strong> table{count === 1 ? "" : "s"} in the{" "}
        <code className="rb-mono">public</code> schema, but none of them
        is a Railbase marker. Railbase expects either an empty database
        or an existing Railbase instance — saving now would install
        service tables and Postgres extensions alongside another app&apos;s
        data.
      </p>
      <p className="text-xs">
        If this is intentional (e.g. you&apos;re co-locating Railbase with
        another app in the same DB), tick the box to confirm.
      </p>
      <label className="inline-flex items-center gap-2 text-sm cursor-pointer pt-1">
        <Checkbox
          checked={proceedAnyway}
          onCheckedChange={(v) => onToggle(v === true)}
        />
        I understand — install Railbase alongside the existing tables.
      </label>
    </div>
  );
}

// ExistingRailbaseNotice — friendly green banner when the probe found
// the `_migrations` marker, confirming the operator is reconnecting to
// an existing Railbase install (e.g. after a restore or a re-deploy).
// Pure information; doesn't gate the Save button.
function ExistingRailbaseNotice() {
  return (
    <div className="text-sm bg-emerald-50 border border-emerald-200 text-emerald-800 rounded px-3 py-2">
      <p className="font-medium">Existing Railbase instance detected.</p>
      <p className="text-xs">
        Found the <code className="rb-mono">_migrations</code> marker —
        this database already belongs to Railbase. Saving will reconnect
        the running process to it and apply any pending migrations.
      </p>
    </div>
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
    <div className="text-sm bg-destructive/10 border border-destructive/30 text-destructive rounded px-3 py-2 space-y-1">
      <p className="font-medium">Connection failed.</p>
      {probe.error ? <p className="font-mono text-xs">{probe.error}</p> : null}
      {probe.hint ? <p className="text-xs">Hint: {probe.hint}</p> : null}
    </div>
  );
}

function SaveResult({ save }: { save: SaveResponse }) {
  if (!save.ok) {
    return (
      <div className="text-sm bg-destructive/10 border border-destructive/30 text-destructive rounded px-3 py-2">
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

// AdminStep — second wizard step, owns its own useForm. Submits to
// /_bootstrap and on success seeds the session token, refreshes the
// auth context, and redirects to /.
function AdminStep({ onBack }: { onBack: () => void }) {
  const { refresh } = useAuth();
  const [, navigate] = useLocation();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const form = useForm<AdminStepValues>({
    resolver: zodResolver(adminStepSchema),
    defaultValues: { email: "", password: "", confirm: "" },
    mode: "onSubmit",
  });

  // Strength meter on confirm mirrors the PRIMARY password value so
  // the bar doesn't flicker between weak/strong as the operator
  // re-types the same string. Watch is the canonical RHF-on-Preact
  // way to subscribe to a single field.
  const passwordValue = form.watch("password");

  async function onSubmit(values: AdminStepValues) {
    setErr(null);
    setBusy(true);
    try {
      const r = await api.request<{ token: string; record: { id: string } }>(
        "POST",
        "/_bootstrap",
        { body: { email: values.email, password: values.password } },
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
    <Card className="p-6">
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
          <header className="space-y-1">
            <h1 className="text-xl font-semibold">Create the first admin</h1>
            <p className="text-sm text-muted-foreground">
              Subsequent admins are created via{" "}
              <code className="rb-mono px-1 py-0.5 bg-muted rounded">
                railbase admin create
              </code>
              .
            </p>
          </header>

          <FormField
            control={form.control}
            name="email"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Email</FormLabel>
                <FormControl>
                  <Input
                    type="email"
                    autoComplete="username"
                    autoFocus
                    value={field.value}
                    onInput={(e) => field.onChange(e.currentTarget.value)}
                    onBlur={field.onBlur}
                    name={field.name}
                    ref={field.ref}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name="password"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Password</FormLabel>
                <FormControl>
                  <PasswordInput
                    showGenerate
                    showStrength
                    autoComplete="new-password"
                    value={field.value}
                    onInput={(e) => field.onChange(e.currentTarget.value)}
                    // When the dice generates a value, propagate it into
                    // BOTH primary and confirm — saves the operator from
                    // copying it into a password manager AND re-typing.
                    // The kit's generator only writes to whichever field
                    // hosts the dice, so the onGenerate callback fans out
                    // to the confirm field via setValue.
                    onGenerate={(p) => {
                      form.setValue("password", p, { shouldValidate: true });
                      form.setValue("confirm", p, { shouldValidate: true });
                    }}
                  />
                </FormControl>
                <FormDescription>
                  Minimum 8 characters. Use the dice to generate a strong one.
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name="confirm"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Confirm password</FormLabel>
                <FormControl>
                  <PasswordInput
                    autoComplete="new-password"
                    value={field.value}
                    onInput={(e) => field.onChange(e.currentTarget.value)}
                    // No generator on confirm. Mirror strength of PRIMARY
                    // so the bar doesn't flicker between weak/strong as
                    // the operator types the same string twice.
                    strengthValue={passwordValue}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          {err ? (
            <p className="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2">
              {err}
            </p>
          ) : null}

          <div className="flex items-center gap-2">
            <Button type="button" variant="outline" onClick={onBack}>
              ← Back
            </Button>
            <Button
              type="submit"
              disabled={busy || form.formState.isSubmitting}
            >
              {busy ? "Creating…" : "Create admin & sign in"}
            </Button>
          </div>
        </form>
      </Form>
    </Card>
  );
}

import { useEffect, useMemo, useState } from "react";
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

// v1.7.43 — wizard expanded from 2 steps to 3 (DB / mailer / admin).
// v1.7.47 — expanded again to 4: step 0 = database, step 1 = auth
//   methods, step 2 = mailer, step 3 = admin. Both auth + mailer steps
//   are REQUIRED before admin creation (server-side 412 gates in
//   bootstrapCreateHandler — authGateError + mailerGateError). Each
//   step can be explicitly skipped via a confirmation modal — that
//   records the corresponding setup_skipped_at flag, which also
//   satisfies the gate. Skipping auth leaves password-only as the safe
//   default (back-end forces auth.password.enabled=true on skip).
type Step = 0 | 1 | 2 | 3;

type MailerStatusResponse = {
  configured_at?: string;
  skipped_at?: string;
  skipped_reason?: string;
  mailer_required: boolean;
  config: {
    driver?: string;
    from_address?: string;
    from_name?: string;
    smtp_host?: string;
    smtp_port?: number;
    smtp_user?: string;
    tls?: string;
    smtp_password_set: boolean;
  };
};

export function BootstrapScreen() {
  // We start on step 0 (database). Each step's "done" hook advances
  // the wizard; the operator can always click Back to revisit prior
  // steps. Auto-advance fires only when /_setup/detect reports the DB
  // is already configured (return-visit AFTER an in-process reload OR
  // manual restart). The mailer step doesn't auto-advance — it's
  // explicitly operator-acknowledged before admin creation.
  const [step, setStep] = useState<Step>(0);
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
    // Pre-auth setup wizard — multi-step full-viewport flow, intentionally
    // not <AdminPage> (no admin session yet, no sidebar). docs/12 §Layout
    // whitelists pre-auth screens.
    // eslint-disable-next-line railbase/no-raw-page-shell
    <div className="min-h-screen flex items-center justify-center bg-muted p-6">
      <div className="w-full max-w-2xl space-y-3">
        <Stepper step={step} />
        {step === 0 ? (
          <DatabaseStep onContinue={() => setStep(1)} />
        ) : step === 1 ? (
          <AuthStep
            onContinue={() => setStep(2)}
            onBack={() => setStep(0)}
          />
        ) : step === 2 ? (
          <MailerStep
            onContinue={() => setStep(3)}
            onBack={() => setStep(1)}
          />
        ) : (
          <AdminStep onBack={() => setStep(2)} />
        )}
      </div>
    </div>
  );
}

function Stepper({ step }: { step: Step }) {
  const labels = [
    "1. Database",
    "2. Auth methods",
    "3. Mailer",
    "4. Admin account",
  ];
  return (
    <ol className="flex items-center gap-3 text-sm text-muted-foreground">
      {labels.map((label, i) => (
        <li
          key={i}
          className="flex items-center gap-3"
        >
          <span className={step === i ? "font-semibold text-foreground" : ""}>
            {label}
          </span>
          {i < labels.length - 1 ? <span>→</span> : null}
        </li>
      ))}
    </ol>
  );
}

// MailerStep — second wizard step (v1.7.43). Lets the operator
// configure outbound email (SMTP or console driver) OR explicitly
// skip the step. Status endpoint reports current configured/skipped
// state; on a re-visit the form pre-fills from the masked snapshot.
//
// Skip path: confirm modal → POST /_setup/mailer-skip with a reason.
// Save path: probe (test email) → save → continue. Mailer config is
// a HARD prerequisite for the admin step server-side; this UI just
// makes the gate visible rather than enforces it.
// v1.7.49.1 — MailerPresetRow.
//
// Three buttons that drive the SMTP host/port/encryption fields to
// documented defaults for the two providers operators run into 90%
// of the time + a Custom escape hatch.
//
//   Gmail: smtp.gmail.com / 587 / STARTTLS. Username = the operator's
//          Gmail address, password = an APP PASSWORD (NOT the account
//          password — Google blocks SMTP auth with the real password
//          since 2022). Per-preset banner spells this out.
//
//   MailHog: 127.0.0.1 / 1025 / no encryption. Username + password
//          are not used. Operator already has MailHog running locally;
//          the hint links to the UI on port 8025.
//
//   Custom: no-op. Operators who already typed a host before reaching
//          for the preset row don't lose their typing. This button is
//          purely visual / "I'm doing my own thing" affordance.
//
// The form prop is typed `any` because MailerStepValues is a
// z.discriminatedUnion local to MailerStep — hoisting the type into
// the module scope would mean restructuring three other places. The
// only fields we touch are smtp_host / smtp_port / tls / smtp_user
// which exist on the smtp variant; setValue is a no-op when in the
// console variant (the preset row only renders under SMTP anyway).
type MailerPreset = "gmail" | "mailhog" | "custom";

interface MailerPresetInfo {
  id: MailerPreset;
  label: string;
  hint?: string;
}

const MAILER_PRESETS: MailerPresetInfo[] = [
  {
    id: "gmail",
    label: "Gmail",
    hint:
      "Username = your Gmail address. Password must be an App Password " +
      "(generate at myaccount.google.com/apppasswords — your regular " +
      "Google password won't work for SMTP since 2022).",
  },
  {
    id: "mailhog",
    label: "MailHog (local dev)",
    hint:
      "Catches every outbound email in MailHog's inbox UI at " +
      "http://localhost:8025. No credentials needed — MailHog accepts " +
      "anything.",
  },
  {
    id: "custom",
    label: "Custom",
  },
];

// applyMailerPreset mutates the form to match the chosen preset. We
// keep smtp_user / smtp_password alone unless we're hopping to MailHog
// (which actively wants them empty) — that way an operator who picks
// Gmail, types creds, then briefly bounces to Custom doesn't lose
// their creds.
function applyMailerPreset(
   
  form: any,
  preset: MailerPreset,
) {
  switch (preset) {
    case "gmail":
      form.setValue("smtp_host", "smtp.gmail.com");
      form.setValue("smtp_port", 587);
      form.setValue("tls", "starttls");
      break;
    case "mailhog":
      form.setValue("smtp_host", "127.0.0.1");
      form.setValue("smtp_port", 1025);
      form.setValue("tls", "off");
      form.setValue("smtp_user", "");
      form.setValue("smtp_password", "");
      break;
    case "custom":
      // No-op: operator picks "Custom" to signal they'll type their
      // own values. Don't clobber whatever's already in the fields.
      break;
  }
}

// detectActivePreset infers which preset (if any) matches the current
// form fields. Used to highlight the right button on mount / on
// return-visits so the operator sees "ah, this is the Gmail config".
function detectActivePreset(host: string, port: number, tls: string): MailerPreset {
  if (host === "smtp.gmail.com" && port === 587 && tls === "starttls") {
    return "gmail";
  }
  if (
    (host === "127.0.0.1" || host === "localhost") &&
    port === 1025 &&
    tls === "off"
  ) {
    return "mailhog";
  }
  return "custom";
}

function MailerPresetRow({
   
  form,
}: {
   
  form: any;
}) {
  const host = form.watch("smtp_host") as string;
  const port = form.watch("smtp_port") as number;
  const tls = form.watch("tls") as string;
  const active = detectActivePreset(host, port, tls);
  const activeInfo = MAILER_PRESETS.find((p) => p.id === active);
  return (
    <div className="space-y-1.5">
      <div className="text-xs text-muted-foreground">Preset</div>
      <div className="flex flex-wrap items-center gap-2">
        {MAILER_PRESETS.map((p) => (
          <button
            key={p.id}
            type="button"
            onClick={() => applyMailerPreset(form, p.id)}
            className={
              "rounded-md border px-3 py-1.5 text-sm transition-colors " +
              (active === p.id
                ? "border-foreground bg-foreground text-background"
                : "bg-background hover:bg-muted")
            }
          >
            {p.label}
          </button>
        ))}
      </div>
      {activeInfo?.hint ? (
        <p className="text-xs text-muted-foreground bg-muted/50 border rounded px-2 py-1.5">
          {activeInfo.hint}
        </p>
      ) : null}
    </div>
  );
}

function MailerStep({
  onContinue,
  onBack,
}: {
  onContinue: () => void;
  onBack: () => void;
}) {
  const [status, setStatus] = useState<MailerStatusResponse | null>(null);
  const [busy, setBusy] = useState<
    null | "probe" | "save" | "skip" | "status"
  >("status");
  const [probeResult, setProbeResult] = useState<
    null | { ok: boolean; error?: string; hint?: string; driver?: string }
  >(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [showSkipConfirm, setShowSkipConfirm] = useState(false);
  const [skipReason, setSkipReason] = useState("");

  const mailerSchema = useMemo(
    () =>
      z.discriminatedUnion("driver", [
        z.object({
          driver: z.literal("smtp"),
          from_address: z.string().email("Valid email required"),
          from_name: z.string().optional(),
          smtp_host: z.string().min(1, "SMTP host required"),
          smtp_port: z
            .number({
              invalid_type_error: "Port must be a number",
              required_error: "Port is required",
            })
            .int()
            .min(1, "Port must be ≥ 1")
            .max(65535, "Port must be ≤ 65535"),
          smtp_user: z.string().optional(),
          smtp_password: z.string().optional(),
          tls: z.enum(["starttls", "implicit", "off"]),
          probe_to: z.string().email("Valid email required for the probe"),
        }),
        z.object({
          driver: z.literal("console"),
          from_address: z.string().email("Valid email required"),
          from_name: z.string().optional(),
          probe_to: z.string().email("Valid email required for the probe"),
        }),
      ]),
    [],
  );

  type MailerStepValues = z.infer<typeof mailerSchema>;

  const form = useForm<MailerStepValues>({
    resolver: zodResolver(mailerSchema),
    defaultValues: {
      driver: "smtp",
      from_address: "",
      from_name: "",
      smtp_host: "",
      smtp_port: 587,
      smtp_user: "",
      smtp_password: "",
      tls: "starttls",
      probe_to: "",
    } as MailerStepValues,
    mode: "onSubmit",
  });
  const driver = form.watch("driver");

  useEffect(() => {
    let cancelled = false;
    fetch("/api/_admin/_setup/mailer-status")
      .then((r) => r.json())
      .then((d: MailerStatusResponse) => {
        if (cancelled) return;
        setStatus(d);
        // Pre-fill the form from the masked snapshot on return visits.
        if (d.config.driver === "smtp") {
          form.reset({
            driver: "smtp",
            from_address: d.config.from_address ?? "",
            from_name: d.config.from_name ?? "",
            smtp_host: d.config.smtp_host ?? "",
            smtp_port: d.config.smtp_port ?? 587,
            smtp_user: d.config.smtp_user ?? "",
            smtp_password: "",
            tls:
              (d.config.tls as "starttls" | "implicit" | "off") ?? "starttls",
            probe_to: "",
          });
        } else if (d.config.driver === "console") {
          form.reset({
            driver: "console",
            from_address: d.config.from_address ?? "",
            from_name: d.config.from_name ?? "",
            probe_to: "",
          });
        }
      })
      .catch(() => {
        // status endpoint failure is not fatal — operator can still
        // submit the form; the save endpoint will surface the real error.
      })
      .finally(() => {
        if (!cancelled) setBusy(null);
      });
    return () => {
      cancelled = true;
    };
     
  }, []);

  const alreadyResolved =
    !!status && (!!status.configured_at || !!status.skipped_at);

  function bodyForBackend(values: MailerStepValues) {
    if (values.driver === "smtp") {
      return {
        driver: "smtp",
        from_address: values.from_address,
        from_name: values.from_name ?? "",
        smtp_host: values.smtp_host,
        smtp_port: values.smtp_port,
        smtp_user: values.smtp_user ?? "",
        smtp_password: values.smtp_password ?? "",
        tls: values.tls,
        probe_to: values.probe_to,
      };
    }
    return {
      driver: "console",
      from_address: values.from_address,
      from_name: values.from_name ?? "",
      smtp_host: "",
      smtp_port: 0,
      smtp_user: "",
      smtp_password: "",
      tls: "",
      probe_to: values.probe_to,
    };
  }

  async function onProbe() {
    setSaveError(null);
    setProbeResult(null);
    const valid = await form.trigger();
    if (!valid) return;
    setBusy("probe");
    try {
      const r = await fetch("/api/_admin/_setup/mailer-probe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(bodyForBackend(form.getValues())),
      });
      const data = await r.json();
      if (r.status === 400) {
        setSaveError(data?.error?.message ?? "Validation error.");
      } else {
        setProbeResult(data);
      }
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : "Probe failed.");
    } finally {
      setBusy(null);
    }
  }

  async function onSubmit(values: MailerStepValues) {
    setSaveError(null);
    setBusy("save");
    try {
      const r = await fetch("/api/_admin/_setup/mailer-save", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(bodyForBackend(values)),
      });
      const data = await r.json();
      if (r.status !== 200 || data?.ok === false) {
        setSaveError(
          data?.error?.message ?? data?.note ?? "Save failed.",
        );
      } else {
        onContinue();
      }
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : "Save failed.");
    } finally {
      setBusy(null);
    }
  }

  async function onSkip() {
    if (skipReason.trim().length === 0) {
      setSaveError("Please provide a brief reason for skipping.");
      return;
    }
    setBusy("skip");
    setSaveError(null);
    try {
      const r = await fetch("/api/_admin/_setup/mailer-skip", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ reason: skipReason }),
      });
      const data = await r.json();
      if (r.status !== 200 || data?.ok === false) {
        setSaveError(data?.error?.message ?? "Skip failed.");
      } else {
        setShowSkipConfirm(false);
        onContinue();
      }
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : "Skip failed.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card className="p-6">
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-5">
          <header className="space-y-1">
            <h1 className="text-xl font-semibold">Configure email delivery</h1>
            <p className="text-sm text-muted-foreground">
              Railbase sends welcome notifications + compromise-detection
              broadcasts whenever an administrator account is created.
              Configure how outbound email is delivered, or skip this step
              if you&apos;ll set it up later.
            </p>
          </header>

          {alreadyResolved ? (
            <div className="text-sm bg-primary/10 border border-primary/40 text-primary rounded px-3 py-2">
              {status?.configured_at ? (
                <p>
                  Mailer was configured on{" "}
                  <code className="font-mono">{status.configured_at}</code>. You
                  can update the settings below or continue.
                </p>
              ) : status?.skipped_at ? (
                <p>
                  Mailer setup was skipped on{" "}
                  <code className="font-mono">{status.skipped_at}</code>
                  {status.skipped_reason ? (
                    <>
                      {" "}
                      — reason: <em>{status.skipped_reason}</em>
                    </>
                  ) : null}
                  . Welcome emails will not fire until you configure the
                  mailer.
                </p>
              ) : null}
            </div>
          ) : null}

          <FormField
            control={form.control}
            name="driver"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Delivery driver</FormLabel>
                <FormControl>
                  <RadioGroup
                    value={field.value}
                    onValueChange={(v) => {
                      // Switch branch via form.reset (same trick as the
                      // DB step) so the discriminated union validates.
                      // Stash cross-branch values first so the operator
                      // doesn't lose what they typed when toggling
                      // driver back and forth.
                      const cur = form.getValues();
                      const fromAddr =
                        (cur as { from_address?: string }).from_address ?? "";
                      const probeTo =
                        (cur as { probe_to?: string }).probe_to ?? "";
                      if (v === "smtp") {
                        form.reset({
                          driver: "smtp",
                          from_address: fromAddr,
                          from_name: "",
                          smtp_host: "",
                          smtp_port: 587,
                          smtp_user: "",
                          smtp_password: "",
                          tls: "starttls",
                          probe_to: probeTo,
                        });
                      } else {
                        form.reset({
                          driver: "console",
                          from_address: fromAddr,
                          from_name: "",
                          probe_to: probeTo,
                        });
                      }
                    }}
                    className="gap-2"
                  >
                    <label className="flex items-start gap-2 rounded border px-3 py-2 cursor-pointer">
                      <RadioGroupItem value="smtp" className="mt-1" />
                      <span>
                        <span className="block text-sm font-medium">
                          SMTP
                        </span>
                        <span className="block text-xs text-muted-foreground">
                          Production-grade. Point at your provider (Gmail,
                          Mailgun, SendGrid, Postmark, …) or self-hosted SMTP.
                        </span>
                      </span>
                    </label>
                    <label className="flex items-start gap-2 rounded border px-3 py-2 cursor-pointer">
                      <RadioGroupItem value="console" className="mt-1" />
                      <span>
                        <span className="block text-sm font-medium">
                          Console (development)
                        </span>
                        <span className="block text-xs text-muted-foreground">
                          Emails are printed to the Railbase server logs.
                          Useful for local dev — operator sees the welcome
                          email without a real SMTP setup.
                        </span>
                      </span>
                    </label>
                  </RadioGroup>
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <div className="grid grid-cols-2 gap-3">
            <FormField
              control={form.control}
              name="from_address"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>From address</FormLabel>
                  <FormControl>
                    <Input
                      type="email"
                      value={field.value ?? ""}
                      onInput={(e) =>
                        field.onChange(e.currentTarget.value)
                      }
                      onBlur={field.onBlur}
                      name={field.name}
                      ref={field.ref}
                      placeholder="railbase@yourcompany.com"
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name={"from_name" as never}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>From name (optional)</FormLabel>
                  <FormControl>
                    <Input
                      type="text"
                      value={(field.value as string) ?? ""}
                      onInput={(e) =>
                        field.onChange(e.currentTarget.value)
                      }
                      onBlur={field.onBlur}
                      name={field.name}
                      ref={field.ref}
                      placeholder="Railbase"
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>

          {driver === "smtp" ? (
            <div className="space-y-3">
              {/* v1.7.49.1 — SMTP-preset row. Three buttons that fill
                  the host/port/encryption fields with the documented
                  defaults for the two providers operators run into 90%
                  of the time + a Custom escape hatch. The "Custom"
                  button doesn't clear anything — operators who type a
                  host first and then realise they want a preset never
                  lose their typing. Preset choice is local UI state;
                  what gets POSTed is whatever's in the form fields. */}
              <MailerPresetRow form={form} />
              <div className="grid grid-cols-3 gap-3">
                <FormField
                  control={form.control}
                  name={"smtp_host" as never}
                  render={({ field }) => (
                    <FormItem className="col-span-2">
                      <FormLabel>SMTP host</FormLabel>
                      <FormControl>
                        <Input
                          type="text"
                          value={(field.value as string) ?? ""}
                          onInput={(e) =>
                            field.onChange(e.currentTarget.value)
                          }
                          onBlur={field.onBlur}
                          name={field.name}
                          ref={field.ref}
                          placeholder="smtp.gmail.com"
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name={"smtp_port" as never}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Port</FormLabel>
                      <FormControl>
                        <Input
                          type="number"
                          inputMode="numeric"
                          value={(field.value as number)?.toString() ?? ""}
                          onInput={(e) => {
                            const n = parseInt(
                              e.currentTarget.value,
                              10,
                            );
                            field.onChange(isNaN(n) ? 0 : n);
                          }}
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
              {/* Username + Password stack vertically — Password carries a
                  FormDescription which would otherwise stretch the grid row
                  and leave a visual gap under Username. SMTP "Username" is
                  typically the operator's email (Gmail/SES/etc.) — use
                  type="email" + autoComplete="email" so the browser keyboard
                  / autofill behave correctly. */}
              <FormField
                control={form.control}
                name={"smtp_user" as never}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Username</FormLabel>
                    <FormControl>
                      <Input
                        type="email"
                        autoComplete="email"
                        placeholder="usually your account email"
                        value={(field.value as string) ?? ""}
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
                name={"smtp_password" as never}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Password</FormLabel>
                    <FormControl>
                      <Input
                        type="password"
                        autoComplete="new-password"
                        value={(field.value as string) ?? ""}
                        onInput={(e) =>
                          field.onChange(e.currentTarget.value)
                        }
                        onBlur={field.onBlur}
                        name={field.name}
                        ref={field.ref}
                        placeholder={
                          status?.config.smtp_password_set
                            ? "(unchanged — leave empty to keep current)"
                            : ""
                        }
                      />
                    </FormControl>
                    <FormDescription>
                      For Gmail-style providers, use an app-specific
                      password.
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name={"tls" as never}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Encryption</FormLabel>
                    <FormControl>
                      <RadioGroup
                        value={(field.value as string) ?? "starttls"}
                        onValueChange={field.onChange}
                        className="flex gap-3"
                      >
                        <label className="inline-flex items-center gap-2 text-sm">
                          <RadioGroupItem value="starttls" />
                          STARTTLS (port 587)
                        </label>
                        <label className="inline-flex items-center gap-2 text-sm">
                          <RadioGroupItem value="implicit" />
                          Implicit TLS (port 465)
                        </label>
                        <label className="inline-flex items-center gap-2 text-sm">
                          <RadioGroupItem value="off" />
                          None
                        </label>
                      </RadioGroup>
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          ) : null}

          <FormField
            control={form.control}
            name="probe_to"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Probe recipient</FormLabel>
                <FormControl>
                  <Input
                    type="email"
                    value={field.value ?? ""}
                    onInput={(e) => field.onChange(e.currentTarget.value)}
                    onBlur={field.onBlur}
                    name={field.name}
                    ref={field.ref}
                    placeholder="you@yourcompany.com"
                  />
                </FormControl>
                <FormDescription>
                  We&apos;ll send a small test email here when you press
                  &quot;Send test email&quot;. Doesn&apos;t need to be the
                  admin email.
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          {probeResult ? (
            probeResult.ok ? (
              <div className="text-sm bg-primary/10 border border-primary/40 text-primary rounded px-3 py-2">
                Test email dispatched via{" "}
                <strong>{probeResult.driver}</strong>. Check the recipient
                inbox (or the Railbase logs for the console driver) to
                confirm receipt.
              </div>
            ) : (
              <div className="text-sm bg-destructive/10 border border-destructive/30 text-destructive rounded px-3 py-2 space-y-1">
                <p className="font-medium">Test send failed.</p>
                {probeResult.error ? (
                  <p className="font-mono text-xs">{probeResult.error}</p>
                ) : null}
                {probeResult.hint ? (
                  <p className="text-xs">Hint: {probeResult.hint}</p>
                ) : null}
              </div>
            )
          ) : null}

          {saveError ? (
            <p className="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2">
              {saveError}
            </p>
          ) : null}

          <div className="flex items-center gap-2 pt-2 border-t">
            <Button type="button" variant="outline" onClick={onBack}>
              ← Back
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={busy !== null}
              onClick={onProbe}
            >
              {busy === "probe" ? "Sending…" : "Send test email"}
            </Button>
            <Button type="submit" disabled={busy !== null}>
              {busy === "save" ? "Saving…" : "Save and continue"}
            </Button>
            <Button
              type="button"
              variant="link"
              className="text-muted-foreground text-sm ml-auto"
              onClick={() => setShowSkipConfirm(true)}
              disabled={busy !== null}
            >
              Skip — I&apos;ll configure later
            </Button>
          </div>

          {showSkipConfirm ? (
            <div className="mt-3 rounded border border-input bg-muted px-3 py-3 text-sm text-foreground space-y-2">
              <p className="font-medium">Skip mailer configuration?</p>
              <p className="text-xs">
                Without a configured mailer, no welcome emails fire when
                administrators are created and no compromise-detection
                broadcasts go out. You can configure the mailer later via
                Settings, and the v1.7.43 retry sweeper will replay any
                queued welcomes whose dispatch failed within the last 7
                days.
              </p>
              <p className="text-xs">
                Please provide a brief reason — this is recorded in the
                audit log.
              </p>
              <Input
                type="text"
                value={skipReason}
                onInput={(e) => setSkipReason(e.currentTarget.value)}
                placeholder="e.g. SMTP credentials still pending from infra team"
                className="text-sm"
              />
              <div className="flex items-center gap-2 pt-1">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setShowSkipConfirm(false)}
                  disabled={busy === "skip"}
                >
                  Cancel
                </Button>
                <Button
                  type="button"
                  size="sm"
                  onClick={onSkip}
                  disabled={busy === "skip"}
                >
                  {busy === "skip" ? "Recording…" : "Confirm skip"}
                </Button>
              </div>
            </div>
          ) : null}
        </form>
      </Form>
    </Card>
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
              <code className="font-mono px-1 py-0.5 bg-muted rounded">
                &lt;dataDir&gt;/.dsn
              </code>
              .
            </p>
          </header>

          {detect?.configured ? (
            <div className="text-sm bg-primary/10 border border-primary/40 text-primary rounded px-3 py-2">
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

              {/* Username + Password stack vertically — the Password's
                  FormDescription used to make the grid row uneven, leaving
                  empty space under Username. A flat vertical flow keeps the
                  visual balance and reads top-to-bottom like a login form. */}
              <FormField
                control={form.control}
                name="username"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Username</FormLabel>
                    <FormControl>
                      <Input
                        type="text"
                        autoComplete="username"
                        placeholder="Postgres role (often your OS user — not an email)"
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
              <div className="grid grid-cols-2 gap-3">
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
                    <code className="font-mono">postgres://</code> or
                    <code className="font-mono ml-1">postgresql://</code>.
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
            <div className="mt-3 rounded border border-primary/40 bg-primary/10 px-3 py-3 text-sm text-primary">
              <strong className="block mb-1 flex items-center gap-2">
                <span className="inline-block h-3 w-3 rounded-full bg-primary animate-pulse" />
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
            <div className="mt-3 rounded border border-input bg-muted px-3 py-3 text-sm text-foreground">
              <strong className="block mb-1">
                Restart railbase to continue.
              </strong>
              <span className="block">
                The configuration is saved. Re-run the process to pick up the
                new DSN.
              </span>
              <code className="mt-2 block whitespace-pre rounded bg-muted px-2 py-1 text-xs text-foreground">
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
            ? "border-foreground bg-muted cursor-pointer"
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
        checked ? "border-foreground bg-muted" : "border-input"
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
    <div className="text-sm bg-muted border border-input text-foreground rounded px-3 py-3 space-y-2">
      <p className="font-medium">This database is not empty.</p>
      <p className="text-xs">
        Found <strong>{count}</strong> table{count === 1 ? "" : "s"} in the{" "}
        <code className="font-mono">public</code> schema, but none of them
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
    <div className="text-sm bg-primary/10 border border-primary/40 text-primary rounded px-3 py-2">
      <p className="font-medium">Existing Railbase instance detected.</p>
      <p className="text-xs">
        Found the <code className="font-mono">_migrations</code> marker —
        this database already belongs to Railbase. Saving will reconnect
        the running process to it and apply any pending migrations.
      </p>
    </div>
  );
}

function ProbeResult({ probe }: { probe: ProbeResponse }) {
  if (probe.ok) {
    return (
      <div className="text-sm bg-primary/10 border border-primary/40 text-primary rounded px-3 py-2 space-y-1">
        <p className="font-medium">Connection OK.</p>
        {probe.version ? (
          <p className="font-mono text-xs">{probe.version}</p>
        ) : null}
        {probe.dsn ? (
          <p className="text-xs">
            DSN: <code className="font-mono">{probe.dsn}</code>
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
    <div className="text-sm bg-primary/10 border border-primary/40 text-primary rounded px-3 py-2 space-y-1">
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
              <code className="font-mono px-1 py-0.5 bg-muted rounded">
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

// ---------------------------------------------------------------------
// AuthStep — v1.7.47. Sits between Database (step 0) and Mailer (step
// 2) in the bootstrap wizard. Captures which authentication mechanisms
// the install will offer to app users — first-class methods (password,
// magic link, OTP, TOTP 2FA, WebAuthn passkeys) and built-in OAuth
// providers (google, github, apple, generic OIDC).
//
// Pattern matches MailerStep: plain useState (not RHF) because the
// dynamic per-provider field set would make a zod schema awkward.
// Three button paths: Save & continue → POST /_setup/auth-save;
// Skip → POST /_setup/auth-skip with a reason; Back → step 0.
//
// v1.7.49 — LDAP / Active Directory is now a CORE method, not a plugin.
// The card collects a full ldap.Config: URL, TLS mode, bind DN, bind
// password, user filter + base DN, attribute mapping. SAML + SCIM
// remain in the plugin-gated list with concrete v1.7.50 / v1.7.51
// target versions (NOT "plugin required" — they're scheduled core
// slices, not external add-ons).
//
// LDAP config changes require a server restart to take effect (the
// Authenticator is built once at boot from the snapshot). The wizard
// surfaces this explicitly so operators don't toggle expecting hot-
// reload. Future polish slice could add atomic-swap, but for v1.7.49
// the restart contract matches the DSN-change pattern already in use.
// ---------------------------------------------------------------------

type OAuthSnapshot = {
  enabled: boolean;
  client_id?: string;
  client_secret?: string; // "set" sentinel — never the real value
  issuer?: string;
};

type LDAPSnapshot = {
  enabled: boolean;
  url?: string;
  tls_mode?: string;
  insecure_skip_verify?: boolean;
  bind_dn?: string;
  bind_password_set: boolean;
  user_base_dn?: string;
  user_filter?: string;
  email_attr?: string;
  name_attr?: string;
};

type SAMLSnapshot = {
  enabled: boolean;
  idp_metadata_url?: string;
  idp_metadata_xml?: string;
  sp_entity_id?: string;
  sp_acs_url?: string;
  sp_slo_url?: string;
  email_attribute?: string;
  name_attribute?: string;
  allow_idp_initiated?: boolean;
  // v1.7.50.1b — signed AuthnRequest support. sp_key_pem is the
  // secret half (encrypted server-side, same handling as
  // ldap.bind_password). The status response sets `sp_key_pem_set`
  // when a key is stored; we never round-trip the key value down.
  sign_authn_requests?: boolean;
  sp_cert_pem?: string;
  sp_key_pem_set?: boolean;
  // v1.7.50.1d — group → RBAC role mapping. The mapping JSON is
  // round-tripped verbatim; the operator edits it as text.
  group_attribute?: string;
  role_mapping?: string;
};

type PluginGated = {
  name: string;
  display_name: string;
  plugin: string;
  available_in: string;
};

// v1.7.51 — SCIM 2.0 inbound provisioning. Wizard captures just the
// opt-in toggle + target collection; token minting lives in the CLI
// (`railbase scim token create`) and the admin UI panel (v1.7.52+).
// `tokens_active` is read-only — surfaced so the operator sees at a
// glance whether any IdP is currently connected.
type SCIMSnapshot = {
  enabled: boolean;
  collection?: string;
  tokens_active: number;
  endpoint_url?: string;
};

type AuthStatusResponse = {
  configured_at?: string;
  skipped_at?: string;
  skipped_reason?: string;
  methods: Record<string, boolean>;
  oauth: Record<string, OAuthSnapshot>;
  ldap: LDAPSnapshot;
  saml: SAMLSnapshot;
  scim: SCIMSnapshot;
  plugin_gated: PluginGated[];
  redirect_base: string;
};

const OAUTH_PROVIDERS = [
  { id: "google", label: "Google" },
  { id: "github", label: "GitHub" },
  { id: "apple", label: "Apple" },
  { id: "oidc", label: "Generic OIDC" },
] as const;

const METHOD_LABELS: Record<string, { title: string; hint: string }> = {
  password: {
    title: "Password",
    hint: "Classic email + password sign-in. Recommended baseline.",
  },
  magic_link: {
    title: "Magic link",
    hint: "Passwordless: emailed one-tap link. Requires the mailer.",
  },
  otp: {
    title: "Email OTP",
    hint: "6-digit code emailed to the user. Useful as a backup factor.",
  },
  totp: {
    title: "TOTP 2FA",
    hint: "Authenticator-app codes (Google Authenticator, 1Password, etc.). Optional second factor.",
  },
  webauthn: {
    title: "Passkeys (WebAuthn)",
    hint: "Hardware-key / platform-authenticator sign-in. Optional alternative to passwords.",
  },
};

function AuthStep({
  onContinue,
  onBack,
}: {
  onContinue: () => void;
  onBack: () => void;
}) {
  const [status, setStatus] = useState<AuthStatusResponse | null>(null);
  const [busy, setBusy] = useState<null | "status" | "save" | "skip">("status");
  const [err, setErr] = useState<string | null>(null);
  const [showSkipConfirm, setShowSkipConfirm] = useState(false);
  const [skipReason, setSkipReason] = useState("");

  // Local editable state. Initialised from /auth-status on mount;
  // POSTed verbatim to /auth-save. We track client_secret separately
  // from the snapshot's "set" sentinel so the operator can clearly
  // signal "leave the stored secret untouched" by NOT typing anything.
  const [methods, setMethods] = useState<Record<string, boolean>>({});
  const [oauth, setOauth] = useState<Record<string, OAuthSnapshot>>({});
  // v1.7.49 LDAP — separate state slice. bind_password is local-only:
  // the wizard never echoes the stored value (UI shows "•••• stored"
  // instead). The empty string in this state means "don't overwrite".
  const [ldap, setLdap] = useState<LDAPSnapshot & { bind_password: string }>({
    enabled: false,
    bind_password_set: false,
    bind_password: "",
  });
  // v1.7.50 SAML — separate state slice. IdP metadata XML is public
  // per SAML spec so we round-trip it (no echo-suppression like the
  // LDAP bind_password). sp_key_pem IS secret and uses the same
  // preserve-on-empty contract as bind_password.
  const [saml, setSaml] = useState<SAMLSnapshot & { sp_key_pem: string }>({
    enabled: false,
    sp_key_pem: "",
  });
  // v1.7.51 SCIM — opt-in toggle + target collection. Token minting is
  // out of band (CLI `railbase scim token create` or admin UI panel).
  const [scim, setScim] = useState<SCIMSnapshot>({
    enabled: false,
    collection: "users",
    tokens_active: 0,
  });

  useEffect(() => {
    let cancelled = false;
    setBusy("status");
    fetch("/api/_admin/_setup/auth-status")
      .then((r) => r.json())
      .then((d: AuthStatusResponse) => {
        if (cancelled) return;
        setStatus(d);
        setMethods({ ...d.methods });
        // Drop the "set" sentinel from local state — the operator types
        // a NEW value (or leaves empty to keep the stored one). Carrying
        // "set" around as an input value would be confusing.
        const cleaned: Record<string, OAuthSnapshot> = {};
        for (const [k, v] of Object.entries(d.oauth ?? {})) {
          cleaned[k] = { ...v, client_secret: v.client_secret === "set" ? "" : v.client_secret };
        }
        setOauth(cleaned);
        // LDAP snapshot — replace bind_password with empty string so
        // the input renders blank (the bind_password_set flag tells
        // the UI whether a stored value exists).
        if (d.ldap) {
          setLdap({ ...d.ldap, bind_password: "" });
        }
        if (d.saml) {
          setSaml({ ...d.saml, sp_key_pem: "" });
        }
        if (d.scim) {
          setScim(d.scim);
        }
        setBusy(null);
      })
      .catch((e) => {
        if (cancelled) return;
        setErr(e instanceof Error ? e.message : "Failed to load auth status.");
        setBusy(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  function toggleMethod(key: string, on: boolean) {
    setMethods((cur) => ({ ...cur, [key]: on }));
  }

  function patchOAuth(provider: string, patch: Partial<OAuthSnapshot>) {
    setOauth((cur) => ({
      ...cur,
      [provider]: { ...(cur[provider] ?? { enabled: false }), ...patch },
    }));
  }

  function patchLdap(patch: Partial<typeof ldap>) {
    setLdap((cur) => ({ ...cur, ...patch }));
  }

  function patchSaml(patch: Partial<SAMLSnapshot & { sp_key_pem: string }>) {
    setSaml((cur) => ({ ...cur, ...patch }));
  }

  async function onSave() {
    setBusy("save");
    setErr(null);
    try {
      // Build the LDAP save body. We pass the bind_password field even
      // when empty — backend treats empty as "preserve stored value".
      const ldapBody = ldap.enabled
        ? {
            enabled: true,
            url: ldap.url ?? "",
            tls_mode: ldap.tls_mode ?? "starttls",
            insecure_skip_verify: !!ldap.insecure_skip_verify,
            bind_dn: ldap.bind_dn ?? "",
            bind_password: ldap.bind_password,
            user_base_dn: ldap.user_base_dn ?? "",
            user_filter: ldap.user_filter ?? "",
            email_attr: ldap.email_attr ?? "",
            name_attr: ldap.name_attr ?? "",
          }
        : { enabled: false };
      // SAML save body — pointer-typed on the backend, so we always
      // send (even when disabled) to make the toggle take effect.
      // sp_key_pem is passed verbatim — backend treats empty as
      // "preserve stored value" (same contract as ldap.bind_password).
      const samlBody = saml.enabled
        ? {
            enabled: true,
            idp_metadata_url: saml.idp_metadata_url ?? "",
            idp_metadata_xml: saml.idp_metadata_xml ?? "",
            sp_entity_id: saml.sp_entity_id ?? "",
            sp_acs_url: saml.sp_acs_url ?? "",
            sp_slo_url: saml.sp_slo_url ?? "",
            email_attribute: saml.email_attribute ?? "",
            name_attribute: saml.name_attribute ?? "",
            allow_idp_initiated: !!saml.allow_idp_initiated,
            sign_authn_requests: !!saml.sign_authn_requests,
            sp_cert_pem: saml.sp_cert_pem ?? "",
            sp_key_pem: saml.sp_key_pem,
            group_attribute: saml.group_attribute ?? "",
            role_mapping: saml.role_mapping ?? "",
          }
        : { enabled: false };
      // v1.7.51 — SCIM save body. Just enabled + collection; token
      // minting is intentionally out of band of the public wizard
      // endpoint (no admin auth at wizard time = no long-lived
      // bearer mint).
      const scimBody = {
        enabled: !!scim.enabled,
        collection: scim.collection ?? "users",
      };
      const r = await fetch("/api/_admin/_setup/auth-save", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ methods, oauth, ldap: ldapBody, saml: samlBody, scim: scimBody }),
      });
      const data = await r.json();
      if (r.status !== 200 || data?.ok === false) {
        setErr(data?.error?.message ?? "Save failed.");
      } else {
        onContinue();
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Save failed.");
    } finally {
      setBusy(null);
    }
  }

  async function onSkip() {
    if (skipReason.trim().length === 0) {
      setErr("Please provide a brief reason for skipping.");
      return;
    }
    setBusy("skip");
    setErr(null);
    try {
      const r = await fetch("/api/_admin/_setup/auth-skip", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ reason: skipReason }),
      });
      const data = await r.json();
      if (r.status !== 200 || data?.ok === false) {
        setErr(data?.error?.message ?? "Skip failed.");
      } else {
        setShowSkipConfirm(false);
        onContinue();
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Skip failed.");
    } finally {
      setBusy(null);
    }
  }

  const isReturnVisit = Boolean(status?.configured_at || status?.skipped_at);

  return (
    <Card className="p-6 space-y-6">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold">Auth methods</h1>
        <p className="text-sm text-muted-foreground">
          Choose how end-users sign in. Admin sign-in (this UI) is always
          password-based and unaffected by these toggles.
        </p>
        {isReturnVisit ? (
          <p className="text-xs text-muted-foreground">
            {status?.configured_at
              ? `Last configured ${status.configured_at}.`
              : `Previously skipped: ${status?.skipped_reason || "no reason recorded"}.`}{" "}
            You can adjust below and Save to re-stamp.
          </p>
        ) : null}
      </header>

      {/* First-class methods toggle grid */}
      <section className="space-y-3">
        <h2 className="text-sm font-medium">Built-in methods</h2>
        <div className="grid gap-3">
          {Object.entries(METHOD_LABELS).map(([key, meta]) => (
            <label
              key={key}
              className="flex items-start gap-3 rounded-md border bg-background p-3 cursor-pointer"
            >
              <Checkbox
                checked={methods[key] ?? false}
                onCheckedChange={(v) => toggleMethod(key, v === true)}
                disabled={busy !== null}
              />
              <div className="flex-1">
                <div className="text-sm font-medium">{meta.title}</div>
                <div className="text-xs text-muted-foreground">{meta.hint}</div>
              </div>
            </label>
          ))}
        </div>
      </section>

      {/* Per-OAuth provider cards */}
      <section className="space-y-3">
        <h2 className="text-sm font-medium">OAuth / SSO providers</h2>
        <p className="text-xs text-muted-foreground">
          Redirect URI base for the provider's OAuth-app config:{" "}
          <code className="font-mono px-1 py-0.5 bg-muted rounded">
            {status?.redirect_base ?? "—"}
          </code>{" "}
          (per-provider URI is{" "}
          <code className="font-mono px-1 py-0.5 bg-muted rounded">
            {"{base}/{provider}/callback"}
          </code>
          ).
        </p>
        <div className="grid gap-3">
          {OAUTH_PROVIDERS.map((p) => {
            const cfg = oauth[p.id] ?? { enabled: false };
            return (
              <div
                key={p.id}
                className="rounded-md border bg-background p-3 space-y-2"
              >
                <label className="flex items-center gap-3 cursor-pointer">
                  <Checkbox
                    checked={cfg.enabled}
                    onCheckedChange={(v) =>
                      patchOAuth(p.id, { enabled: v === true })
                    }
                    disabled={busy !== null}
                  />
                  <span className="text-sm font-medium">{p.label}</span>
                  {status?.oauth?.[p.id]?.client_secret === "set" ? (
                    <span className="ml-auto text-xs text-muted-foreground">
                      secret stored
                    </span>
                  ) : null}
                </label>
                {cfg.enabled ? (
                  <div className="space-y-2 pl-7">
                    <div>
                      <label className="text-xs text-muted-foreground">
                        Client ID
                      </label>
                      <Input
                        type="text"
                        value={cfg.client_id ?? ""}
                        onInput={(e) =>
                          patchOAuth(p.id, {
                            client_id: e.currentTarget.value,
                          })
                        }
                        disabled={busy !== null}
                      />
                    </div>
                    <div>
                      <label className="text-xs text-muted-foreground">
                        Client secret
                        {status?.oauth?.[p.id]?.client_secret === "set" ? (
                          <span className="ml-1">
                            (leave blank to keep stored)
                          </span>
                        ) : null}
                      </label>
                      <PasswordInput
                        value={cfg.client_secret ?? ""}
                        onInput={(e) =>
                          patchOAuth(p.id, {
                            client_secret: e.currentTarget.value,
                          })
                        }
                        autoComplete="new-password"
                      />
                    </div>
                    {p.id === "oidc" ? (
                      <div>
                        <label className="text-xs text-muted-foreground">
                          Issuer URL
                        </label>
                        <Input
                          type="url"
                          placeholder="https://accounts.example.com"
                          value={cfg.issuer ?? ""}
                          onInput={(e) =>
                            patchOAuth(p.id, {
                              issuer: e.currentTarget.value,
                            })
                          }
                          disabled={busy !== null}
                        />
                      </div>
                    ) : null}
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      </section>

      {/* v1.7.49 — LDAP / Active Directory (core). Renders as a full
          configurable card alongside OAuth. The Enterprise SSO group
          below (SAML/SCIM) lists what's still in-flight as core slices. */}
      <section className="space-y-3">
        <h2 className="text-sm font-medium">LDAP / Active Directory</h2>
        <p className="text-xs text-muted-foreground">
          Enterprise SSO via your corporate directory. Config changes
          take effect after a server restart.
        </p>
        <div className="rounded-md border bg-background p-3 space-y-3">
          <label className="flex items-center gap-3 cursor-pointer">
            <Checkbox
              checked={ldap.enabled}
              onCheckedChange={(v) => patchLdap({ enabled: v === true })}
              disabled={busy !== null}
            />
            <span className="text-sm font-medium">Enable LDAP sign-in</span>
            {status?.ldap?.bind_password_set ? (
              <span className="ml-auto text-xs text-muted-foreground">
                bind password stored
              </span>
            ) : null}
          </label>
          {ldap.enabled ? (
            <div className="space-y-2 pl-7">
              <div>
                <label className="text-xs text-muted-foreground">
                  Server URL
                </label>
                <Input
                  type="text"
                  placeholder="ldaps://ad.example.com:636"
                  value={ldap.url ?? ""}
                  onInput={(e) => patchLdap({ url: e.currentTarget.value })}
                  disabled={busy !== null}
                />
                <p className="text-xs text-muted-foreground mt-0.5">
                  Use <code className="font-mono px-1 py-0.5 bg-muted rounded">ldaps://</code> for port 636 (recommended for AD) or <code className="font-mono px-1 py-0.5 bg-muted rounded">ldap://</code> for plain/STARTTLS on port 389.
                </p>
              </div>
              <div>
                <label className="text-xs text-muted-foreground">
                  TLS mode
                </label>
                <select
                  className="w-full rounded border bg-background px-2 py-1 text-sm"
                  value={ldap.tls_mode ?? "starttls"}
                  onChange={(e) => patchLdap({ tls_mode: e.currentTarget.value })}
                  disabled={busy !== null}
                >
                  <option value="starttls">STARTTLS (upgrade plain → TLS)</option>
                  <option value="tls">TLS (ldaps://)</option>
                  <option value="off">Off (plain — insecure on untrusted networks)</option>
                </select>
              </div>
              <label className="flex items-center gap-2 text-xs">
                <Checkbox
                  checked={!!ldap.insecure_skip_verify}
                  onCheckedChange={(v) =>
                    patchLdap({ insecure_skip_verify: v === true })
                  }
                  disabled={busy !== null}
                />
                <span className="text-destructive">
                  Skip TLS certificate verification (dev only)
                </span>
              </label>
              <div>
                <label className="text-xs text-muted-foreground">
                  Service-account bind DN
                </label>
                <Input
                  type="text"
                  placeholder="cn=railbase,ou=ServiceAccounts,dc=example,dc=com"
                  value={ldap.bind_dn ?? ""}
                  onInput={(e) => patchLdap({ bind_dn: e.currentTarget.value })}
                  disabled={busy !== null}
                />
              </div>
              <div>
                <label className="text-xs text-muted-foreground">
                  Service-account password
                  {ldap.bind_password_set ? (
                    <span className="ml-1">(leave blank to keep stored)</span>
                  ) : null}
                </label>
                <PasswordInput
                  value={ldap.bind_password}
                  onInput={(e) => patchLdap({ bind_password: e.currentTarget.value })}
                  autoComplete="new-password"
                />
              </div>
              <div>
                <label className="text-xs text-muted-foreground">
                  User search base DN
                </label>
                <Input
                  type="text"
                  placeholder="ou=Users,dc=example,dc=com"
                  value={ldap.user_base_dn ?? ""}
                  onInput={(e) => patchLdap({ user_base_dn: e.currentTarget.value })}
                  disabled={busy !== null}
                />
              </div>
              <div>
                <label className="text-xs text-muted-foreground">
                  User search filter (use <code className="font-mono">%s</code> for username)
                </label>
                <Input
                  type="text"
                  placeholder="(&amp;(objectClass=person)(|(uid=%s)(mail=%s)(sAMAccountName=%s)))"
                  value={ldap.user_filter ?? ""}
                  onInput={(e) => patchLdap({ user_filter: e.currentTarget.value })}
                  disabled={busy !== null}
                />
              </div>
              <div className="grid grid-cols-2 gap-2">
                <div>
                  <label className="text-xs text-muted-foreground">
                    Email attribute
                  </label>
                  <Input
                    type="text"
                    placeholder="mail"
                    value={ldap.email_attr ?? ""}
                    onInput={(e) => patchLdap({ email_attr: e.currentTarget.value })}
                    disabled={busy !== null}
                  />
                </div>
                <div>
                  <label className="text-xs text-muted-foreground">
                    Name attribute
                  </label>
                  <Input
                    type="text"
                    placeholder="cn"
                    value={ldap.name_attr ?? ""}
                    onInput={(e) => patchLdap({ name_attr: e.currentTarget.value })}
                    disabled={busy !== null}
                  />
                </div>
              </div>
            </div>
          ) : null}
        </div>
      </section>

      {/* v1.7.50 — SAML 2.0 (core). Renders alongside LDAP. */}
      <section className="space-y-3">
        <h2 className="text-sm font-medium">SAML 2.0 (SSO)</h2>
        <p className="text-xs text-muted-foreground">
          Enterprise SSO via an identity provider (Okta / Azure AD /
          OneLogin / ADFS / Auth0). Config changes take effect after a
          server restart.
        </p>
        <div className="rounded-md border bg-background p-3 space-y-3">
          <label className="flex items-center gap-3 cursor-pointer">
            <Checkbox
              checked={saml.enabled}
              onCheckedChange={(v) => patchSaml({ enabled: v === true })}
              disabled={busy !== null}
            />
            <span className="text-sm font-medium">Enable SAML sign-in</span>
          </label>
          {saml.enabled ? (
            <div className="space-y-2 pl-7">
              <div>
                <label className="text-xs text-muted-foreground">
                  IdP metadata URL (preferred)
                </label>
                <Input
                  type="url"
                  placeholder="https://idp.example.com/saml/metadata"
                  value={saml.idp_metadata_url ?? ""}
                  onInput={(e) => patchSaml({ idp_metadata_url: e.currentTarget.value })}
                  disabled={busy !== null}
                />
                <p className="text-xs text-muted-foreground mt-0.5">
                  Most IdPs publish this at a public URL. Use the
                  inline XML below for air-gapped deploys.
                </p>
              </div>
              <div>
                <label className="text-xs text-muted-foreground">
                  IdP metadata XML (alternative — paste raw)
                </label>
                <textarea
                  className="w-full rounded border bg-background px-2 py-1 text-sm font-mono"
                  rows={4}
                  placeholder="<EntityDescriptor xmlns=&quot;urn:oasis:names:tc:SAML:2.0:metadata&quot; ...>"
                  value={saml.idp_metadata_xml ?? ""}
                  onInput={(e) => patchSaml({ idp_metadata_xml: e.currentTarget.value })}
                  disabled={busy !== null}
                />
              </div>
              <div>
                <label className="text-xs text-muted-foreground">
                  SP Entity ID
                </label>
                <Input
                  type="text"
                  placeholder="https://railbase.example.com/saml/sp"
                  value={saml.sp_entity_id ?? ""}
                  onInput={(e) => patchSaml({ sp_entity_id: e.currentTarget.value })}
                  disabled={busy !== null}
                />
                <p className="text-xs text-muted-foreground mt-0.5">
                  The unique string identifying THIS Railbase install.
                  Paste this into your IdP's app config.
                </p>
              </div>
              <div>
                <label className="text-xs text-muted-foreground">
                  ACS URL (Assertion Consumer Service)
                </label>
                <Input
                  type="url"
                  placeholder="https://railbase.example.com/api/collections/users/auth-with-saml/acs"
                  value={saml.sp_acs_url ?? ""}
                  onInput={(e) => patchSaml({ sp_acs_url: e.currentTarget.value })}
                  disabled={busy !== null}
                />
                <p className="text-xs text-muted-foreground mt-0.5">
                  Where the IdP POSTs the SAMLResponse. Paste this into
                  your IdP's app config as the ACS / SSO URL.
                </p>
              </div>
              <div>
                <label className="text-xs text-muted-foreground">
                  SLO URL (Single Logout) — optional
                </label>
                <Input
                  type="url"
                  placeholder="https://railbase.example.com/api/collections/users/auth-with-saml/slo"
                  value={saml.sp_slo_url ?? ""}
                  onInput={(e) => patchSaml({ sp_slo_url: e.currentTarget.value })}
                  disabled={busy !== null}
                />
                <p className="text-xs text-muted-foreground mt-0.5">
                  Where the IdP POSTs the LogoutRequest. Leave blank if
                  your org doesn't use SAML Single Logout — the SLO
                  endpoint refuses incoming requests when unset.
                </p>
              </div>
              <div className="grid grid-cols-2 gap-2">
                <div>
                  <label className="text-xs text-muted-foreground">
                    Email attribute
                  </label>
                  <Input
                    type="text"
                    placeholder="email"
                    value={saml.email_attribute ?? ""}
                    onInput={(e) => patchSaml({ email_attribute: e.currentTarget.value })}
                    disabled={busy !== null}
                  />
                </div>
                <div>
                  <label className="text-xs text-muted-foreground">
                    Name attribute
                  </label>
                  <Input
                    type="text"
                    placeholder="name"
                    value={saml.name_attribute ?? ""}
                    onInput={(e) => patchSaml({ name_attribute: e.currentTarget.value })}
                    disabled={busy !== null}
                  />
                </div>
              </div>
              <label className="flex items-center gap-2 text-xs cursor-pointer">
                <Checkbox
                  checked={!!saml.allow_idp_initiated}
                  onCheckedChange={(v) =>
                    patchSaml({ allow_idp_initiated: v === true })
                  }
                  disabled={busy !== null}
                />
                <span>
                  Allow IdP-initiated sign-in
                  <span className="text-muted-foreground"> — IdP posts SAMLResponse without prior AuthnRequest. Opens a CSRF-shaped attack surface; enable only if your threat model accepts it.</span>
                </span>
              </label>

              {/* v1.7.50.1b — signed AuthnRequests. Some IdPs (Okta
                  strict, ADFS, compliance regimes) require the SP to
                  sign its AuthnRequests. */}
              <div className="space-y-2 rounded-md border border-dashed bg-muted/30 p-3">
                <label className="flex items-center gap-2 text-xs cursor-pointer">
                  <Checkbox
                    checked={!!saml.sign_authn_requests}
                    onCheckedChange={(v) =>
                      patchSaml({ sign_authn_requests: v === true })
                    }
                    disabled={busy !== null}
                  />
                  <span className="font-medium">Sign AuthnRequests</span>
                </label>
                <p className="text-xs text-muted-foreground -mt-1">
                  Enable when your IdP requires signed AuthnRequests
                  (Okta strict mode, ADFS default). Generate a fresh
                  cert+key pair with:{" "}
                  <code className="font-mono px-1 py-0.5 bg-muted rounded">
                    openssl req -x509 -newkey rsa:2048 -keyout sp.key -out sp.crt -days 730 -nodes -subj "/CN=railbase-saml-sp"
                  </code>
                </p>
                {saml.sign_authn_requests ? (
                  <>
                    <div>
                      <label className="text-xs text-muted-foreground">
                        SP Certificate (PEM)
                      </label>
                      <textarea
                        className="w-full text-xs font-mono rounded-md border border-input bg-background px-2 py-1.5"
                        rows={4}
                        placeholder="-----BEGIN CERTIFICATE-----&#10;MIIDazCCAlOgAwIBAgIUe...&#10;-----END CERTIFICATE-----"
                        value={saml.sp_cert_pem ?? ""}
                        onInput={(e) =>
                          patchSaml({
                            sp_cert_pem: (e.currentTarget as HTMLTextAreaElement).value,
                          })
                        }
                        disabled={busy !== null}
                      />
                      <p className="text-xs text-muted-foreground mt-0.5">
                        Published in our SP metadata; IdP verifies our
                        signatures with this cert. Not secret.
                      </p>
                    </div>
                    <div>
                      <label className="text-xs text-muted-foreground flex items-center justify-between">
                        <span>SP Private Key (PEM)</span>
                        {saml.sp_key_pem_set ? (
                          /* shadcn: emerald-600 conveys "secret stored" status — canonical secret-management UX. */
                          <span className="text-emerald-600">•••• stored</span>
                        ) : null}
                      </label>
                      <textarea
                        className="w-full text-xs font-mono rounded-md border border-input bg-background px-2 py-1.5"
                        rows={4}
                        placeholder={
                          saml.sp_key_pem_set
                            ? "Leave blank to keep the stored key — type to rotate."
                            : "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEA...\n-----END PRIVATE KEY-----"
                        }
                        value={saml.sp_key_pem}
                        onInput={(e) =>
                          patchSaml({
                            sp_key_pem: (e.currentTarget as HTMLTextAreaElement).value,
                          })
                        }
                        disabled={busy !== null}
                      />
                      <p className="text-xs text-muted-foreground mt-0.5">
                        Secret — never echoed back after save. Both
                        PKCS#1 (RSA PRIVATE KEY) and PKCS#8 (PRIVATE
                        KEY) PEM blocks are accepted.
                      </p>
                    </div>
                  </>
                ) : null}
              </div>

              {/* v1.7.50.1d — group → role mapping. Optional; when
                  set, JIT-provisioned users get their RBAC roles from
                  the IdP's group claim. */}
              <div className="space-y-2 rounded-md border border-dashed bg-muted/30 p-3">
                <h3 className="text-xs font-medium">Group → role mapping (optional)</h3>
                <p className="text-xs text-muted-foreground -mt-1">
                  Map your IdP's group memberships to Railbase RBAC
                  roles. On every SAML signin, the user's roles are
                  synced to match the groups in their assertion.
                </p>
                <div>
                  <label className="text-xs text-muted-foreground">
                    Group attribute name
                  </label>
                  <Input
                    type="text"
                    placeholder="groups"
                    value={saml.group_attribute ?? ""}
                    onInput={(e) => patchSaml({ group_attribute: e.currentTarget.value })}
                    disabled={busy !== null}
                  />
                  <p className="text-xs text-muted-foreground mt-0.5">
                    SAML attribute carrying the user's group list.
                    Common values: <code className="font-mono px-1 py-0.5 bg-muted rounded">groups</code>,{" "}
                    <code className="font-mono px-1 py-0.5 bg-muted rounded">memberOf</code>,{" "}
                    <code className="font-mono px-1 py-0.5 bg-muted rounded">http://schemas.xmlsoap.org/claims/Group</code>{" "}
                    (AD FS).
                  </p>
                </div>
                <div>
                  <label className="text-xs text-muted-foreground">
                    Role mapping (JSON)
                  </label>
                  <textarea
                    className="w-full text-xs font-mono rounded-md border border-input bg-background px-2 py-1.5"
                    rows={4}
                    placeholder={`{\n  "engineering": "developer",\n  "admin-group": "site_admin"\n}`}
                    value={saml.role_mapping ?? ""}
                    onInput={(e) =>
                      patchSaml({
                        role_mapping: (e.currentTarget as HTMLTextAreaElement).value,
                      })
                    }
                    disabled={busy !== null}
                  />
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Maps each group name to a Railbase site-scoped role.
                    Only site-scoped roles are supported in v1.7.50;
                    tenant-scoped mapping is a future slice.
                  </p>
                </div>
              </div>
            </div>
          ) : null}
        </div>
      </section>

      {/* v1.7.51 — SCIM 2.0 inbound provisioning (RFC 7643/7644).
          Distinct from signin SSO: SCIM is the IdP pushing User /
          Group lifecycle into Railbase, not a user-facing sign-in
          method. Tokens are minted via CLI / admin UI panel, not the
          wizard — minting a long-lived bearer over a public bootstrap
          endpoint would be a security hole. */}
      <section className="space-y-2">
        <h2 className="text-sm font-medium">SCIM 2.0 (inbound provisioning)</h2>
        <p className="text-xs text-muted-foreground">
          Lets an external IdP (Okta / Azure AD / OneLogin / Auth0) POST
          Users and Groups into Railbase so HR de-provisioning flows
          through to user accounts automatically. Bearer-token auth via
          credentials minted with{" "}
          <code className="rb-mono px-1 py-0.5 bg-muted rounded">railbase scim token create</code>.
        </p>
        <div className="rounded-md border bg-card p-3 space-y-2">
          <label className="flex items-center gap-2 cursor-pointer">
            <Checkbox
              checked={!!scim.enabled}
              onCheckedChange={(v) => setScim({ ...scim, enabled: v === true })}
              disabled={busy !== null}
            />
            <span className="text-sm font-medium">Enable SCIM provisioning</span>
            {scim.tokens_active > 0 ? (
              <span className="ml-auto text-xs text-emerald-600">
                {scim.tokens_active} active token{scim.tokens_active === 1 ? "" : "s"}
              </span>
            ) : null}
          </label>
          {scim.enabled ? (
            <div className="space-y-2 pt-1">
              <div>
                <label className="text-xs text-muted-foreground">
                  Target auth-collection
                </label>
                <Input
                  type="text"
                  placeholder="users"
                  value={scim.collection ?? "users"}
                  onInput={(e) =>
                    setScim({ ...scim, collection: e.currentTarget.value })
                  }
                  disabled={busy !== null}
                />
                <p className="text-xs text-muted-foreground mt-0.5">
                  Which auth-collection's table the IdP will provision into.
                  Default <code className="rb-mono px-1 py-0.5 bg-muted rounded">users</code>.
                </p>
              </div>
              {scim.endpoint_url ? (
                <div className="rounded-md bg-muted/40 p-2 text-xs">
                  <div className="text-muted-foreground">SCIM endpoint URL (paste into your IdP):</div>
                  <div className="rb-mono mt-0.5 break-all">{scim.endpoint_url}</div>
                </div>
              ) : null}
              <div className="rounded-md border border-dashed bg-amber-50 p-2 text-xs text-amber-900">
                <div className="font-medium">Next step — mint a bearer credential:</div>
                <div className="rb-mono mt-0.5">
                  railbase scim token create --name &lt;label&gt; --collection {scim.collection ?? "users"}
                </div>
                <div className="mt-1 text-amber-800">
                  The CLI prints the token ONCE; copy it into your IdP's SCIM
                  configuration. Token management UI lands in v1.7.52+.
                </div>
              </div>
            </div>
          ) : null}
        </div>
      </section>

      {/* Empty as of v1.7.51 — LDAP + SAML + SCIM all in core. */}
      {status?.plugin_gated?.length ? (
        <section className="space-y-2">
          <h2 className="text-sm font-medium">Enterprise SSO (coming in core)</h2>
          <p className="text-xs text-muted-foreground">
            These are scheduled core slices — no external plugin needed.
            Configurable cards will appear in the version listed.
          </p>
          <div className="grid gap-2">
            {status.plugin_gated.map((p) => (
              <div
                key={p.name}
                className="flex items-center justify-between rounded-md border border-dashed bg-muted/40 px-3 py-2 text-sm"
              >
                <span>{p.display_name}</span>
                <span className="text-xs text-muted-foreground">
                  arrives in <code className="font-mono px-1 py-0.5 bg-background rounded">{p.available_in}</code>
                </span>
              </div>
            ))}
          </div>
        </section>
      ) : null}

      {err ? (
        <p
          role="alert"
          className="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2"
        >
          {err}
        </p>
      ) : null}

      <div className="flex items-center justify-between pt-2">
        <Button type="button" variant="outline" onClick={onBack} disabled={busy !== null}>
          ← Back
        </Button>
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="ghost"
            onClick={() => setShowSkipConfirm(true)}
            disabled={busy !== null}
          >
            Skip
          </Button>
          <Button type="button" onClick={onSave} disabled={busy !== null}>
            {busy === "save" ? "Saving…" : "Save & continue"}
          </Button>
        </div>
      </div>

      {/* Skip confirmation — inline pseudo-modal. Forces the operator
          to type a one-liner so the skip decision shows up in audit /
          settings later, not as a silent click-past. */}
      {showSkipConfirm ? (
        /* shadcn: amber 300/50 (light) + 800/950 (dark) is the canonical
         * warning-callout palette used across shadcn examples — intentional
         * yellow-warning semantic that should remain visible in both themes. */
        <div className="rounded-md border border-amber-300 bg-amber-50 p-3 space-y-2 dark:bg-amber-950/30 dark:border-amber-800">
          <p className="text-sm">
            Skipping leaves <strong>password</strong> as the only sign-in
            method (safe default). You can revisit this from the admin UI
            after first login.
          </p>
          <textarea
            className="w-full rounded border bg-background px-2 py-1 text-sm"
            rows={2}
            placeholder="Why are you skipping? (e.g. 'configure later via CLI')"
            value={skipReason}
            onInput={(e) => setSkipReason(e.currentTarget.value)}
          />
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              onClick={() => setShowSkipConfirm(false)}
              disabled={busy !== null}
            >
              Cancel
            </Button>
            <Button type="button" onClick={onSkip} disabled={busy !== null}>
              {busy === "skip" ? "Skipping…" : "Confirm skip"}
            </Button>
          </div>
        </div>
      ) : null}
    </Card>
  );
}

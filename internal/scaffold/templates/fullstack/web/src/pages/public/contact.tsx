import { useState } from "preact/hooks";
import { rb } from "../../api.js";

// Contact — talk-to-sales form backed by POST /api/contact (v0.4.3
// Sprint 5). Rate-limited server-side; a hidden honeypot input on
// this form trips bots before they touch the backend.

export function ContactPage() {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [company, setCompany] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState<"idle" | "ok" | "err">("idle");
  const [errMsg, setErrMsg] = useState<string | null>(null);

  const submit = async (e: Event) => {
    e.preventDefault();
    setBusy(true);
    setStatus("idle");
    setErrMsg(null);
    try {
      // `website` honeypot stays empty in real submissions — the
      // server interprets a non-empty value as a bot and returns 202
      // without sending the email. We intentionally don't render the
      // input via state; a hidden <input> with autocomplete=off is
      // enough to catch most form-spam crawlers.
      await rb.contact.submit({
        name, email, company,
        subject: "Sales inquiry",
        message,
      });
      setStatus("ok");
      setName(""); setEmail(""); setCompany(""); setMessage("");
    } catch (err: unknown) {
      setStatus("err");
      setErrMsg(err instanceof Error ? err.message : "Submission failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section class="mx-auto max-w-3xl px-6 py-20">
      <h1 class="text-4xl font-semibold tracking-tight">Contact sales</h1>
      <p class="mt-3 text-slate-600">
        Tell us about your team and we'll be in touch within one business day.
      </p>

      <form onSubmit={submit} class="mt-10 space-y-5">
        <Field label="Your name">
          <input
            type="text" required maxLength={120}
            value={name}
            onInput={(e) => setName((e.target as HTMLInputElement).value)}
            class="w-full rounded border border-slate-300 px-3 py-2"
            autoComplete="name"
          />
        </Field>
        <Field label="Email">
          <input
            type="email" required
            value={email}
            onInput={(e) => setEmail((e.target as HTMLInputElement).value)}
            class="w-full rounded border border-slate-300 px-3 py-2"
            autoComplete="email"
          />
        </Field>
        <Field label="Company">
          <input
            type="text"
            value={company}
            onInput={(e) => setCompany((e.target as HTMLInputElement).value)}
            class="w-full rounded border border-slate-300 px-3 py-2"
            autoComplete="organization"
          />
        </Field>
        <Field label="Message">
          <textarea
            required rows={6} maxLength={5000}
            value={message}
            onInput={(e) => setMessage((e.target as HTMLTextAreaElement).value)}
            class="w-full rounded border border-slate-300 px-3 py-2"
          />
        </Field>

        {/* Honeypot — invisible to humans, irresistible to crawlers. */}
        <input
          type="text" name="website" tabIndex={-1} autoComplete="off"
          aria-hidden="true"
          style="position:absolute;left:-10000px;width:1px;height:1px;overflow:hidden"
        />

        {status === "ok" ? (
          <p class="rounded bg-green-50 px-3 py-2 text-sm text-green-800">
            Thanks — we'll be in touch.
          </p>
        ) : null}
        {status === "err" ? (
          <p class="rounded bg-red-50 px-3 py-2 text-sm text-red-800">
            {errMsg ?? "Submission failed; please try again."}
          </p>
        ) : null}

        <button
          type="submit" disabled={busy}
          class="rounded bg-slate-900 px-5 py-2.5 text-sm font-medium text-white hover:bg-slate-800 disabled:opacity-50"
        >
          {busy ? "Sending…" : "Send message"}
        </button>
      </form>
    </section>
  );
}

function Field({ label, children }: { label: string; children: preact.ComponentChildren }) {
  return (
    <label class="block">
      <span class="mb-1 block text-sm font-medium text-slate-700">{label}</span>
      {children}
    </label>
  );
}

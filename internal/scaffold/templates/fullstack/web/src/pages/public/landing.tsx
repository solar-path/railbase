import { Link } from "wouter-preact";

// Landing page — hero + 3-feature grid + CTA strip. Pure layout
// scaffolding the operator's copywriter will rewrite; the structure
// is what the scaffold provides.

export function LandingPage() {
  return (
    <>
      <section class="mx-auto max-w-6xl px-6 py-24 text-center">
        <h1 class="text-balance text-5xl font-semibold tracking-tight md:text-6xl">
          Build your SaaS without rebuilding the plumbing.
        </h1>
        <p class="mx-auto mt-6 max-w-2xl text-balance text-lg text-slate-600">
          Auth, multi-tenancy, RBAC, audit log, billing, mailer. One binary,
          one Postgres, your application code on top.
        </p>
        <div class="mt-10 flex flex-wrap justify-center gap-3">
          <Link href="/login" class="rounded bg-slate-900 px-5 py-2.5 text-sm font-medium text-white hover:bg-slate-800">
            Get started
          </Link>
          <Link href="/pricing" class="rounded border border-slate-300 px-5 py-2.5 text-sm font-medium hover:bg-slate-100">
            See pricing
          </Link>
        </div>
      </section>

      <section class="border-y border-slate-200 bg-slate-50">
        <div class="mx-auto grid max-w-6xl gap-8 px-6 py-16 md:grid-cols-3">
          <Feature
            title="Multi-tenant out of the box"
            body="Workspaces, members, invites, roles — wired to your auth collection and ready to ship."
          />
          <Feature
            title="Audit everything"
            body="Every signin, every record write, every role change lands in a tamper-evident chain."
          />
          <Feature
            title="One binary, all batteries"
            body="Embedded Postgres for dev, your own Postgres in production. Same code path either way."
          />
        </div>
      </section>

      <section class="mx-auto max-w-6xl px-6 py-20">
        <div class="rounded-2xl bg-slate-900 px-8 py-12 text-center text-white">
          <h2 class="text-2xl font-semibold">Ready when you are.</h2>
          <p class="mt-3 text-slate-300">
            Sign up in 30 seconds — your first workspace is on us.
          </p>
          <div class="mt-6 flex justify-center gap-3">
            <Link href="/login" class="rounded bg-white px-5 py-2.5 text-sm font-medium text-slate-900 hover:bg-slate-100">
              Sign in
            </Link>
            <Link href="/contact" class="rounded border border-white/30 px-5 py-2.5 text-sm font-medium hover:bg-white/10">
              Talk to sales
            </Link>
          </div>
        </div>
      </section>
    </>
  );
}

function Feature({ title, body }: { title: string; body: string }) {
  return (
    <div>
      <div class="mb-3 inline-flex h-10 w-10 items-center justify-center rounded bg-slate-900 text-white">★</div>
      <h3 class="font-semibold">{title}</h3>
      <p class="mt-1 text-sm text-slate-600">{body}</p>
    </div>
  );
}

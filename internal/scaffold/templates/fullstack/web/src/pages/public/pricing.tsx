import { Link } from "wouter-preact";

// Pricing — 3 tiers with feature lists. Operator edits numbers + copy.
// The plans here mirror the air/rail starter so the scaffold renders
// to something meaningful even if the user never touches it.

type Plan = {
  name: string;
  price: string;
  cadence: string;
  cta: string;
  highlights: string[];
  recommended?: boolean;
};

const PLANS: Plan[] = [
  {
    name: "Free",
    price: "$0",
    cadence: "forever",
    cta: "Start free",
    highlights: ["1 workspace", "Up to 3 members", "Community support"],
  },
  {
    name: "Team",
    price: "$29",
    cadence: "per workspace / month",
    cta: "Start trial",
    highlights: ["Unlimited members", "Custom roles", "Audit log retention 90 days", "Email support"],
    recommended: true,
  },
  {
    name: "Enterprise",
    price: "Custom",
    cadence: "annual",
    cta: "Talk to sales",
    highlights: ["SAML SSO", "SCIM provisioning", "Dedicated support", "Audit log retention 1 year"],
  },
];

export function PricingPage() {
  return (
    <section class="mx-auto max-w-6xl px-6 py-20">
      <div class="text-center">
        <h1 class="text-4xl font-semibold tracking-tight">Pricing</h1>
        <p class="mx-auto mt-4 max-w-xl text-slate-600">
          Simple, transparent pricing. Cancel anytime. No "contact us for the small print".
        </p>
      </div>

      <div class="mt-12 grid gap-6 md:grid-cols-3">
        {PLANS.map((p) => (
          <div
            key={p.name}
            class={`rounded-2xl border p-6 ${
              p.recommended ? "border-slate-900 shadow-lg" : "border-slate-200"
            }`}
          >
            <div class="flex items-center justify-between">
              <h2 class="text-xl font-semibold">{p.name}</h2>
              {p.recommended ? (
                <span class="rounded-full bg-slate-900 px-2 py-0.5 text-xs text-white">Popular</span>
              ) : null}
            </div>
            <div class="mt-4">
              <span class="text-4xl font-semibold">{p.price}</span>
              <span class="ml-1 text-sm text-slate-500">{p.cadence}</span>
            </div>
            <ul class="mt-6 space-y-2 text-sm">
              {p.highlights.map((h) => (
                <li key={h} class="flex gap-2"><span>✓</span>{h}</li>
              ))}
            </ul>
            <Link
              href={p.name === "Enterprise" ? "/contact" : "/login"}
              class={`mt-6 block rounded px-4 py-2 text-center text-sm font-medium ${
                p.recommended
                  ? "bg-slate-900 text-white hover:bg-slate-800"
                  : "border border-slate-300 hover:bg-slate-100"
              }`}
            >
              {p.cta}
            </Link>
          </div>
        ))}
      </div>
    </section>
  );
}

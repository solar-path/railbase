import { Link, useParams } from "wouter-preact";

// Docs — minimal markdown-free static-doc shell. The operator
// fills the doc body with their real legal copy; the scaffold ships
// the structure (index, privacy, terms, cookies) so each slug
// already routes.

const DOCS: Record<string, { title: string; body: preact.ComponentChildren }> = {
  privacy: {
    title: "Privacy policy",
    body: (
      <>
        <p>We collect only what's required to operate the service.</p>
        <p>Replace this placeholder with your actual privacy policy before going live.</p>
      </>
    ),
  },
  terms: {
    title: "Terms of service",
    body: (
      <>
        <p>By using this service you agree to these terms.</p>
        <p>Replace this placeholder with your actual terms before going live.</p>
      </>
    ),
  },
  cookies: {
    title: "Cookie policy",
    body: (
      <>
        <p>We use a single session cookie for authentication.</p>
        <p>Replace this placeholder with your actual cookie disclosure before going live.</p>
      </>
    ),
  },
};

export function DocsPage() {
  const params = useParams<{ slug?: string }>();
  const slug = params.slug;

  if (!slug) {
    return (
      <section class="mx-auto max-w-3xl px-6 py-20">
        <h1 class="text-4xl font-semibold tracking-tight">Documentation</h1>
        <ul class="mt-8 space-y-2">
          {Object.entries(DOCS).map(([k, v]) => (
            <li key={k}>
              <Link href={`/docs/${k}`} class="text-slate-700 underline hover:text-slate-900">
                {v.title}
              </Link>
            </li>
          ))}
        </ul>
      </section>
    );
  }

  const doc = DOCS[slug];
  if (!doc) {
    return (
      <section class="mx-auto max-w-3xl px-6 py-20">
        <h1 class="text-4xl font-semibold tracking-tight">Not found</h1>
        <p class="mt-3 text-slate-600">No document at <code>/docs/{slug}</code>.</p>
        <Link href="/docs" class="mt-6 inline-block text-slate-700 underline">Back to docs</Link>
      </section>
    );
  }
  return (
    <article class="mx-auto max-w-3xl px-6 py-20 prose">
      <h1 class="text-4xl font-semibold tracking-tight">{doc.title}</h1>
      <div class="mt-8 space-y-4 text-slate-700">{doc.body}</div>
      <Link href="/docs" class="mt-10 inline-block text-sm text-slate-500 underline">← All docs</Link>
    </article>
  );
}

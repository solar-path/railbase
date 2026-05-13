import { useSignal } from "@preact/signals";
import { Link } from "wouter-preact";
import { useQuery } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { signin } from "../auth/context";
import { isAPIError } from "../api/client";
import { Button } from "@/lib/ui/button.ui";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Input } from "@/lib/ui/input.ui";
import { PasswordInput } from "@/lib/ui/password.ui";
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/lib/ui/form.ui";

// Login screen — first admin consumer of kit's <Form> + react-hook-form
// pattern (v1.7.41). Reference implementation for downstream apps that
// lift the kit: this is what a "shadcn-style form on Preact" looks like.
//
// The setup:
//   - Schema declared with zod; type comes from z.infer<typeof schema>
//   - useForm() owns form state (replaces useSignal() per-field)
//   - <Form {...form}> + <FormField name=... render={({field}) => ...}>
//     gives each input access to the RHF context (error tracking, dirty
//     flags, focus management, ARIA wiring)
//   - <FormMessage/> renders zod errors automatically — no manual error
//     state plumbing
//   - busy / err remain as useSignal() — they're transient UX state, not
//     form state, and don't belong in the form schema
//
// Preact compat note: react-hook-form wires onChange handlers. Preact-
// native onChange fires on blur, BUT preact/compat patches it to React-
// style on-input semantics. We pass field.value + field.onChange through
// the {...field} spread for Input; PasswordInput keeps the explicit
// `onInput → field.onChange(value)` wiring because its onInput handler
// expects raw events and PasswordInput's signature is more verbose than
// plain Input.

const loginSchema = z.object({
  email: z.string().email("Enter a valid email"),
  password: z.string().min(1, "Password required"),
});

type LoginValues = z.infer<typeof loginSchema>;

export function LoginScreen() {
  const busy = useSignal(false);
  const err = useSignal<string | null>(null);

  // Probe whether the install has zero admins so the "create one via
  // CLI" hint only shows in that genuinely-empty state (v1.7.46). The
  // probe is the same one LoginGate uses; TanStack Query dedupes.
  const bootstrapProbe = useQuery({
    queryKey: ["bootstrap-probe"],
    queryFn: async () => {
      try {
        return await fetch("/api/_admin/_bootstrap").then((r) => r.json());
      } catch {
        return { needsBootstrap: false } as { needsBootstrap: boolean };
      }
    },
    staleTime: 60_000,
    retry: false,
  });
  const noAdminsYet = bootstrapProbe.data?.needsBootstrap === true;

  const form = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { email: "", password: "" },
    mode: "onSubmit",
  });

  async function onSubmit(values: LoginValues) {
    err.value = null;
    busy.value = true;
    try {
      await signin(values.email, values.password);
    } catch (e) {
      err.value = isAPIError(e) ? e.message : "Sign-in failed.";
    } finally {
      busy.value = false;
    }
  }

  return (
    // Pre-auth login form — full-viewport centered shell, intentionally
    // not <AdminPage> (no sidebar / header context). docs/12 §Layout
    // whitelists pre-auth screens.
    // eslint-disable-next-line railbase/no-raw-page-shell
    <div class="min-h-screen flex items-center justify-center bg-muted p-6">
      <Card class="w-full max-w-sm">
        <CardHeader class="space-y-1">
          <CardTitle class="text-xl">Railbase admin</CardTitle>
          <CardDescription>Sign in to continue.</CardDescription>
        </CardHeader>
        <CardContent>
          <Form {...form}>
            <form onSubmit={form.handleSubmit(onSubmit)} class="space-y-4">
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
                        {...field}
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
                        autoComplete="current-password"
                        value={field.value}
                        onInput={(e) =>
                          field.onChange(e.currentTarget.value)
                        }
                        // Sign-in form: eye toggle only — no
                        // showStrength / showGenerate. Operator is
                        // typing a value they already chose; second-
                        // guessing it adds friction.
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {err.value ? (
                <p
                  role="alert"
                  class="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded px-3 py-2"
                >
                  {err.value}
                </p>
              ) : null}

              <Button
                type="submit"
                disabled={busy.value || form.formState.isSubmitting}
                class="w-full"
              >
                {busy.value ? "Signing in…" : "Sign in"}
              </Button>

              <p class="text-xs text-muted-foreground text-center">
                <Link href="/forgot-password" class="underline">
                  Forgot password?
                </Link>
              </p>

              {noAdminsYet ? (
                <p class="text-xs text-muted-foreground">
                  No admins yet? Create one with{" "}
                  <code class="font-mono px-1 py-0.5 bg-muted rounded">
                    railbase admin create &lt;email&gt;
                  </code>
                  {" "}or use the bootstrap wizard.
                </p>
              ) : null}
            </form>
          </Form>
        </CardContent>
      </Card>
    </div>
  );
}

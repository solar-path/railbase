import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { useLocation, Link } from "wouter-preact";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import { Button } from "@/lib/ui/button.ui";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { PasswordInput } from "@/lib/ui/password.ui";
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/lib/ui/form.ui";

// ResetPasswordScreen — consumes the single-use token from the
// `?token=...` query param and lets the operator pick a new password.
// On success: revokes every live session for the admin (server-side)
// AND redirects to /login so the operator signs back in clean.
//
// Token-absent state: render a fallback explaining the link came from
// the password-reset email; if they got here directly, route them to
// /forgot-password to request a fresh one.

// Min-8 + at-least-1 upper / digit / symbol matches the bootstrap
// wizard's admin password rule. Identical schema means an operator
// can't pick a weaker password via reset than they could at create.
const schema = z
  .object({
    password: z
      .string()
      .min(8, "Min 8 characters")
      .regex(/[A-Z]/, "Need uppercase")
      .regex(/[0-9]/, "Need digit")
      .regex(/[^A-Za-z0-9]/, "Need symbol"),
    confirm: z.string(),
  })
  .refine((d) => d.password === d.confirm, {
    message: "Passwords don't match",
    path: ["confirm"],
  });

type Values = z.infer<typeof schema>;

export function ResetPasswordScreen() {
  const [, navigate] = useLocation();
  const token = useSignal<string>("");
  const busy = useSignal(false);
  const err = useSignal<string | null>(null);
  const success = useSignal(false);

  // Read ?token=... from the URL on mount. wouter-preact's location
  // hook covers PATH but not search; we read window.location.search
  // directly — pre-auth screen, no SSR concerns.
  useEffect(() => {
    try {
      const params = new URLSearchParams(window.location.search);
      token.value = params.get("token") ?? "";
    } catch {
      // Malformed URL — token stays empty, fallback renders.
    }
     
  }, []);

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { password: "", confirm: "" },
    mode: "onSubmit",
  });

  async function onSubmit(values: Values) {
    err.value = null;
    busy.value = true;
    try {
      await adminAPI.resetPassword(token.value, values.password);
      success.value = true;
      // Short delay so the operator sees the success message before
      // the redirect — feels less like a click-and-jump.
      setTimeout(() => navigate("/login"), 1500);
    } catch (e) {
      err.value = isAPIError(e) ? e.message : "Reset failed.";
    } finally {
      busy.value = false;
    }
  }

  return (
    // eslint-disable-next-line railbase/no-raw-page-shell
    <div class="min-h-screen flex items-center justify-center bg-muted p-6">
      <Card class="w-full max-w-md">
        <CardHeader class="space-y-1">
          <CardTitle class="text-xl">Set a new password</CardTitle>
          <CardDescription>
            {token.value
              ? "Pick a strong password. This link can only be used once."
              : "This page expects a reset token in the URL."}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {!token.value ? (
            <div class="space-y-3 text-sm">
              <p>
                No reset token in the URL. Reset links arrive by email
                — open the link in that email, or request a new one.
              </p>
              <Link
                href="/forgot-password"
                class="block text-center text-sm underline"
              >
                Request a reset link
              </Link>
            </div>
          ) : success.value ? (
            <div class="space-y-3 text-sm">
              <p class="text-foreground">
                Password updated. Redirecting to sign in…
              </p>
            </div>
          ) : (
            <Form {...form}>
              <form onSubmit={form.handleSubmit(onSubmit)} class="space-y-4">
                <FormField
                  control={form.control}
                  name="password"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>New password</FormLabel>
                      <FormControl>
                        <PasswordInput
                          autoComplete="new-password"
                          autoFocus
                          showStrength
                          value={field.value}
                          onInput={(e) =>
                            field.onChange(e.currentTarget.value)
                          }
                        />
                      </FormControl>
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
                          onInput={(e) =>
                            field.onChange(e.currentTarget.value)
                          }
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
                  {busy.value ? "Resetting…" : "Set new password"}
                </Button>
                <p class="text-xs text-muted-foreground text-center">
                  <Link href="/login" class="underline">
                    Back to sign in
                  </Link>
                </p>
              </form>
            </Form>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

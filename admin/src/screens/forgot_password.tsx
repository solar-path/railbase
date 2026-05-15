import { useMemo } from "react";
import { useSignal } from "@preact/signals";
import { Link } from "wouter-preact";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import { useT } from "../i18n";
import { Button } from "@/lib/ui/button.ui";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Input } from "@/lib/ui/input.ui";
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/lib/ui/form.ui";

// ForgotPasswordScreen — pre-auth password-reset request (v1.7.46).
//
// Flow:
//   1. Operator enters their admin email
//   2. POST /api/_admin/forgot-password
//   3a. Backend responds 200 (always) → show "check inbox" success state
//   3b. Backend responds 503 with `error: mailer_not_configured` → show
//       the CLI escape-hatch hint inline (operator likely never ran the
//       mailer wizard step; their server has no way to email them)
//
// Notes:
//   - We do NOT echo back whether the email exists. That's the
//     backend's anti-enumeration contract; the UI just reflects it.
//   - The screen is reachable at /_/forgot-password (anon route).
//   - Pre-auth shell: bypass the no-raw-page-shell ESLint rule with
//     the same pragma login.tsx uses.

type Values = { email: string };

type Phase =
  | { kind: "form" }
  | { kind: "sent"; email: string }
  | { kind: "mailer-down" };

export function ForgotPasswordScreen() {
  const { t } = useT();
  const phase = useSignal<Phase>({ kind: "form" });
  const err = useSignal<string | null>(null);
  const busy = useSignal(false);

  const schema = useMemo(
    () =>
      z.object({
        email: z.string().email(t("login.emailInvalid")),
      }),
    [t],
  );

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { email: "" },
    mode: "onSubmit",
  });

  async function onSubmit(values: Values) {
    err.value = null;
    busy.value = true;
    try {
      await adminAPI.forgotPassword(values.email);
      phase.value = { kind: "sent", email: values.email };
    } catch (e) {
      if (isAPIError(e) && e.code === "unavailable") {
        // Backend signals mailer-not-configured via CodeUnavailable
        // (HTTP 503). Show the inline CLI hint instead of a raw error.
        phase.value = { kind: "mailer-down" };
      } else {
        err.value = isAPIError(e) ? e.message : t("forgotPassword.requestFailed");
      }
    } finally {
      busy.value = false;
    }
  }

  return (
    // Pre-auth shell — see login.tsx for the same pattern.
    // eslint-disable-next-line railbase/no-raw-page-shell
    <div class="min-h-screen flex items-center justify-center bg-muted p-6">
      <Card class="w-full max-w-md">
        <CardHeader class="space-y-1">
          <CardTitle class="text-xl">{t("forgotPassword.title")}</CardTitle>
          <CardDescription>
            {t("forgotPassword.subtitle")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {phase.value.kind === "form" ? (
            <Form {...form}>
              <form onSubmit={form.handleSubmit(onSubmit)} class="space-y-4">
                <FormField
                  control={form.control}
                  name="email"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t("forgotPassword.emailLabel")}</FormLabel>
                      <FormControl>
                        <Input
                          type="email"
                          autoComplete="email"
                          autoFocus
                          {...field}
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
                  {busy.value ? t("forgotPassword.sending") : t("forgotPassword.submit")}
                </Button>
                <p class="text-xs text-muted-foreground text-center">
                  <Link href="/login" class="underline">
                    {t("forgotPassword.backToSignIn")}
                  </Link>
                </p>
              </form>
            </Form>
          ) : phase.value.kind === "sent" ? (
            <div class="space-y-3 text-sm">
              <p class="text-foreground">
                {t("forgotPassword.sentPart1")} <strong>{phase.value.email}</strong> {t("forgotPassword.sentPart2")}
              </p>
              <p class="text-xs text-muted-foreground">
                {t("forgotPassword.sentValidity")}
              </p>
              <Link href="/login" class="block text-center text-sm underline">
                {t("forgotPassword.backToSignIn")}
              </Link>
            </div>
          ) : (
            // mailer-down: show CLI escape hatch
            <div class="space-y-4 text-sm">
              <div class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-destructive">
                <p class="font-medium">{t("forgotPassword.mailerDownTitle")}</p>
                <p class="text-xs mt-1 text-foreground">
                  {t("forgotPassword.mailerDownDesc")}
                </p>
              </div>
              <pre class="font-mono text-xs bg-muted rounded px-3 py-2 overflow-x-auto">
                railbase admin reset-password &lt;email&gt;
              </pre>
              <p class="text-xs text-muted-foreground">
                {t("forgotPassword.mailerDownFollowup")}{" "}
                <code class="font-mono">{t("forgotPassword.mailerDownSettingsPath")}</code>.
              </p>
              <Link href="/login" class="block text-center text-sm underline">
                {t("forgotPassword.backToSignIn")}
              </Link>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

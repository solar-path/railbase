import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { adminAPI } from "../api/admin";
import { isAPIError } from "../api/client";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Button } from "@/lib/ui/button.ui";
import { Input } from "@/lib/ui/input.ui";
import { Textarea } from "@/lib/ui/textarea.ui";
import { Alert, AlertDescription } from "@/lib/ui/alert.ui";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/lib/ui/form.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";

// Settings panel — single-screen list + add/edit/delete. The
// underlying _settings table stores arbitrary JSONB values, so the
// UI is generic: a textarea for the value, validated client-side
// before submit.
//
// Form pattern: react-hook-form + zod, mirroring the login.tsx
// reference. The `value` field stays a free-form string; we coerce
// it per the picked `type` in onSubmit. JSON parsing failures are
// reported via form.setError so they render under the field rather
// than a top-level banner.

const settingsSchema = z.object({
  key: z
    .string()
    .min(1, "Key required")
    .regex(
      /^[a-z][a-z0-9._-]*$/,
      "Lowercase letters, digits, dots, dashes, underscores only (must start with a letter).",
    ),
  value: z.string(),
  type: z.enum(["string", "int", "bool", "json"]),
});

type SettingsValues = z.infer<typeof settingsSchema>;

const defaultValues: SettingsValues = {
  key: "",
  value: '""',
  type: "json",
};

export function SettingsScreen() {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["settings"], queryFn: () => adminAPI.settingsList() });

  // Form-wide error banner. Field-level errors live on the form
  // itself via form.setError; this is only for transport / server
  // failures that don't map onto a single field.
  const [err, setErr] = useState<string | null>(null);

  const form = useForm<SettingsValues>({
    resolver: zodResolver(settingsSchema),
    defaultValues,
    mode: "onSubmit",
  });

  const setMu = useMutation({
    mutationFn: ({ key, value }: { key: string; value: unknown }) =>
      adminAPI.settingsSet(key, value),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["settings"] }),
  });
  const delMu = useMutation({
    mutationFn: (key: string) => adminAPI.settingsDelete(key),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["settings"] }),
  });

  async function onSubmit(values: SettingsValues) {
    setErr(null);
    let parsed: unknown;
    switch (values.type) {
      case "string":
        parsed = values.value;
        break;
      case "int": {
        const n = Number(values.value);
        if (!Number.isFinite(n) || !Number.isInteger(n)) {
          form.setError("value", {
            type: "manual",
            message: "value must be an integer",
          });
          return;
        }
        parsed = n;
        break;
      }
      case "bool": {
        const v = values.value.trim().toLowerCase();
        if (v !== "true" && v !== "false") {
          form.setError("value", {
            type: "manual",
            message: 'value must be "true" or "false"',
          });
          return;
        }
        parsed = v === "true";
        break;
      }
      case "json":
      default:
        try {
          parsed = JSON.parse(values.value);
        } catch {
          form.setError("value", {
            type: "manual",
            message:
              "value must be valid JSON (string, number, bool, object, etc.)",
          });
          return;
        }
        break;
    }

    setMu.mutate(
      { key: values.key.trim(), value: parsed },
      {
        onSuccess: () => {
          form.reset(defaultValues);
        },
        onError: (e) => setErr(isAPIError(e) ? e.message : "Failed to save."),
      },
    );
  }

  return (
    <div class="space-y-6 max-w-3xl">
      <header>
        <h1 class="text-2xl font-semibold">Settings</h1>
        <p class="text-sm text-muted-foreground">
          Key/value entries persisted in <code class="rb-mono">_settings</code>.
          Values are arbitrary JSON.
        </p>
      </header>

      <Card>
        <CardHeader class="border-b px-4 py-2 space-y-0">
          <CardTitle class="text-sm font-medium">Add or update a key</CardTitle>
        </CardHeader>
        <CardContent class="p-4">
          <Form {...form}>
            <form onSubmit={form.handleSubmit(onSubmit)} class="space-y-3">
              <FormField
                control={form.control}
                name="key"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Key</FormLabel>
                    <FormControl>
                      <Input
                        type="text"
                        placeholder="feature.dark_mode"
                        class="rb-mono"
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name="type"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Type</FormLabel>
                    <FormControl>
                      <select
                        class="h-9 w-full rounded border border-input bg-transparent px-2 text-sm"
                        {...field}
                      >
                        <option value="json">json</option>
                        <option value="string">string</option>
                        <option value="int">int</option>
                        <option value="bool">bool</option>
                      </select>
                    </FormControl>
                    <FormDescription>
                      Server stores everything as JSONB; this picker only
                      affects how the textarea content is coerced before
                      submit.
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name="value"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Value</FormLabel>
                    <FormControl>
                      <Textarea rows={4} class="rb-mono" {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {err ? (
                <Alert variant="destructive">
                  <AlertDescription>{err}</AlertDescription>
                </Alert>
              ) : null}

              <Button type="submit" disabled={setMu.isPending}>
                {setMu.isPending ? "Saving…" : "Save"}
              </Button>
            </form>
          </Form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="border-b px-4 py-2 space-y-0">
          <CardTitle class="text-sm font-medium">Current settings</CardTitle>
        </CardHeader>
        <CardContent class="p-0">
          {list.isLoading ? (
            <p class="px-4 py-3 text-sm text-muted-foreground">Loading…</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>key</TableHead>
                  <TableHead>value</TableHead>
                  <TableHead class="w-32"></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(list.data?.items ?? []).map((row) => (
                  <TableRow key={row.key}>
                    <TableCell class="rb-mono">{row.key}</TableCell>
                    <TableCell>
                      <pre class="rb-mono text-xs whitespace-pre-wrap break-all">
                        {JSON.stringify(row.value)}
                      </pre>
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => delMu.mutate(row.key)}
                        disabled={delMu.isPending}
                        class="text-destructive hover:text-destructive"
                      >
                        Delete
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
                {list.data?.items.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={3} class="text-muted-foreground text-center py-4">
                      No settings yet.
                    </TableCell>
                  </TableRow>
                ) : null}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

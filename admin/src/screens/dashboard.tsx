import { useQuery } from "@tanstack/react-query";
import { adminAPI } from "../api/admin";
import { Link } from "wouter-preact";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/lib/ui/card.ui";
import { Badge } from "@/lib/ui/badge.ui";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/lib/ui/table.ui";

// Dashboard — minimal v0.8 cut: collection count, recent audit
// events, links to deep screens. The "stats cards / health checks /
// charts" rich variant from docs/12 §Dashboard lands in v1 along
// with the metrics endpoint.

export function DashboardScreen() {
  const schemaQ = useQuery({ queryKey: ["schema"], queryFn: () => adminAPI.schema() });
  const auditQ = useQuery({
    queryKey: ["audit", { perPage: 10 }],
    queryFn: () => adminAPI.audit({ perPage: 10 }),
  });

  return (
    <div class="space-y-6">
      <header>
        <h1 class="text-2xl font-semibold">Dashboard</h1>
        <p class="text-sm text-muted-foreground">Quick health overview.</p>
      </header>

      <section class="grid grid-cols-2 md:grid-cols-4 gap-3">
        <StatCard label="Collections" value={schemaQ.data?.count ?? "—"} href="/schema" />
        <StatCard label="Audit events" value={auditQ.data?.totalItems ?? "—"} href="/audit" />
        <StatCard label="Settings" value="↗" href="/settings" />
        <StatCard label="Docs" value="↗" href="https://github.com/railbase/railbase" external />
      </section>

      <section class="space-y-2">
        <h2 class="text-sm font-medium text-foreground">Recent audit events</h2>
        <Card>
          <CardContent class="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>seq</TableHead>
                  <TableHead>event</TableHead>
                  <TableHead>outcome</TableHead>
                  <TableHead>at</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(auditQ.data?.items ?? []).slice(0, 10).map((e) => (
                  <TableRow key={e.seq}>
                    <TableCell class="rb-mono">{e.seq}</TableCell>
                    <TableCell class="rb-mono">{e.event}</TableCell>
                    <TableCell>
                      <Badge variant={outcomeVariant(e.outcome)}>{e.outcome}</Badge>
                    </TableCell>
                    <TableCell class="rb-mono text-muted-foreground">{e.at}</TableCell>
                  </TableRow>
                ))}
                {auditQ.data?.items.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={4} class="text-muted-foreground text-center py-4">
                      No events yet.
                    </TableCell>
                  </TableRow>
                ) : null}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </section>
    </div>
  );
}

function StatCard({
  label,
  value,
  href,
  external,
}: {
  label: string;
  value: string | number;
  href: string;
  external?: boolean;
}) {
  const inner = (
    <Card class="transition-colors hover:border-ring">
      <CardHeader class="p-3 pb-1 space-y-0">
        <CardDescription class="text-xs">{label}</CardDescription>
      </CardHeader>
      <CardContent class="p-3 pt-1">
        <CardTitle class="text-2xl">{value}</CardTitle>
      </CardContent>
    </Card>
  );
  if (external) {
    return (
      <a href={href} target="_blank" rel="noreferrer">
        {inner}
      </a>
    );
  }
  return <Link href={href}>{inner}</Link>;
}

function outcomeVariant(o: string): "default" | "secondary" | "destructive" | "outline" {
  switch (o) {
    case "success": return "secondary";
    case "denied":  return "outline";
    case "failed":  return "destructive";
    case "error":   return "destructive";
    default:        return "outline";
  }
}

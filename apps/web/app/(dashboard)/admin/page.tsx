"use client";

import { useCallback, useEffect, useState } from "react";
import { useAuth } from "@/contexts/auth-context";
import { DashboardStatCard } from "@/components/dashboard/dashboard-stat-card";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import {
  adminLookup,
  adminUpdateFlags,
  fetchAdminMetrics,
  fetchPrivilegedAccounts,
  type AdminLookupResult,
  type AdminPlatformMetrics,
  type AdminPrivilegedAccountRow,
} from "@/lib/admin-api";
import {
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import Link from "next/link";
import { ShieldAlertIcon } from "lucide-react";
import dynamic from "next/dynamic";
import { glassSurfaceClasses } from "@/lib/glass";
import { cn } from "@/lib/utils";

const MetricsTimeseriesCharts = dynamic(
  () => import("@/components/dashboard/metrics-timeseries-charts").then((module) => module.MetricsTimeseriesCharts),
  { loading: () => <p role="status" className="text-muted-foreground text-sm">Loading charts…</p> },
);

type IdKind = "github_login" | "github_id" | "email";

/** Relative time since override was granted (for audit list). */
function formatHeldSince(iso: string | null | undefined): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const sec = Math.floor((Date.now() - t) / 1000);
  if (sec < 0) return "just now";
  const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
  if (sec < 60) return rtf.format(-sec, "second");
  const min = Math.floor(sec / 60);
  if (min < 60) return rtf.format(-min, "minute");
  const hr = Math.floor(min / 60);
  if (hr < 48) return rtf.format(-hr, "hour");
  const day = Math.floor(hr / 24);
  if (day < 60) return rtf.format(-day, "day");
  const month = Math.floor(day / 30);
  return rtf.format(-month, "month");
}

function overridesSummary(row: AdminPrivilegedAccountRow): string {
  const parts: string[] = [];
  if (row.is_admin) parts.push("Admin");
  return parts.length ? parts.join(" · ") : "—";
}

export default function AdminPage() {
  const { user, isLoading } = useAuth();
  const [metrics, setMetrics] = useState<AdminPlatformMetrics | null>(null);
  const [metricsError, setMetricsError] = useState<string | null>(null);
  const [metricsLoading, setMetricsLoading] = useState(false);

  const [idKind, setIdKind] = useState<IdKind>("github_login");
  const [idInput, setIdInput] = useState("");
  const [lookupLoading, setLookupLoading] = useState(false);
  const [lookupError, setLookupError] = useState<string | null>(null);
  const [lookupResult, setLookupResult] = useState<AdminLookupResult | null>(
    null
  );

  const [actionMessage, setActionMessage] = useState<string | null>(null);
  const [actionLoading, setActionLoading] = useState(false);

  const [privileged, setPrivileged] = useState<AdminPrivilegedAccountRow[]>([]);
  const [privilegedLoading, setPrivilegedLoading] = useState(false);
  const [privilegedError, setPrivilegedError] = useState<string | null>(null);

  const loadMetrics = useCallback(async () => {
    setMetricsLoading(true);
    setMetricsError(null);
    try {
      const m = await fetchAdminMetrics();
      setMetrics(m);
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) {
        setMetricsError("You do not have access to admin metrics.");
      } else {
        setMetricsError(
          e instanceof ApiError ? formatApiErrorDetail(e.body) : "Load failed."
        );
      }
      setMetrics(null);
    } finally {
      setMetricsLoading(false);
    }
  }, []);

  const loadPrivileged = useCallback(async () => {
    setPrivilegedLoading(true);
    setPrivilegedError(null);
    try {
      const rows = await fetchPrivilegedAccounts();
      setPrivileged(rows);
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) {
        setPrivilegedError("You do not have access.");
      } else {
        setPrivilegedError(
          e instanceof ApiError ? formatApiErrorDetail(e.body) : "Load failed."
        );
      }
      setPrivileged([]);
    } finally {
      setPrivilegedLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!user?.is_admin) return;
    queueMicrotask(() => {
      void loadMetrics();
      void loadPrivileged();
    });
  }, [user?.is_admin, loadMetrics, loadPrivileged]);

  const runLookup = async () => {
    setLookupLoading(true);
    setLookupError(null);
    setLookupResult(null);
    setActionMessage(null);
    try {
      const trimmed = idInput.trim();
      if (!trimmed) {
        setLookupError("Enter a value to look up.");
        return;
      }
      let body:
        | { github_login: string }
        | { github_id: string }
        | { email: string };
      if (idKind === "github_login") {
        body = { github_login: trimmed };
      } else if (idKind === "github_id") {
        body = { github_id: trimmed };
      } else {
        body = { email: trimmed.toLowerCase() };
      }
      const res = await adminLookup(body);
      setLookupResult(res);
    } catch (e) {
      if (e instanceof ApiError) {
        if (e.status === 404) {
          setLookupError("No account found for that identifier.");
        } else {
          setLookupError(formatApiErrorDetail(e.body));
        }
      } else {
        setLookupError("Lookup failed.");
      }
    } finally {
      setLookupLoading(false);
    }
  };

  const runFlagUpdate = async (patch: {
    is_admin?: boolean;
  }) => {
    if (!lookupResult) return;
    setActionLoading(true);
    setActionMessage(null);
    try {
      const updated = await adminUpdateFlags({
        account_id: lookupResult.account_id,
        ...patch,
      });
      setLookupResult(updated);
      setActionMessage("Saved.");
      await loadMetrics();
      await loadPrivileged();
    } catch (e) {
      setActionMessage(
        e instanceof ApiError ? formatApiErrorDetail(e.body) : "Update failed."
      );
    } finally {
      setActionLoading(false);
    }
  };

  if (isLoading) {
    return (
      <div className="mx-auto max-w-4xl space-y-6">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  if (!user?.is_admin) {
    return (
      <div className="mx-auto max-w-lg space-y-4">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <ShieldAlertIcon className="size-5" />
              Not Authorized
            </CardTitle>
            <CardDescription>
              Admin tools are restricted to platform administrators.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Button nativeButton={false} render={<Link href="/dashboard" />}>
              Back to Overview
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-6xl space-y-8">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Admin</h1>
        <p className="text-muted-foreground mt-1">
          Platform-wide aggregates, a directory of administrators, and single-account lookup. Lookup by identifier still returns
          only id and flags (no email or profile).
        </p>
      </div>

      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-lg font-semibold">Platform Metrics</h2>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => void loadMetrics()}
            disabled={metricsLoading}
          >
            {metricsLoading ? "Loading…" : "Refresh"}
          </Button>
        </div>
        {metricsError ? (
          <p className="text-destructive text-sm">{metricsError}</p>
        ) : null}
        <div className="grid gap-3 sm:grid-cols-3">
          <DashboardStatCard
            title="Total Users"
            value={
              metrics ? String(metrics.total_users) : metricsLoading ? "—" : "—"
            }
          />
          <DashboardStatCard
            title="Total Projects"
            value={
              metrics ? String(metrics.total_projects) : metricsLoading ? "—" : "—"
            }
          />
          <DashboardStatCard
            title="Total MCP Calls"
            value={
              metrics
                ? String(metrics.total_mcp_calls)
                : metricsLoading
                  ? "—"
                  : "—"
            }
            hint="Count of rows in request logs (includes parse errors logged as MCP traffic)."
          />
        </div>
      </section>

      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Platform Traffic</h2>
        <p className="text-muted-foreground text-sm">
          Same charts as the dashboard, sourced from hourly aggregates (not live
          per-request scans). Refresh cadence is configured on the backend —
          see docs for scheduling rollup jobs.
        </p>
        <MetricsTimeseriesCharts variant="admin" />
      </section>

      <Card
        className={cn(
          glassSurfaceClasses("subtle"),
          "rounded-lg ring-0",
        )}
      >
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div>
              <CardTitle>Accounts With Overrides</CardTitle>
              <CardDescription>
                Accounts with platform admin access. Times are from
                when each override was last turned on (approximate for accounts
                granted before this audit field existed — backfilled from account
                creation).
              </CardDescription>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void loadPrivileged()}
              disabled={privilegedLoading}
            >
              {privilegedLoading ? "Loading…" : "Refresh"}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {privilegedError ? (
            <p className="text-destructive text-sm">{privilegedError}</p>
          ) : privilegedLoading && privileged.length === 0 ? (
            <Skeleton className="h-32 w-full" />
          ) : privileged.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              No administrator accounts.
            </p>
          ) : (
            <Table>
              <TableCaption className="sr-only">
                Administrator accounts and grant times.
              </TableCaption>
              <TableHeader>
                <TableRow>
                  <TableHead>GitHub Login</TableHead>
                  <TableHead>Overrides</TableHead>
                  <TableHead>Admin Since</TableHead>
                  <TableHead className="font-mono text-xs">Account ID</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {privileged.filter((row) => row.is_admin).map((row) => (
                  <TableRow key={row.account_id}>
                    <TableCell className="font-medium">{row.github_login}</TableCell>
                    <TableCell className="text-sm">{overridesSummary(row)}</TableCell>
                    <TableCell className="text-muted-foreground text-sm">
                      {row.is_admin ? formatHeldSince(row.admin_granted_at) : "—"}
                    </TableCell>
                    <TableCell className="font-mono text-xs break-all">
                      {row.account_id}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card
        className={cn(
          glassSurfaceClasses("subtle"),
          "rounded-lg ring-0",
        )}
      >
        <CardHeader>
          <CardTitle>Lookup and Update Flags</CardTitle>
          <CardDescription>
            Use a single identifier. Response shows only account id and current
            flags (no email or profile).
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
            <div className="grid flex-1 gap-2">
              <Label>Identifier Type</Label>
              <Select
                value={idKind}
                onValueChange={(v) => {
                  setIdKind(v as IdKind);
                  setLookupResult(null);
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="github_login">GitHub Login</SelectItem>
                  <SelectItem value="github_id">GitHub Numeric ID</SelectItem>
                  <SelectItem value="email">Email (Stored on Account)</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid min-w-0 flex-[2] gap-2">
              <Label htmlFor="admin-lookup-value">Value</Label>
              <Input
                id="admin-lookup-value"
                value={idInput}
                onChange={(e) => setIdInput(e.target.value)}
                placeholder={
                  idKind === "github_login"
                    ? "octocat"
                    : idKind === "github_id"
                      ? "583231"
                      : "user@example.com"
                }
                autoComplete="off"
              />
            </div>
            <Button
              type="button"
              onClick={() => void runLookup()}
              disabled={lookupLoading}
            >
              {lookupLoading ? "Looking up…" : "Lookup"}
            </Button>
          </div>

          {lookupError ? (
            <p className="text-destructive text-sm">{lookupError}</p>
          ) : null}

          {lookupResult ? (
            <div className="space-y-3 rounded-md border bg-muted/30 p-4">
              <p className="font-mono text-sm">
                <span className="text-muted-foreground">Account ID: </span>
                {lookupResult.account_id}
              </p>
              <p className="text-sm">
                Admin:{" "}
                <strong>{lookupResult.is_admin ? "yes" : "no"}</strong>
              </p>
              <div className="flex flex-wrap justify-center gap-2 pt-1 sm:justify-start">
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  disabled={actionLoading || lookupResult.is_admin}
                  onClick={() => void runFlagUpdate({ is_admin: true })}
                >
                  Grant Admin
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={actionLoading || !lookupResult.is_admin}
                  onClick={() => void runFlagUpdate({ is_admin: false })}
                >
                  Revoke Admin
                </Button>
              </div>
              {actionMessage ? (
                <p className="text-muted-foreground text-sm">{actionMessage}</p>
              ) : null}
            </div>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}

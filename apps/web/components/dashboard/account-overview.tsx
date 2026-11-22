"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { fetchAccountDashboardSummary } from "@/lib/projects-api";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import { Skeleton } from "@/components/ui/skeleton";
import dynamic from "next/dynamic";
import { DashboardStatCard } from "@/components/dashboard/dashboard-stat-card";
import { pluralEn } from "@/lib/pluralize";
import { DashboardMcpMethodsBreakdownCard } from "@/components/dashboard/dashboard-mcp-methods-breakdown-card";

const MetricsTimeseriesCharts = dynamic(
  () => import("@/components/dashboard/metrics-timeseries-charts").then((module) => module.MetricsTimeseriesCharts),
  { loading: () => <p role="status" className="text-muted-foreground text-sm">Loading charts…</p> },
);

function formatPct(x: number | null | undefined): string {
  if (x == null) return "—";
  return `${Math.round(x * 1000) / 10}%`;
}

export function AccountOverview() {
  const { data, isLoading, error } = useQuery({
    queryKey: ["account-dashboard-summary"],
    queryFn: fetchAccountDashboardSummary,
  });

  if (isLoading) {
    return (
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {Array.from({ length: 8 }).map((_, i) => (
          <Skeleton key={i} className="h-24 rounded-lg" />
        ))}
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm">
        <p className="font-medium text-destructive">Could not load dashboard metrics.</p>
        {error instanceof ApiError ? (
          <pre className="text-muted-foreground mt-2 max-h-40 overflow-auto whitespace-pre-wrap break-all text-xs">
            {formatApiErrorDetail(error.body) || error.message}
          </pre>
        ) : (
          <p className="text-muted-foreground mt-2 text-xs">{String(error)}</p>
        )}
      </div>
    );
  }

  if (!data) return null;

  const logWord = pluralEn(data.metrics_sample_size_last_7d, "log", "logs");
  const successHint =
    data.requests_last_7d > data.metrics_sample_size_last_7d
      ? `Based on newest ${data.metrics_sample_size_last_7d.toLocaleString()} ${logWord} in the last 7 days.`
      : "Based on MCP request logs: HTTP 2xx/3xx with no logged JSON-RPC error. Failed calls use non-success HTTP status and/or a logged error code.";

  const publishedCapsHint = [
    `${data.active_tools_total.toLocaleString()} ${pluralEn(data.active_tools_total, "tool", "tools")}`,
    `${data.active_resources_total.toLocaleString()} ${pluralEn(data.active_resources_total, "resource", "resources")}`,
    `${data.active_prompts_total.toLocaleString()} ${pluralEn(data.active_prompts_total, "prompt", "prompts")}`,
  ].join(" · ");

  const projectsHint = `${data.projects_with_active_release.toLocaleString()} ${pluralEn(data.projects_with_active_release, "project", "projects")} with an active release · ${data.projects_total.toLocaleString()} total ${pluralEn(data.projects_total, "project", "projects")}`;

  return (
    <div className="space-y-8">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <DashboardStatCard
          title="Total MCP Requests"
          value={data.total_requests.toLocaleString()}
        />
        <DashboardStatCard
          title="Last 24 Hours"
          value={data.requests_last_24h.toLocaleString()}
        />
        <DashboardStatCard
          title="Last 7 Days"
          value={data.requests_last_7d.toLocaleString()}
        />
        <DashboardStatCard
          title="Success Rate (7d)"
          value={formatPct(data.success_rate_last_7d)}
          hint={successHint}
        />
        <DashboardStatCard
          title="Average Latency (7d)"
          value={
            data.avg_latency_ms_last_7d != null
              ? `${Math.round(data.avg_latency_ms_last_7d)} ms`
              : "—"
          }
        />
        <DashboardStatCard
          title="P95 Latency (7d)"
          value={
            data.p95_latency_ms_last_7d != null
              ? `${data.p95_latency_ms_last_7d} ms`
              : "—"
          }
        />
        <DashboardStatCard
          title="Projects"
          value={`${data.projects_with_active_release} / ${data.projects_total}`}
          hint={projectsHint}
        />
        <DashboardStatCard
          title="Published Capabilities"
          value={`${data.active_tools_total + data.active_resources_total + data.active_prompts_total}`}
          hint={`${publishedCapsHint} (active releases)`}
        />
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <DashboardMcpMethodsBreakdownCard methods={data.method_breakdown_last_7d} />
        <div className="rounded-lg border p-4">
          <h3 className="font-medium">Top Projects (7d Sample)</h3>
          <ul className="mt-3 max-h-56 space-y-2 overflow-y-auto text-sm">
            {data.top_projects_last_7d.length === 0 ? (
              <li className="text-muted-foreground">No per-project traffic in sample.</li>
            ) : (
              data.top_projects_last_7d.map((row) => (
                <li key={row.project_id} className="flex items-center justify-between gap-2">
                  <Link
                    href={`/projects/${row.project_id}`}
                    className="min-w-0 truncate font-medium text-primary underline-offset-4 hover:underline"
                  >
                    {row.project_name}
                  </Link>
                  <span className="font-mono text-xs tabular-nums">
                    {row.request_count}
                  </span>
                </li>
              ))
            )}
          </ul>
        </div>
      </div>

      <MetricsTimeseriesCharts variant="account" />
    </div>
  );
}

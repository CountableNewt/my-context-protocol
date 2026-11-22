"use client";

import { useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchProjectDashboardSummary } from "@/lib/projects-api";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import { Skeleton } from "@/components/ui/skeleton";
import dynamic from "next/dynamic";
import { DashboardStatCard } from "@/components/dashboard/dashboard-stat-card";
import { pluralEn } from "@/lib/pluralize";
import { DashboardMcpMethodsBreakdownCard } from "@/components/dashboard/dashboard-mcp-methods-breakdown-card";
import { cn } from "@/lib/utils";
import type { ProjectDashboardSummary } from "@/lib/types";

const MetricsTimeseriesCharts = dynamic(
  () => import("@/components/dashboard/metrics-timeseries-charts").then((module) => module.MetricsTimeseriesCharts),
  { loading: () => <p role="status" className="text-muted-foreground text-sm">Loading charts…</p> },
);

function shortSha(sha: string | null | undefined): string {
  if (!sha?.trim()) return "—";
  const t = sha.trim();
  if (t === "pending" || t === "unknown") return t;
  return t.length <= 7 ? t : t.slice(0, 7);
}

function formatPct(x: number | null | undefined): string {
  if (x == null) return "—";
  return `${Math.round(x * 1000) / 10}%`;
}

function subscribeMediaQuery(mq: MediaQueryList, onChange: () => void) {
  mq.addEventListener("change", onChange);
  return () => mq.removeEventListener("change", onChange);
}

function useMinWidthLg() {
  return useSyncExternalStore(
    (onStoreChange) => {
      if (typeof window === "undefined") {
        return () => {};
      }
      const mq = window.matchMedia("(min-width: 1024px)");
      return subscribeMediaQuery(mq, onStoreChange);
    },
    () => (typeof window !== "undefined" ? window.matchMedia("(min-width: 1024px)").matches : false),
    () => false
  );
}

function ProjectMetricsWithMethodTable({
  data,
  successHint,
}: {
  data: ProjectDashboardSummary;
  successHint: string;
}) {
  const leftRef = useRef<HTMLDivElement>(null);
  const [leftHeightPx, setLeftHeightPx] = useState<number | null>(null);
  const isLg = useMinWidthLg();

  useLayoutEffect(() => {
    const el = leftRef.current;
    if (!el) return;
    const measure = () => {
      const h = Math.round(el.getBoundingClientRect().height);
      setLeftHeightPx((prev) => (prev === h ? prev : h));
    };
    measure();
    const ro = new ResizeObserver(() => {
      requestAnimationFrame(measure);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [data, isLg]);

  const methodsPanelHeightStyle =
    isLg && leftHeightPx != null
      ? {
          height: leftHeightPx,
          maxHeight: leftHeightPx,
          minHeight: 0,
        }
      : undefined;

  return (
    <div className="grid min-w-0 gap-4 lg:grid-cols-2 lg:items-start">
      <div ref={leftRef} className="min-w-0">
        <div className="grid grid-cols-2 gap-4">
          <ProjectMetricCards data={data} successHint={successHint} />
        </div>
      </div>
      {/* min-h-0 + overflow-hidden: grid row height follows LHS, not methods list intrinsic height */}
      <div className="flex min-h-0 min-w-0 flex-col overflow-hidden lg:min-h-0 lg:w-full lg:self-start lg:overflow-hidden">
        <div
          className={
            isLg
              ? cn(
                  "flex min-h-0 w-full flex-col overflow-hidden lg:flex-none",
                  leftHeightPx == null && "lg:max-h-80"
                )
              : "flex max-h-80 min-h-0 flex-1 flex-col overflow-hidden"
          }
          style={methodsPanelHeightStyle}
        >
          <DashboardMcpMethodsBreakdownCard
            methods={data.method_breakdown_last_7d}
            className="flex h-full min-h-0 flex-col overflow-hidden"
            listClassName="mt-3 min-h-0 max-h-none flex-1 space-y-2 overflow-y-auto overscroll-contain text-sm"
          />
        </div>
      </div>
    </div>
  );
}

function ProjectMetricCards({
  data,
  successHint,
}: {
  data: ProjectDashboardSummary;
  successHint: string;
}) {
  return (
    <>
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
    </>
  );
}

export function ProjectOverviewMetrics({ projectId }: { projectId: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["project-dashboard-summary", projectId],
    queryFn: () => fetchProjectDashboardSummary(projectId),
    enabled: !!projectId,
  });

  if (isLoading) {
    return (
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-24 rounded-lg" />
        ))}
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm">
        <p className="font-medium text-destructive">Could not load project metrics.</p>
        {error instanceof ApiError ? (
          <pre className="text-muted-foreground mt-2 max-h-32 overflow-auto whitespace-pre-wrap break-all text-xs">
            {formatApiErrorDetail(error.body) || error.message}
          </pre>
        ) : null}
      </div>
    );
  }

  if (!data) return null;

  const logWord = pluralEn(data.metrics_sample_size_last_7d, "log", "logs");
  const successHint =
    data.requests_last_7d > data.metrics_sample_size_last_7d
      ? `Based on newest ${data.metrics_sample_size_last_7d.toLocaleString()} ${logWord} in the last 7 days.`
      : "Based on MCP request logs: HTTP 2xx/3xx with no logged JSON-RPC error. Failed calls use non-success HTTP status and/or a logged error code.";

  const activeCapsSummary = `${data.active_tools} ${pluralEn(data.active_tools, "tool", "tools")}, ${data.active_resources} ${pluralEn(data.active_resources, "resource", "resources")}, ${data.active_prompts} ${pluralEn(data.active_prompts, "prompt", "prompts")}`;

  return (
    <div className="space-y-4">
      <div className="rounded-lg border border-dashed p-3 text-sm">
        <span className="text-muted-foreground">Active Release </span>
        {data.active_release_id ? (
          <>
            <span className="font-mono font-medium">{shortSha(data.active_commit_sha)}</span>
            {data.active_release_status ? (
              <span className="text-muted-foreground">
                {" "}
                · <span className="text-foreground">{data.active_release_status}</span>
              </span>
            ) : null}
          </>
        ) : (
          <span className="text-muted-foreground">— None Activated</span>
        )}
        <span className="text-muted-foreground">
          {" "}
          · {activeCapsSummary}
        </span>
      </div>
      <ProjectMetricsWithMethodTable data={data} successHint={successHint} />

      <MetricsTimeseriesCharts variant="project" projectId={projectId} />
    </div>
  );
}

"use client";

import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  ComposedChart,
  Legend,
  Line,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import {
  fetchAccountDashboardTimeseries,
  fetchProjectDashboardTimeseries,
} from "@/lib/projects-api";
import { fetchAdminDashboardTimeseries } from "@/lib/admin-api";
import {
  DASHBOARD_TIMESERIES_OPTIONS,
  type AdminDashboardTimeseries,
  type AccountDashboardTimeseries,
  type DashboardTimeseriesRange,
  type ProjectDashboardTimeseries,
} from "@/lib/dashboard-timeseries";

/** Union of account / project / admin timeseries API responses for chart queries. */
type DashboardTimeseriesPayload =
  | AccountDashboardTimeseries
  | ProjectDashboardTimeseries
  | AdminDashboardTimeseries;
import {
  formatDashboardBucketLabel,
  formatLocalBucketRangeTooltip,
  formatLocalDateTime,
} from "@/lib/format-local-time";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const axisMuted = "hsl(240 3.8% 46.1%)";
const gridStroke = "hsl(240 5.9% 90% / 0.35)";
const areaFill = "hsl(221 83% 53% / 0.25)";
const areaStroke = "hsl(221 83% 53%)";
const okFill = "hsl(142 71% 45% / 0.85)";
const errFill = "hsl(0 84% 60% / 0.55)";
const latencyStroke = "hsl(262 83% 58%)";

type Variant = "account" | "project" | "admin";

export function MetricsTimeseriesCharts({
  variant,
  projectId,
}: {
  variant: Variant;
  projectId?: string;
}) {
  const [range, setRange] = useState<DashboardTimeseriesRange>("24h");

  const query = useQuery<DashboardTimeseriesPayload>({
    queryKey:
      variant === "account"
        ? ["account-dashboard-timeseries", range]
        : variant === "admin"
          ? ["admin-dashboard-timeseries", range]
          : ["project-dashboard-timeseries", projectId, range],
    queryFn: () => {
      if (variant === "account") return fetchAccountDashboardTimeseries(range);
      if (variant === "admin") return fetchAdminDashboardTimeseries(range);
      return fetchProjectDashboardTimeseries(projectId!, range);
    },
    enabled: variant === "account" || variant === "admin" || !!projectId,
  });

  const hourAxisLabels = range === "1h" || range === "24h";

  const chartRows = useMemo(() => {
    const buckets = query.data?.buckets ?? [];
    return buckets.map((b) => {
      const fail = Math.max(0, b.request_count - b.success_count);
      const successPct =
        b.request_count > 0
          ? Math.round((1000 * b.success_count) / b.request_count) / 10
          : null;
      return {
        label: formatDashboardBucketLabel(b.start, hourAxisLabels),
        startIso: b.start,
        endIso: b.end,
        requests: b.request_count,
        ok: b.success_count,
        err: fail,
        successPct,
        latency: b.avg_latency_ms != null ? Math.round(b.avg_latency_ms) : null,
      };
    });
  }, [query.data?.buckets, hourAxisLabels]);

  const rangeLabel =
    DASHBOARD_TIMESERIES_OPTIONS.find((o) => o.value === range)?.label ?? range;

  const metricsSummaryText = useMemo(() => {
    if (chartRows.length === 0) {
      return `No traffic data for ${rangeLabel}.`;
    }
    const totalReq = chartRows.reduce((s, r) => s + r.requests, 0);
    const totalOk = chartRows.reduce((s, r) => s + r.ok, 0);
    const last = chartRows[chartRows.length - 1];
    return `${rangeLabel}: ${chartRows.length} time buckets, ${totalReq.toLocaleString()} total requests, ${totalOk.toLocaleString()} successful responses. Latest bucket ${last.label}: ${last.requests} requests, ${last.err} non-success.`;
  }, [chartRows, rangeLabel]);

  const tooltipLabel = (label: string, payload: readonly { payload?: { startIso?: string; endIso?: string } }[]) => {
    const row = payload[0]?.payload;
    if (row?.startIso && row?.endIso) {
      return formatLocalBucketRangeTooltip(row.startIso, row.endIso);
    }
    return label;
  };

  if (query.isLoading) {
    return (
      <div className="rounded-lg border p-4" role="status" aria-live="polite">
        <div className="text-muted-foreground text-sm">Loading charts…</div>
      </div>
    );
  }

  if (query.error) {
    const err = query.error;
    return (
      <div
        className="rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm"
        role="alert"
      >
        <p className="font-medium text-destructive">Could not load time series.</p>
        {err instanceof ApiError ? (
          <pre className="text-muted-foreground mt-2 max-h-32 overflow-auto whitespace-pre-wrap text-xs">
            {formatApiErrorDetail(err.body) || err.message}
          </pre>
        ) : (
          <p className="text-muted-foreground mt-2 text-xs">{String(err)}</p>
        )}
      </div>
    );
  }

  return (
    <section
      className="space-y-6"
      aria-labelledby="traffic-over-time-heading"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 id="traffic-over-time-heading" className="font-medium">
          Traffic Over Time
        </h3>
        <Select
          value={range}
          onValueChange={(v) => v && setRange(v as DashboardTimeseriesRange)}
        >
          <SelectTrigger
            className="h-9 w-[200px]"
            size="sm"
            aria-label="Time Range for Traffic Charts"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {DASHBOARD_TIMESERIES_OPTIONS.map((opt) => (
              <SelectItem
                key={opt.value}
                value={opt.value}
              >
                {opt.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {variant === "admin" && query.data && isAdminTimeseriesPayload(query.data) ? (
        <div className="text-muted-foreground space-y-1 text-xs">
          <p>{query.data.data_source_note}</p>
          {query.data.rollup_updated_at ? (
            <p>
              Last aggregate refresh:{" "}
              <span className="text-foreground font-medium">
                {formatLocalDateTime(query.data.rollup_updated_at)}
              </span>
            </p>
          ) : (
            <p>
              No rollup rows yet. Run{" "}
              <code className="rounded bg-muted px-1 py-0.5 text-[0.7rem]">
                POST /admin/analytics/rollup-refresh
              </code>{" "}
              (admin) or schedule the SQL job described in the backend docs.
            </p>
          )}
        </div>
      ) : null}

      <p id="metrics-chart-summary" className="sr-only">
        {metricsSummaryText} A full numeric table of all buckets follows for
        screen readers.
      </p>
      <div className="sr-only">
        <table>
          <caption>
            Traffic metrics by time bucket ({rangeLabel})
          </caption>
          <thead>
            <tr>
              <th scope="col">Bucket Label</th>
              <th scope="col">Requests</th>
              <th scope="col">Successful</th>
              <th scope="col">Other</th>
              <th scope="col">Success %</th>
              <th scope="col">Avg Latency (ms)</th>
            </tr>
          </thead>
          <tbody>
            {chartRows.map((row) => (
              <tr key={row.startIso}>
                <td>{row.label}</td>
                <td>{row.requests}</td>
                <td>{row.ok}</td>
                <td>{row.err}</td>
                <td>
                  {row.successPct != null ? `${row.successPct}%` : "—"}
                </td>
                <td>{row.latency != null ? row.latency : "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <figure
          className="rounded-lg border p-3 pt-4"
          aria-labelledby="chart-request-volume-title"
          aria-describedby="metrics-chart-summary"
        >
          <p
            id="chart-request-volume-title"
            className="text-muted-foreground mb-2 text-xs font-medium tracking-wide"
          >
            Request Volume
          </p>
          <div className="h-[240px] w-full min-w-0" aria-hidden="true">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={chartRows} margin={{ top: 8, right: 8, left: 0, bottom: 4 }}>
                <CartesianGrid stroke={gridStroke} strokeDasharray="3 3" vertical={false} />
                <XAxis
                  dataKey="label"
                  tick={{ fill: axisMuted, fontSize: 10 }}
                  interval="preserveStartEnd"
                  tickMargin={6}
                />
                <YAxis
                  tick={{ fill: axisMuted, fontSize: 10 }}
                  width={40}
                  allowDecimals={false}
                />
                <Tooltip
                  labelFormatter={tooltipLabel}
                  contentStyle={{
                    background: "hsl(0 0% 10%)",
                    border: "1px solid hsl(240 5% 26%)",
                    borderRadius: 8,
                    fontSize: 12,
                  }}
                  labelStyle={{ color: "hsl(0 0% 98%)" }}
                />
                <Area
                  type="monotone"
                  dataKey="requests"
                  name="Requests"
                  stroke={areaStroke}
                  fill={areaFill}
                  strokeWidth={2}
                />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </figure>

        <figure
          className="rounded-lg border p-3 pt-4"
          aria-labelledby="chart-success-errors-title"
          aria-describedby="metrics-chart-summary"
        >
          <p
            id="chart-success-errors-title"
            className="text-muted-foreground mb-2 text-xs font-medium tracking-wide"
          >
            Success vs. Errors (Per Bucket)
          </p>
          <div className="h-[240px] w-full min-w-0" aria-hidden="true">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={chartRows} margin={{ top: 8, right: 8, left: 0, bottom: 4 }}>
                <CartesianGrid stroke={gridStroke} strokeDasharray="3 3" vertical={false} />
                <XAxis
                  dataKey="label"
                  tick={{ fill: axisMuted, fontSize: 10 }}
                  interval="preserveStartEnd"
                  tickMargin={6}
                />
                <YAxis tick={{ fill: axisMuted, fontSize: 10 }} width={40} allowDecimals={false} />
                <Tooltip
                  labelFormatter={tooltipLabel}
                  contentStyle={{
                    background: "hsl(0 0% 10%)",
                    border: "1px solid hsl(240 5% 26%)",
                    borderRadius: 8,
                    fontSize: 12,
                  }}
                />
                <Legend wrapperStyle={{ fontSize: 12 }} />
                <Bar dataKey="ok" name="2xx–3xx" stackId="s" fill={okFill} />
                <Bar dataKey="err" name="Other" stackId="s" fill={errFill} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        </figure>
      </div>

      <figure
        className="rounded-lg border p-3 pt-4"
        aria-labelledby="chart-success-latency-title"
        aria-describedby="metrics-chart-summary"
      >
        <p
          id="chart-success-latency-title"
          className="text-muted-foreground mb-2 text-xs font-medium tracking-wide"
        >
          Success Rate and Average Latency
        </p>
        <div className="h-[260px] w-full min-w-0" aria-hidden="true">
          <ResponsiveContainer width="100%" height="100%">
            <ComposedChart data={chartRows} margin={{ top: 8, right: 16, left: 0, bottom: 4 }}>
              <CartesianGrid stroke={gridStroke} strokeDasharray="3 3" vertical={false} />
              <XAxis
                dataKey="label"
                tick={{ fill: axisMuted, fontSize: 10 }}
                interval="preserveStartEnd"
                tickMargin={6}
              />
              <YAxis
                yAxisId="left"
                domain={[0, 100]}
                tick={{ fill: axisMuted, fontSize: 10 }}
                width={36}
                label={{ value: "% ok", angle: -90, position: "insideLeft", fill: axisMuted, fontSize: 10 }}
              />
              <YAxis
                yAxisId="right"
                orientation="right"
                tick={{ fill: axisMuted, fontSize: 10 }}
                width={44}
                label={{
                  value: "ms",
                  angle: 90,
                  position: "insideRight",
                  fill: axisMuted,
                  fontSize: 10,
                }}
              />
              <Tooltip
                labelFormatter={tooltipLabel}
                contentStyle={{
                  background: "hsl(0 0% 10%)",
                  border: "1px solid hsl(240 5% 26%)",
                  borderRadius: 8,
                  fontSize: 12,
                }}
              />
              <Legend wrapperStyle={{ fontSize: 12 }} />
              <Line
                yAxisId="left"
                type="monotone"
                dataKey="successPct"
                name="Success %"
                stroke={okFill}
                dot={false}
                strokeWidth={2}
                connectNulls
              />
              <Line
                yAxisId="right"
                type="monotone"
                dataKey="latency"
                name="Avg latency"
                stroke={latencyStroke}
                dot={false}
                strokeWidth={2}
                connectNulls
              />
            </ComposedChart>
          </ResponsiveContainer>
        </div>
      </figure>
    </section>
  );
}

function isAdminTimeseriesPayload(
  data: DashboardTimeseriesPayload
): data is AdminDashboardTimeseries {
  return "data_source_note" in data && "rollup_updated_at" in data;
}

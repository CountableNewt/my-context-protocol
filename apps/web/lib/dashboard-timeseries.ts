export type DashboardTimeseriesRange =
  | "1h"
  | "24h"
  | "7d"
  | "1mo"
  | "3mo"
  | "6mo"
  | "1y"
  | "ytd"
  | "all";

export interface DashboardTimeseriesBucket {
  label: string;
  start: string;
  end: string;
  request_count: number;
  success_count: number;
  avg_latency_ms: number | null;
}

export interface AccountDashboardTimeseries {
  range_key: string;
  range_start: string;
  range_end: string;
  buckets: DashboardTimeseriesBucket[];
}

export interface ProjectDashboardTimeseries extends AccountDashboardTimeseries {
  project_id: string;
}

/** Admin platform charts from hourly rollup (`GET /admin/timeseries`). */
export interface AdminDashboardTimeseries extends AccountDashboardTimeseries {
  rollup_updated_at: string | null;
  data_source_note: string;
}

export const DASHBOARD_TIMESERIES_OPTIONS: {
  value: DashboardTimeseriesRange;
  label: string;
}[] = [
  { value: "1h", label: "Last Hour" },
  { value: "24h", label: "24 Hours" },
  { value: "7d", label: "7 Days" },
  { value: "1mo", label: "30 Days" },
  { value: "3mo", label: "3 Months" },
  { value: "6mo", label: "6 Months" },
  { value: "1y", label: "1 Year" },
  { value: "ytd", label: "Year to Date" },
  { value: "all", label: "All Time" },
];

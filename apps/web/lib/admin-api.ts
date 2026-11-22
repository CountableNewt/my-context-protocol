import { api } from "./api";
import type { AdminDashboardTimeseries, DashboardTimeseriesRange } from "./dashboard-timeseries";

export interface AdminPlatformMetrics {
  total_users: number;
  total_projects: number;
  total_mcp_calls: number;
}

export interface AdminLookupResult {
  account_id: string;
  is_admin: boolean;
}

/** Accounts with platform admin access (admin session only). */
export interface AdminPrivilegedAccountRow {
  account_id: string;
  github_login: string;
  is_admin: boolean;
  admin_granted_at: string | null;
}

export async function fetchAdminMetrics(): Promise<AdminPlatformMetrics> {
  return api.get<AdminPlatformMetrics>("/admin/metrics");
}

export async function fetchAdminDashboardTimeseries(
  range: DashboardTimeseriesRange = "24h"
): Promise<AdminDashboardTimeseries> {
  const q = encodeURIComponent(range);
  return api.get<AdminDashboardTimeseries>(`/admin/timeseries?range=${q}`);
}

/** Triggers rollup refresh (admin session). Run hourly via cron/Supabase or manually. */
export async function postAdminAnalyticsRollupRefresh(): Promise<void> {
  await api.post<void>("/admin/analytics/rollup-refresh");
}

export async function adminLookup(
  body:
    | { github_login: string }
    | { github_id: string }
    | { email: string }
): Promise<AdminLookupResult> {
  return api.post<AdminLookupResult>("/admin/lookup", body);
}

export async function adminUpdateFlags(body: {
  account_id: string;
  is_admin?: boolean;
}): Promise<AdminLookupResult> {
  return api.post<AdminLookupResult>("/admin/account-flags", body);
}

export async function fetchPrivilegedAccounts(): Promise<AdminPrivilegedAccountRow[]> {
  return api.get<AdminPrivilegedAccountRow[]>("/admin/privileged-accounts");
}

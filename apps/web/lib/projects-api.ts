import { api, ApiError } from "./api";
import type {
  Project,
  RepoConnection,
  Release,
  ApiKey,
  RequestLog,
  GithubRepoListItem,
  ProjectCatalog,
  ProjectCatalogMarkdownUpdate,
  ReleaseValidationReport,
  ValidationErrorEntry,
  CompiledSkill,
  AccountDashboardSummary,
  ProjectDashboardSummary,
  ProjectSkillRuntime,
  SkillRuntimeAssignment,
  ProjectSkillUsage,
  SkillUsageQuery,
} from "./types";
import type {
  AccountDashboardTimeseries,
  DashboardTimeseriesRange,
  ProjectDashboardTimeseries,
} from "./dashboard-timeseries";

export async function fetchProjects(): Promise<Project[]> {
  const response = await api.get<Project[] | { projects: Project[] }>("/projects");
  if (Array.isArray(response)) return response;
  return response.projects ?? [];
}

export async function fetchProject(id: string): Promise<Project> {
  return api.get<Project>(`/projects/${id}`);
}

export async function fetchAccountDashboardSummary(): Promise<AccountDashboardSummary> {
  return api.get<AccountDashboardSummary>("/dashboard/summary");
}

export async function fetchProjectDashboardSummary(
  projectId: string
): Promise<ProjectDashboardSummary> {
  return api.get<ProjectDashboardSummary>(
    `/projects/${projectId}/dashboard/summary`
  );
}

export async function fetchAccountDashboardTimeseries(
  range: DashboardTimeseriesRange = "24h"
): Promise<AccountDashboardTimeseries> {
  const q = encodeURIComponent(range);
  return api.get<AccountDashboardTimeseries>(`/dashboard/timeseries?range=${q}`);
}

export async function fetchProjectDashboardTimeseries(
  projectId: string,
  range: DashboardTimeseriesRange = "24h"
): Promise<ProjectDashboardTimeseries> {
  const q = encodeURIComponent(range);
  return api.get<ProjectDashboardTimeseries>(
    `/projects/${projectId}/dashboard/timeseries?range=${q}`
  );
}

export async function fetchProjectCatalog(projectId: string): Promise<ProjectCatalog> {
  return api.get<ProjectCatalog>(`/projects/${projectId}/catalog`);
}

export async function updateProjectCatalogMarkdown(
  projectId: string,
  body: { markdown: string }
): Promise<ProjectCatalogMarkdownUpdate> {
  return api.patch<ProjectCatalogMarkdownUpdate>(
    `/projects/${projectId}/catalog-markdown`,
    body
  );
}

export async function createProject(data: {
  name: string;
  slug: string;
}): Promise<Project> {
  return api.post<Project>("/projects", data);
}

export async function updateProject(
  projectId: string,
  body: { name: string }
): Promise<Project> {
  return api.patch<Project>(`/projects/${projectId}`, body);
}

export interface CustomDomainStatus {
  hostname: string | null;
  verified: boolean;
  verification_token?: string | null;
  verification_record_name?: string | null;
  instructions?: string | null;
  ownership_verification_record_name?: string | null;
  ownership_verification_record_value?: string | null;
  /** Deprecated Fly-specific compatibility fields; always null on Railway. */
  fly_ownership_verification_record_name?: string | null;
  fly_ownership_verification_record_value?: string | null;
  fly_a_record_values?: string[] | null;
  fly_aaaa_record_values?: string[] | null;
  fly_cname_record_value?: string | null;
  platform_dns_records?: Array<{
    type: string;
    name: string;
    value: string;
    status?: string | null;
    purpose?: string | null;
  }> | null;
  certificate_status?: "not_configured" | "pending" | "issued" | "failed" | "unknown" | null;
  certificate_message?: string | null;
}

export async function fetchCustomDomain(projectId: string): Promise<CustomDomainStatus> {
  return api.get<CustomDomainStatus>(`/projects/${projectId}/custom-domain`);
}

export async function setProjectCustomDomain(
  projectId: string,
  hostname: string
): Promise<CustomDomainStatus> {
  return api.post<CustomDomainStatus>(`/projects/${projectId}/custom-domain`, { hostname });
}

export async function verifyProjectCustomDomain(projectId: string): Promise<CustomDomainStatus> {
  return api.post<CustomDomainStatus>(`/projects/${projectId}/custom-domain/verify`);
}

export async function fetchRepoConnection(projectId: string): Promise<RepoConnection | null> {
  try {
    return await api.get<RepoConnection>(`/projects/${projectId}/repo-connection`);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) return null;
    throw error;
  }
}

export async function fetchUserGithubRepos(): Promise<GithubRepoListItem[]> {
  return api.get<GithubRepoListItem[]>("/github/repos");
}

export type RepositoryMigrationSource = Pick<RepoConnection, "provider" | "repo_owner" | "repo_name" | "default_branch">;

export async function connectRepo(
  projectId: string,
  data: ({ provider?: "github"; owner: string; repo: string; branch: string } | { provider: "tangled"; owner: string; repo: string; branch: string; private?: boolean }) & { migrate_from?: RepositoryMigrationSource }
): Promise<RepoConnection> {
  return api.post<RepoConnection>(`/projects/${projectId}/connect-repo`, data);
}

export interface SyncProgress { status: string; id?: string; release_id?: string }
export async function triggerSync(projectId: string): Promise<SyncProgress | void> {
  return api.post<SyncProgress | void>(`/projects/${projectId}/sync`);
}

export async function fetchReleases(projectId: string): Promise<Release[]> {
  const response = await api.get<Release[] | { releases: Release[] }>(
    `/projects/${projectId}/releases`
  );
  if (Array.isArray(response)) return response;
  return response.releases ?? [];
}

export async function activateRelease(
  projectId: string,
  releaseId: string
): Promise<void> {
  await api.post(`/projects/${projectId}/releases/${releaseId}/activate`);
}

export async function fetchReleaseValidation(
  projectId: string,
  releaseId: string
): Promise<ReleaseValidationReport> {
  const r = await api.get<ReleaseValidationReport>(
    `/projects/${projectId}/releases/${releaseId}/validation`
  );
  const normalizeEntry = (e: ValidationErrorEntry): ValidationErrorEntry => {
    const summary = e.summary?.trim();
    const fix = e.fix_hint?.trim();
    const message =
      e.message?.trim() ||
      [summary, fix].filter(Boolean).join(" — ") ||
      "Validation issue";
    return { ...e, message };
  };
  return {
    ...r,
    errors: (r.errors ?? []).map(normalizeEntry),
    warnings: (r.warnings ?? []).map(normalizeEntry),
  };
}

export async function fetchCompiledSkills(
  projectId: string,
  releaseId: string
): Promise<CompiledSkill[]> {
  return api.get<CompiledSkill[]>(
    `/projects/${projectId}/releases/${releaseId}/compiled-skills`
  );
}

export async function updateCompiledSkill(
  projectId: string,
  releaseId: string,
  compiledSkillId: string,
  body: {
    exposure_type?: string;
    risk_level?: string;
    status?: string;
    summary?: string | null;
    skill_body?: string | null;
    /** SKILL routing metadata (lists + invoke_first); persisted and merged into resource MCP schema. */
    routing?: {
      use_when?: string[];
      avoid_when?: string[];
      failure_modes?: string[];
      invoke_first?: boolean;
    };
    /** When true, `schema_json` is applied (use empty string to rebuild defaults). */
    replace_schema?: boolean;
    schema_json?: string | null;
    runtime?: {
      kind?: "operating" | "task" | "tool-use" | "reference";
      scope?: "global" | "organization" | "workspace" | "repository" | "task";
      activation?: {
        mode?: "always" | "intent" | "event" | "explicit";
        intents?: string[];
        events?: string[];
        tags?: string[];
        examples?: string[];
      };
      enforcement?: "advisory" | "required";
      priority?: number;
      version?: string;
    };
  }
): Promise<CompiledSkill> {
  return api.patch<CompiledSkill>(
    `/projects/${projectId}/releases/${releaseId}/compiled-skills/${compiledSkillId}`,
    body
  );
}

export async function writeBackCompiledSkillMetadata(projectId: string, releaseId: string, compiledSkillId: string): Promise<{ pull_request_url: string; branch: string; source_path: string }> {
  return api.post(`/projects/${projectId}/releases/${releaseId}/compiled-skills/${compiledSkillId}/writeback`, {});
}

export async function fetchProjectSkillRuntime(projectId: string): Promise<ProjectSkillRuntime> {
  return api.get<ProjectSkillRuntime>(`/projects/${projectId}/skill-runtime`);
}

export async function fetchProjectSkillUsage(projectId: string, params: SkillUsageQuery = {}): Promise<ProjectSkillUsage> {
  const query = new URLSearchParams({ window: params.window ?? "7d" });
  if (params.page != null) query.set("page", String(params.page));
  if (params.page_size != null) query.set("page_size", String(params.page_size));
  if (params.sort) query.set("sort", params.sort);
  if (params.direction) query.set("direction", params.direction);
  if (params.skill_id) query.set("skill_id", params.skill_id);
  return api.get<ProjectSkillUsage>(`/projects/${encodeURIComponent(projectId)}/skill-usage?${query}`);
}

export async function updateProjectSkillRuntime(
  projectId: string,
  body: Partial<Omit<ProjectSkillRuntime, "recent_events" | "assignments">> & { assignments?: SkillRuntimeAssignment[] }
): Promise<ProjectSkillRuntime> {
  return api.patch<ProjectSkillRuntime>(`/projects/${projectId}/skill-runtime`, body);
}

export async function fetchApiKeys(
  projectId: string,
  options?: { includeRevoked?: boolean }
): Promise<ApiKey[]> {
  const q =
    options?.includeRevoked === true ? "?include_revoked=true" : "";
  const response = await api.get<ApiKey[] | { api_keys: ApiKey[] }>(
    `/projects/${projectId}/api-keys${q}`
  );
  if (Array.isArray(response)) return response;
  return response.api_keys ?? [];
}

export async function createApiKey(
  projectId: string,
  data?: { name?: string | null }
): Promise<{ id: string; key: string; prefix: string; name?: string | null }> {
  return api.post<{ id: string; key: string; prefix: string; name?: string | null }>(
    `/projects/${projectId}/api-keys`,
    data ?? {}
  );
}

export async function updateApiKey(
  projectId: string,
  keyId: string,
  body: { name: string }
): Promise<ApiKey> {
  return api.patch<ApiKey>(`/projects/${projectId}/api-keys/${keyId}`, body);
}

/** Permanently revokes the key (soft-delete on the server). */
export async function revokeApiKey(projectId: string, keyId: string): Promise<void> {
  await api.delete(`/projects/${projectId}/api-keys/${keyId}`);
}

export async function fetchRequestLogs(
  projectId: string,
  params?: { limit?: number; offset?: number }
): Promise<RequestLog[]> {
  const q = new URLSearchParams();
  if (params?.limit != null) q.set("limit", String(params.limit));
  if (params?.offset != null) q.set("offset", String(params.offset));
  const search = q.toString() ? `?${q.toString()}` : "";
  const response = await api.get<RequestLog[] | { logs: RequestLog[] }>(
    `/projects/${projectId}/request-logs${search}`
  );
  if (Array.isArray(response)) return response;
  return response.logs ?? [];
}

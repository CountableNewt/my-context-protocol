import { afterEach, describe, expect, it, vi } from "vitest";

const { get, post, patch, deleteFn } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  patch: vi.fn(),
  deleteFn: vi.fn(),
}));

vi.mock("./api", () => ({
  api: { get, post, patch, put: vi.fn(), delete: deleteFn },
  ApiError: class ApiError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  },
}));

import { ApiError } from "./api";
import {
  activateRelease,
  connectRepo,
  createApiKey,
  createProject,
  updateProject,
  fetchAccountDashboardSummary,
  fetchAccountDashboardTimeseries,
  fetchApiKeys,
  fetchCompiledSkills,
  fetchCustomDomain,
  fetchProject,
  fetchProjectCatalog,
  fetchProjectDashboardSummary,
  fetchProjectDashboardTimeseries,
  fetchProjects,
  fetchReleaseValidation,
  fetchReleases,
  fetchRepoConnection,
  fetchRequestLogs,
  fetchUserGithubRepos,
  setProjectCustomDomain,
  triggerSync,
  updateApiKey,
  revokeApiKey,
  updateCompiledSkill,
  updateProjectCatalogMarkdown,
  verifyProjectCustomDomain,
  fetchProjectSkillRuntime,
  fetchProjectSkillUsage,
  updateProjectSkillRuntime,
  writeBackCompiledSkillMetadata,
} from "./projects-api";

afterEach(() => {
  get.mockReset();
  post.mockReset();
  patch.mockReset();
  deleteFn.mockReset();
});

describe("projects-api", () => {
  it("fetchProjectSkillUsage defaults to seven days and encodes filters", async () => {
    get.mockResolvedValue({ skills: [] });
    await fetchProjectSkillUsage("p");
    expect(get).toHaveBeenLastCalledWith("/projects/p/skill-usage?window=7d");
    await fetchProjectSkillUsage("p/1", { window: "30d", page: 2, page_size: 25, sort: "surfaced", direction: "asc", skill_id: "skill & one" });
    expect(get).toHaveBeenLastCalledWith("/projects/p%2F1/skill-usage?window=30d&page=2&page_size=25&sort=surfaced&direction=asc&skill_id=skill+%26+one");
  });

  it("fetchProjects unwraps array or .projects", async () => {
    get.mockResolvedValueOnce([{ id: "1" }]);
    expect(await fetchProjects()).toEqual([{ id: "1" }]);

    get.mockResolvedValueOnce({ projects: [{ id: "2" }] });
    expect(await fetchProjects()).toEqual([{ id: "2" }]);

    get.mockResolvedValueOnce({});
    expect(await fetchProjects()).toEqual([]);
  });

  it("fetchProject and dashboard getters forward paths", async () => {
    get.mockResolvedValueOnce({ id: "p" });
    expect(await fetchProject("abc")).toEqual({ id: "p" });
    expect(get).toHaveBeenCalledWith("/projects/abc");

    get.mockResolvedValueOnce({ total: 1 });
    expect(await fetchAccountDashboardSummary()).toEqual({ total: 1 });

    get.mockResolvedValueOnce({ total: 2 });
    expect(await fetchProjectDashboardSummary("pid")).toEqual({ total: 2 });
    expect(get).toHaveBeenCalledWith("/projects/pid/dashboard/summary");
  });

  it("fetchAccountDashboardTimeseries encodes range", async () => {
    get.mockResolvedValueOnce({ range_key: "24h", buckets: [] });
    await fetchAccountDashboardTimeseries("7d");
    expect(get).toHaveBeenCalledWith("/dashboard/timeseries?range=7d");
  });

  it("fetchProjectDashboardTimeseries encodes range", async () => {
    get.mockResolvedValueOnce({ project_id: "x", buckets: [] });
    await fetchProjectDashboardTimeseries("x", "1mo");
    expect(get).toHaveBeenCalledWith("/projects/x/dashboard/timeseries?range=1mo");
  });

  it("fetchProjectCatalog, createProject, and updateProject", async () => {
    get.mockResolvedValueOnce({
      catalog_markdown: "# Catalog",
      catalog_markdown_generated: "# Catalog",
      catalog_markdown_override: null,
      tools: [],
      resources: [],
      prompts: [],
    });
    await fetchProjectCatalog("pid");
    expect(get).toHaveBeenCalledWith("/projects/pid/catalog");

    patch.mockResolvedValueOnce({
      catalog_markdown: "# Catalog",
      catalog_markdown_generated: "# Catalog",
      catalog_markdown_override: null,
    });
    await updateProjectCatalogMarkdown("pid", { markdown: "" });
    expect(patch).toHaveBeenCalledWith("/projects/pid/catalog-markdown", { markdown: "" });

    post.mockResolvedValueOnce({ id: "new" });
    await createProject({ name: "N", slug: "n" });
    expect(post).toHaveBeenCalledWith("/projects", { name: "N", slug: "n" });

    patch.mockResolvedValueOnce({ id: "pid", name: "Renamed" });
    await updateProject("pid", { name: "Renamed" });
    expect(patch).toHaveBeenCalledWith("/projects/pid", { name: "Renamed" });
  });

  it("custom domain helpers", async () => {
    get.mockResolvedValueOnce({ hostname: "h", verified: false });
    expect(await fetchCustomDomain("pid")).toEqual({ hostname: "h", verified: false });

    post.mockResolvedValueOnce({ verified: true });
    await setProjectCustomDomain("pid", "app.example.com");
    expect(post).toHaveBeenCalledWith("/projects/pid/custom-domain", {
      hostname: "app.example.com",
    });

    post.mockResolvedValueOnce({ verified: true });
    await verifyProjectCustomDomain("pid");
    expect(post).toHaveBeenCalledWith("/projects/pid/custom-domain/verify");
  });

  it("fetchRepoConnection returns null on ApiError", async () => {
    get.mockRejectedValueOnce(new ApiError("missing", 404));
    expect(await fetchRepoConnection("pid")).toBeNull();
  });

  it("preserves repository connection failures instead of reporting disconnection", async () => {
    get.mockRejectedValueOnce(new ApiError("unavailable", 503));
    await expect(fetchRepoConnection("pid")).rejects.toMatchObject({ status: 503 });
  });

  it("connectRepo, triggerSync, fetchUserGithubRepos", async () => {
    post.mockResolvedValueOnce({ id: "rc" });
    await connectRepo("pid", { owner: "a", repo: "b", branch: "main" });
    expect(post).toHaveBeenCalledWith("/projects/pid/connect-repo", {
      owner: "a",
      repo: "b",
      branch: "main",
    });

    post.mockResolvedValueOnce(undefined);
    await triggerSync("pid");
    expect(post).toHaveBeenCalledWith("/projects/pid/sync");

    get.mockResolvedValueOnce([]);
    await fetchUserGithubRepos();
    expect(get).toHaveBeenCalledWith("/github/repos");
  });

  it("fetchReleases unwraps array or .releases", async () => {
    get.mockResolvedValueOnce([{ id: "r1" }]);
    expect(await fetchReleases("pid")).toEqual([{ id: "r1" }]);

    get.mockResolvedValueOnce({ releases: [{ id: "r2" }] });
    expect(await fetchReleases("pid")).toEqual([{ id: "r2" }]);

    get.mockResolvedValueOnce({});
    expect(await fetchReleases("pid")).toEqual([]);
  });

  it("activateRelease, fetchReleaseValidation, fetchCompiledSkills, updateCompiledSkill", async () => {
    post.mockResolvedValueOnce(undefined);
    await activateRelease("pid", "rid");
    expect(post).toHaveBeenCalledWith("/projects/pid/releases/rid/activate");

    get.mockResolvedValueOnce({ ok: true });
    await fetchReleaseValidation("pid", "rid");
    expect(get).toHaveBeenCalledWith("/projects/pid/releases/rid/validation");

    get.mockResolvedValueOnce([]);
    await fetchCompiledSkills("pid", "rid");
    expect(get).toHaveBeenCalledWith("/projects/pid/releases/rid/compiled-skills");

    patch.mockResolvedValueOnce({ id: "cs" });
    await updateCompiledSkill("pid", "rid", "csid", { summary: "x" });
    expect(patch).toHaveBeenCalledWith(
      "/projects/pid/releases/rid/compiled-skills/csid",
      { summary: "x" }
    );
  });

  it("portable runtime helpers use tenant-scoped endpoints", async () => {
    get.mockResolvedValueOnce({ telemetry_enabled: false, assignments: [], recent_events: [] });
    await fetchProjectSkillRuntime("pid");
    expect(get).toHaveBeenCalledWith("/projects/pid/skill-runtime");

    patch.mockResolvedValueOnce({ telemetry_enabled: true, assignments: [], recent_events: [] });
    await updateProjectSkillRuntime("pid", { telemetry_enabled: true, assignments: [] });
    expect(patch).toHaveBeenCalledWith("/projects/pid/skill-runtime", {
      telemetry_enabled: true,
      assignments: [],
    });

    post.mockResolvedValueOnce({ pull_request_url: "https://github.test/pr/1" });
    await writeBackCompiledSkillMetadata("pid", "rid", "sid");
    expect(post).toHaveBeenCalledWith(
      "/projects/pid/releases/rid/compiled-skills/sid/writeback",
      {}
    );
  });

  it("fetchApiKeys unwraps and createApiKey passes body", async () => {
    get.mockResolvedValueOnce([{ id: "k" }]);
    expect(await fetchApiKeys("pid")).toEqual([{ id: "k" }]);

    get.mockResolvedValueOnce({ api_keys: [{ id: "k2" }] });
    expect(await fetchApiKeys("pid")).toEqual([{ id: "k2" }]);

    post.mockResolvedValueOnce({ key: "sec", prefix: "pre" });
    await createApiKey("pid", { name: "CI" });
    expect(post).toHaveBeenCalledWith("/projects/pid/api-keys", { name: "CI" });

    post.mockResolvedValueOnce({ key: "sec", prefix: "pre" });
    await createApiKey("pid");
    expect(post).toHaveBeenLastCalledWith("/projects/pid/api-keys", {});

    patch.mockResolvedValueOnce({ id: "kid", name: "Renamed" });
    await updateApiKey("pid", "kid", { name: "Renamed" });
    expect(patch).toHaveBeenCalledWith("/projects/pid/api-keys/kid", {
      name: "Renamed",
    });

    get.mockResolvedValueOnce([{ id: "k3" }]);
    await fetchApiKeys("pid", { includeRevoked: true });
    expect(get).toHaveBeenCalledWith("/projects/pid/api-keys?include_revoked=true");

    deleteFn.mockResolvedValueOnce(undefined);
    await revokeApiKey("pid", "kid");
    expect(deleteFn).toHaveBeenCalledWith("/projects/pid/api-keys/kid");

    get.mockResolvedValueOnce({});
    expect(await fetchApiKeys("pid")).toEqual([]);
  });

  it("fetchRequestLogs builds query and unwraps", async () => {
    get.mockResolvedValueOnce([{ id: "l" }]);
    expect(await fetchRequestLogs("pid")).toEqual([{ id: "l" }]);

    get.mockResolvedValueOnce({ logs: [{ id: "l2" }] });
    expect(await fetchRequestLogs("pid", { limit: 5, offset: 10 })).toEqual([{ id: "l2" }]);
    expect(get).toHaveBeenCalledWith("/projects/pid/request-logs?limit=5&offset=10");

    get.mockResolvedValueOnce({});
    expect(await fetchRequestLogs("pid")).toEqual([]);
  });
});

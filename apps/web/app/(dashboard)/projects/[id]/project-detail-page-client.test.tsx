/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api";
import { fetchProject } from "@/lib/projects-api";
import { ProjectDetailPageClient } from "./project-detail-page-client";
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
}));
vi.mock("@/contexts/auth-context", () => ({ useAuth: () => ({ user: { plan: "free" } }) }));
vi.mock("@/lib/projects-api", () => ({
  fetchProject: vi.fn().mockResolvedValue({ id: "project-1", name: "Skills", slug: "skills", subdomain: "skills" }),
  fetchRepoConnection: vi.fn().mockResolvedValue(null),
  fetchUserGithubRepos: vi.fn().mockResolvedValue([]),
  connectRepo: vi.fn(), triggerSync: vi.fn(),
}));
vi.mock("@/components/dashboard/project-name-header", () => ({ ProjectNameHeader: () => null }));
vi.mock("@/components/dashboard/release-table", () => ({ ReleaseTable: () => null }));
vi.mock("@/components/dashboard/api-key-manager", () => ({ ApiKeyManager: () => null }));
vi.mock("@/components/dashboard/request-logs-table", () => ({ RequestLogsTable: () => null }));
vi.mock("@/components/dashboard/custom-domain-section", () => ({ CustomDomainSection: () => null }));
vi.mock("@/components/dashboard/mcp-catalog-section", () => ({ McpCatalogSection: () => null }));
vi.mock("@/components/dashboard/project-overview-metrics", () => ({ ProjectOverviewMetrics: () => null }));
vi.mock("@/components/dashboard/skill-runtime-section", () => ({ SkillRuntimeSection: () => null }));
vi.mock("@/components/dashboard/skill-usage-section", () => ({ SkillUsageSection: () => null }));
it("mounts the repository picker on an installation callback without a tab or pending selection", async () => {
  window.history.replaceState({}, "", "/projects/project-1?github_app_installed=1");
  sessionStorage.clear();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["github-repos"], []);
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  try {
    await act(async () => {
      root.render(<QueryClientProvider client={client}><ProjectDetailPageClient projectId="project-1"/></QueryClientProvider>);
    });
    await vi.waitFor(async () => {
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 10)); });
      expect(host.textContent).toContain("No repositories are accessible to MyContextProtocol yet.");
    });
    expect(host.querySelector("a")?.textContent).toBe("Configure GitHub Access");
    expect(window.location.search).toBe("?tab=repo");
    expect(host.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe("Repo");
  }
  finally {
    await act(async () => root.unmount());
    host.remove();
    client.clear();
  }
});

it("keeps record verification available when a canonical audit blocks normal project reads", async () => {
  vi.mocked(fetchProject).mockRejectedValueOnce(new ApiError("Unavailable", 503, { reason: "Canonical records are missing or invalid" }));
  window.history.replaceState({}, "", "/projects/project-broken");
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><ProjectDetailPageClient projectId="project-broken" /></QueryClientProvider>));
    await vi.waitFor(async () => {
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 10)); });
      expect(host.textContent).toContain("Failed to load project.");
      expect([...host.querySelectorAll("button")].some((button) => button.textContent === "Verify records")).toBe(true);
    });
  } finally {
    await act(async () => root.unmount());
    host.remove();
    client.clear();
  }
});

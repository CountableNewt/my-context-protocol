"use client";

import { useEffect, useMemo } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { fetchProject } from "@/lib/projects-api";
import { ProjectNameHeader } from "@/components/dashboard/project-name-header";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { PublicationReviewSection } from "@/components/dashboard/publication-review-section";
import { RepoConnectionSection } from "@/components/dashboard/repo-connection-section";
import { ReleaseTable } from "@/components/dashboard/release-table";
import { ApiKeyManager } from "@/components/dashboard/api-key-manager";
import { RequestLogsTable } from "@/components/dashboard/request-logs-table";
import { CustomDomainSection } from "@/components/dashboard/custom-domain-section";
import { McpCatalogSection } from "@/components/dashboard/mcp-catalog-section";
import { ProjectOverviewMetrics } from "@/components/dashboard/project-overview-metrics";
import { SkillRuntimeSection } from "@/components/dashboard/skill-runtime-section";
import { SkillUsageSection } from "@/components/dashboard/skill-usage-section";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { CopyIcon } from "lucide-react";
import { copyTextToClipboard } from "@/lib/clipboard";

export function ProjectDetailPageClient({ projectId }: { projectId: string }) {
  const router = useRouter();
  const searchParams = useSearchParams();

  // Legacy connect callbacks omit tab; mount the repository flow to consume the callback.
  const tab = searchParams.get("github_app_installed") === "1"
    ? "repo"
    : searchParams.get("tab") ?? "overview";
  const tabList = useMemo(
    () => ["overview", "repo", "releases", "runtime", "api-keys", "logs"] as const,
    []
  );

  function setTab(next: string) {
    const p = new URLSearchParams(searchParams.toString());
    if (next === "overview") {
      p.delete("tab");
    } else {
      p.set("tab", next);
    }
    const qs = p.toString();
    router.replace(qs ? `/projects/${projectId}?${qs}` : `/projects/${projectId}`, {
      scroll: false,
    });
  }

  const { data: project, isLoading, error } = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => fetchProject(projectId),
    enabled: !!projectId,
  });

  const isNotFound =
    error instanceof ApiError && error.status === 404;

  useEffect(() => {
    if (isNotFound) {
      router.replace("/projects");
    }
  }, [isNotFound, router]);

  if (isLoading) {
    return (
      <div
        className="space-y-6"
        role="status"
        aria-live="polite"
        aria-busy="true"
      >
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64" />
      </div>
    );
  }

  if (error) {
    if (isNotFound) {
      return (
        <p className="text-muted-foreground text-sm" role="status">
          This project no longer exists — opening your project list…
        </p>
      );
    }
    return (
      <div className="space-y-2 rounded-md border border-destructive/40 bg-destructive/5 p-4 text-sm">
        <p className="font-medium text-destructive">Failed to load project.</p>
        {error instanceof ApiError ? (
          <pre className="text-muted-foreground max-h-48 overflow-auto whitespace-pre-wrap break-all text-xs">
            {formatApiErrorDetail(error.body) || error.message}
          </pre>
        ) : (
          <p className="text-muted-foreground text-xs">{String(error)}</p>
        )}
      </div>
    );
  }

  if (!project) {
    return null;
  }

  return (
    <div className="space-y-6 md:space-y-7">
      <div className="space-y-2">
        <ProjectNameHeader project={project} projectId={projectId} />
        <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
          <p className="text-muted-foreground font-mono text-sm leading-relaxed break-all">
            {project.mcp_url ??
              "MCP URL unavailable — set SAAS_MCP_BASE_DOMAIN on the server (or verify a custom domain)."}
          </p>
          {project.mcp_url ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="shrink-0"
              onClick={() =>
                void copyTextToClipboard(project.mcp_url!, {
                  success: "MCP URL copied to clipboard",
                  error: "Could not copy MCP URL",
                })
              }
            >
              <CopyIcon className="mr-1 h-3.5 w-3.5" aria-hidden />
              Copy MCP URL
            </Button>
          ) : null}
        </div>
      </div>
      <Tabs value={tabList.includes(tab as (typeof tabList)[number]) ? tab : "overview"} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="repo">Repo</TabsTrigger>
          <TabsTrigger value="releases">Releases</TabsTrigger>
          <TabsTrigger value="runtime">Runtime</TabsTrigger>
          <TabsTrigger value="api-keys">API Keys</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="space-y-4">
          <ProjectOverviewMetrics projectId={projectId} />
          <div className="rounded-lg border p-4">
            <h3 className="font-medium">Project Info</h3>
            <dl className="mt-2 space-y-1 text-sm">
              <div>
                <dt className="text-muted-foreground inline">Slug: </dt>
                <dd className="inline">{project.slug}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground inline">Platform subdomain: </dt>
                <dd className="inline">{project.subdomain}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground inline">MCP URL: </dt>
                <dd className="inline font-mono break-all">
                  {project.mcp_url ??
                    "— (configure SAAS_MCP_BASE_DOMAIN or verify custom domain)"}
                </dd>
              </div>
            </dl>
          </div>
          <McpCatalogSection projectId={projectId} />
          <CustomDomainSection projectId={projectId} />
        </TabsContent>
        <TabsContent value="repo" className="space-y-5">
          <RepoConnectionSection projectId={projectId} />
          <PublicationReviewSection key={projectId} projectId={projectId} />
        </TabsContent>
        <TabsContent value="releases">
          <ReleaseTable projectId={projectId} />
        </TabsContent>
        <TabsContent value="runtime" className="space-y-5">
          <SkillUsageSection key={projectId} projectId={projectId} />
          <SkillRuntimeSection projectId={projectId} />
        </TabsContent>
        <TabsContent value="api-keys">
          <ApiKeyManager
            projectId={projectId}
            mcpUrl={project.mcp_url}
            projectSlug={project.slug}
            mcpOAuthEnabled={project.mcp_oauth_enabled === true}
          />
        </TabsContent>
        <TabsContent value="logs">
          <RequestLogsTable projectId={projectId} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

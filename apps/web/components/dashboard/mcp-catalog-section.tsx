"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  fetchProjectCatalog,
  fetchProjectDashboardSummary,
  updateProjectCatalogMarkdown,
} from "@/lib/projects-api";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { CopyIcon } from "lucide-react";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import { copyTextToClipboard, mcpEventsUrl } from "@/lib/clipboard";
import { pluralEn } from "@/lib/pluralize";
import dynamic from "next/dynamic";
import { glassSurfaceClasses } from "@/lib/glass";
import { MYCONTEXT_CATALOG_TOOL_NAME } from "@/lib/mcp-tool-names";
import { cn } from "@/lib/utils";
import type { DashboardCapabilityUsage } from "@/lib/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

const MarkdownPreview = dynamic(
  () => import("@/components/dashboard/markdown-preview").then((module) => module.MarkdownPreview),
  { loading: () => <p role="status" className="text-muted-foreground text-sm">Loading preview…</p> },
);

function mcpHostOrigin(mcpUrl: string): string | null {
  try {
    return new URL(mcpUrl).origin;
  } catch {
    return null;
  }
}

const CATALOG_MARKDOWN_MAX_CHARS = 512 * 1024;

/** Inset surface for subpanels, monospace blocks, and URL chips (shared fill). */
const MCP_CATALOG_INSET_SURFACE =
  "rounded-lg border border-border/80 bg-muted/35 dark:bg-muted/20";

/** Nested blocks (catalog editor, resource cards) inside a section. */
const MCP_CATALOG_SUBPANEL = cn("space-y-3 p-4", MCP_CATALOG_INSET_SURFACE);

/** Bordered block matching Project Info / other dashboard sections. */
const MCP_CATALOG_SECTION =
  "space-y-4 rounded-lg border p-4 text-sm text-card-foreground";

function capabilityUsageMapKey(kind: string, key: string): string {
  return `${kind}\u001e${key}`;
}

function lookupCapabilityUsage(
  map: Map<string, DashboardCapabilityUsage>,
  kind: string,
  key: string,
): DashboardCapabilityUsage | undefined {
  return map.get(capabilityUsageMapKey(kind, key));
}

function CapabilityUsageCells({
  usage,
}: {
  usage: DashboardCapabilityUsage | undefined;
}) {
  if (!usage) {
    return (
      <>
        <TableCell className="text-muted-foreground text-right tabular-nums">
          —
        </TableCell>
        <TableCell className="text-muted-foreground text-right tabular-nums">
          —
        </TableCell>
      </>
    );
  }
  return (
    <>
      <TableCell className="text-right tabular-nums">
        {usage.invocations_last_7d.toLocaleString()}
      </TableCell>
      <TableCell className="text-right tabular-nums">
        {usage.successful_last_7d.toLocaleString()}
      </TableCell>
    </>
  );
}

interface McpCatalogSectionProps {
  projectId: string;
}

export function McpCatalogSection({ projectId }: McpCatalogSectionProps) {
  const queryClient = useQueryClient();
  const { data, isLoading, error } = useQuery({
    queryKey: ["project-catalog", projectId],
    queryFn: () => fetchProjectCatalog(projectId),
  });

  const { data: projectMetrics, isFetched: dashboardMetricsFetched } = useQuery({
    queryKey: ["project-dashboard-summary", projectId],
    queryFn: () => fetchProjectDashboardSummary(projectId),
    enabled: !!projectId,
  });

  const capabilityUsageByTarget = useMemo(() => {
    const rows = projectMetrics?.capability_usage_last_7d ?? [];
    const m = new Map<string, DashboardCapabilityUsage>();
    for (const r of rows) {
      m.set(capabilityUsageMapKey(r.kind, r.key), r);
    }
    return m;
  }, [projectMetrics?.capability_usage_last_7d]);

  const [draft, setDraft] = useState("");
  const [saveError, setSaveError] = useState<string | null>(null);
  const [catalogEditorOpen, setCatalogEditorOpen] = useState(false);

  // Static-checkable dep for draft reset below; complex `?? ""` falls afoul of
  // react-hooks/exhaustive-deps if used inline.
  const catalogOverrideKey = data?.catalog_markdown_override ?? "";

  const catalogSyncKey = useMemo(
    () =>
      data
        ? `${projectId}:${data.catalog_markdown ?? ""}:${data.catalog_markdown_generated ?? ""}:${catalogOverrideKey}`
        : "",
    [projectId, data, catalogOverrideKey],
  );

  const [prevCatalogSyncKey, setPrevCatalogSyncKey] = useState("");
  if (data && catalogSyncKey !== prevCatalogSyncKey) {
    setPrevCatalogSyncKey(catalogSyncKey);
    setDraft(data.catalog_markdown ?? "");
  }

  const saveMutation = useMutation({
    mutationFn: async () => {
      if (!data) throw new Error("Catalog not loaded");
      const trimmedDraft = draft.trim();
      const trimmedGen = (
        data.catalog_markdown_generated ??
        data.catalog_markdown ??
        ""
      ).trim();
      const markdown = trimmedDraft === trimmedGen ? "" : trimmedDraft;
      if (markdown.length > CATALOG_MARKDOWN_MAX_CHARS) {
        throw new Error(
          `Catalog markdown must be ${CATALOG_MARKDOWN_MAX_CHARS.toLocaleString()} characters or fewer`,
        );
      }
      return updateProjectCatalogMarkdown(projectId, { markdown });
    },
    onSuccess: () => {
      setSaveError(null);
      setCatalogEditorOpen(false);
      void queryClient.invalidateQueries({
        queryKey: ["project-catalog", projectId],
      });
    },
    onError: (e: unknown) => {
      setSaveError(
        e instanceof ApiError
          ? formatApiErrorDetail(e.body) || e.message
          : e instanceof Error
            ? e.message
            : String(e),
      );
    },
  });

  const restoreMutation = useMutation({
    mutationFn: () => updateProjectCatalogMarkdown(projectId, { markdown: "" }),
    onSuccess: () => {
      setSaveError(null);
      setCatalogEditorOpen(false);
      void queryClient.invalidateQueries({
        queryKey: ["project-catalog", projectId],
      });
    },
    onError: (e: unknown) => {
      setSaveError(
        e instanceof ApiError
          ? formatApiErrorDetail(e.body) || e.message
          : e instanceof Error
            ? e.message
            : String(e),
      );
    },
  });

  if (isLoading) {
    return <Skeleton className="h-64 w-full" />;
  }

  if (error) {
    return (
      <div className="space-y-2 rounded-md border border-destructive/40 bg-destructive/5 p-4 text-sm">
        <p className="font-medium text-destructive">
          Could not load MCP catalog.
        </p>
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

  if (!data) {
    return null;
  }

  const url = data.mcp_url;
  const oauthOnHost = data.mcp_oauth_enabled === true;
  const oauthOrigin = url ? mcpHostOrigin(url) : null;
  const sampleInitialize = JSON.stringify(
    {
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: {
        protocolVersion: "2024-11-05",
        capabilities: {},
        clientInfo: { name: "example", version: "1.0.0" },
      },
    },
    null,
    2,
  );

  const sampleInitializeResponse = JSON.stringify(
    {
      jsonrpc: "2.0",
      id: 1,
      result: {
        protocolVersion: "2024-11-05",
        capabilities: {
          tools: { listChanged: true },
          resources: { subscribe: true, listChanged: true },
          prompts: { listChanged: true },
        },
        serverInfo: {
          name: "MyContextProtocol",
          version: "1.0.0",
          title: "MyContextProtocol — Your project",
          description: "Hosted MCP skills for this project.",
          websiteUrl: "https://your-app.example.com/projects/<project-id>",
        },
        instructions:
          "You are connected to MyContextProtocol…\nDiscovery: call tool `mycontext_catalog` first…",
      },
    },
    null,
    2,
  );

  const sampleResourcesRead = (uri: string) =>
    JSON.stringify(
      {
        jsonrpc: "2.0",
        id: 2,
        method: "resources/read",
        params: { uri },
      },
      null,
      2,
    );

  const sampleCatalogToolsCall = JSON.stringify(
    {
      jsonrpc: "2.0",
      id: 2,
      method: "tools/call",
      params: {
        name: MYCONTEXT_CATALOG_TOOL_NAME,
        arguments: {},
      },
    },
    null,
    2,
  );

  const skillTools = data.tools.filter(
    (t) => t.name !== MYCONTEXT_CATALOG_TOOL_NAME,
  );
  const catalogToolRow = data.tools.find(
    (t) => t.name === MYCONTEXT_CATALOG_TOOL_NAME,
  );
  const catalogToolUsage = lookupCapabilityUsage(
    capabilityUsageByTarget,
    "tool",
    MYCONTEXT_CATALOG_TOOL_NAME,
  );
  const totalCapabilities =
    skillTools.length + data.resources.length + data.prompts.length;

  const hasCustomOverride = Boolean(
    data.catalog_markdown_override?.trim().length,
  );
  const trimmedDraft = draft.trim();
  const catalogMarkdownGenerated =
    data.catalog_markdown_generated ?? data.catalog_markdown ?? "";
  const trimmedGen = catalogMarkdownGenerated.trim();
  const wouldSendEmpty = trimmedDraft === trimmedGen;
  const customTooLong =
    !wouldSendEmpty && trimmedDraft.length > CATALOG_MARKDOWN_MAX_CHARS;
  const catalogDirty = trimmedDraft !== (data.catalog_markdown ?? "").trim();
  const catalogSaving = saveMutation.isPending || restoreMutation.isPending;

  const liveCatalogMarkdown = data.catalog_markdown ?? "";
  const catalogPreviewEmpty = !liveCatalogMarkdown.trim();

  return (
    <div className="space-y-6">
      <section
        className={MCP_CATALOG_SECTION}
        aria-labelledby="mcp-catalog-heading"
      >
        <div className="space-y-1.5">
          <h2
            id="mcp-catalog-heading"
            className="text-base leading-snug font-medium text-foreground"
          >
            MCP Catalog
          </h2>
          <p className="text-sm text-muted-foreground">
            Markdown returned by{" "}
            <code className="font-mono text-xs">{MYCONTEXT_CATALOG_TOOL_NAME}</code>{" "}
            for this project&apos;s{" "}
            <span className="font-medium text-foreground">Active Release</span>.
            It is built from the release unless you save a custom override in
            the editor.
          </p>
        </div>
        {!data.release_id && (
          <p className="text-muted-foreground text-sm">
            No active release yet — connect a repo, sync, then activate a ready
            release.
          </p>
        )}
        {data.release_id && (
          <p className="text-muted-foreground text-sm">
            Release <span className="font-mono">{data.release_id}</span>
            {data.release_status ? ` · status: ${data.release_status}` : ""}
            {totalCapabilities === 0
              ? " · no skill capabilities in this release (check skill exposure types in compiled skills)."
              : ` · ${totalCapabilities} ${pluralEn(totalCapabilities, "skill capability", "skill capabilities")} (plus discovery tool ${MYCONTEXT_CATALOG_TOOL_NAME}).`}
          </p>
        )}

        <div className={MCP_CATALOG_SUBPANEL}>
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-sm font-medium">Catalog Markdown</h3>
              {hasCustomOverride ? (
                <span className="bg-primary/10 text-primary inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium leading-none">
                  Custom Text
                </span>
              ) : (
                <span className="bg-muted inline-flex items-center rounded-md px-2 py-0.5 text-xs leading-none">
                  Auto-Generated
                </span>
              )}
            </div>
            <p className="text-muted-foreground text-xs leading-relaxed">
              This is the markdown returned by{" "}
              <code className="font-mono">{MYCONTEXT_CATALOG_TOOL_NAME}</code>. By
              default it is built from the active release; you can replace it
              with your own text (for example a shorter onboarding blurb).
              Saving text identical to the auto-generated catalog clears the
              custom override.
            </p>
          </div>
          <div className="space-y-2">
            <p className="text-muted-foreground text-xs font-medium">
              Preview (Live Catalog)
            </p>
            <div
              className={cn(
                "max-h-40 overflow-auto rounded-lg p-3",
                glassSurfaceClasses("subtle"),
              )}
            >
              {catalogPreviewEmpty ? (
                <span className="text-muted-foreground text-xs leading-relaxed">
                  No catalog text yet. Sync and activate a release, or open the
                  editor to add custom markdown.
                </span>
              ) : (
                <MarkdownPreview markdown={liveCatalogMarkdown} />
              )}
            </div>
            {catalogDirty ? (
              <p className="text-amber-700 text-xs dark:text-amber-400">
                You have unsaved edits in the editor — open it to save or adjust
                before leaving the page.
              </p>
            ) : null}
            <Button
              type="button"
              size="sm"
              onClick={() => {
                setSaveError(null);
                setCatalogEditorOpen(true);
              }}
            >
              Edit Catalog Markdown…
            </Button>
          </div>
        </div>

        <Dialog
          open={catalogEditorOpen}
          onOpenChange={(open) => {
            setCatalogEditorOpen(open);
            if (!open) setSaveError(null);
          }}
        >
          <DialogContent
            showCloseButton
            className="flex h-[min(90vh,calc(100vh-2rem))] max-h-[min(90vh,calc(100vh-2rem))] w-full flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(56rem,calc(100vw-2rem))]"
          >
            <DialogHeader className="shrink-0 space-y-2 border-b border-border/50 px-4 py-3">
              <DialogTitle>Edit Catalog Markdown</DialogTitle>
              <DialogDescription>
                Markdown returned by{" "}
                <code className="font-mono text-xs">
                  {MYCONTEXT_CATALOG_TOOL_NAME}
                </code>
                . Saving text identical to the auto-generated catalog clears the
                custom override.
              </DialogDescription>
            </DialogHeader>
            <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-hidden px-4 py-3">
              <Label
                htmlFor={`mcp-catalog-md-${projectId}`}
                className="sr-only"
              >
                Catalog Markdown
              </Label>
              <div
                className={cn(
                  "flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg p-3",
                  glassSurfaceClasses("default"),
                )}
              >
                <textarea
                  id={`mcp-catalog-md-${projectId}`}
                  value={draft}
                  onChange={(e) => {
                    setSaveError(null);
                    setDraft(e.target.value);
                  }}
                  spellCheck={false}
                  aria-invalid={customTooLong}
                  className={cn(
                    "min-h-[min(50vh,26rem)] w-full flex-1 resize-y rounded-md border border-input/80 bg-transparent px-2.5 py-2 font-mono text-xs",
                    "placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50",
                    "outline-none dark:bg-input/20",
                    customTooLong &&
                      "border-destructive focus-visible:border-destructive",
                  )}
                />
              </div>
              <div className="text-muted-foreground flex flex-wrap items-center justify-between gap-2 text-xs">
                <span>
                  {trimmedDraft.length.toLocaleString()} /{" "}
                  {CATALOG_MARKDOWN_MAX_CHARS.toLocaleString()} characters
                  (custom text)
                </span>
                {customTooLong ? (
                  <span className="text-destructive font-medium">
                    Too Long to Save
                  </span>
                ) : null}
              </div>
              {saveError ? (
                <p className="text-destructive text-xs">{saveError}</p>
              ) : null}
            </div>
            <DialogFooter className="mx-0 mb-0 mt-0 shrink-0 border-t border-border/40 bg-background/35 px-4 py-3 supports-backdrop-filter:bg-background/22 sm:justify-end">
              <Button
                type="button"
                variant="outline"
                onClick={() => setCatalogEditorOpen(false)}
              >
                Cancel
              </Button>
              <Button
                type="button"
                variant="outline"
                disabled={catalogSaving || !hasCustomOverride}
                onClick={() => restoreMutation.mutate()}
              >
                Use Auto-Generated Catalog
              </Button>
              <Button
                type="button"
                disabled={
                  catalogSaving ||
                  !catalogDirty ||
                  customTooLong ||
                  (wouldSendEmpty && !hasCustomOverride)
                }
                onClick={() => saveMutation.mutate()}
              >
                Save Catalog Markdown
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </section>

      <section
        className={MCP_CATALOG_SECTION}
        aria-labelledby="mcp-connect-heading"
      >
        <div className="space-y-1.5">
          <h2
            id="mcp-connect-heading"
            className="text-base leading-snug font-medium text-foreground"
          >
            Connect
          </h2>
          <p className="text-sm text-muted-foreground">
            {oauthOnHost
              ? "Use an API key or an OAuth access token with your MCP URL to run JSON-RPC over HTTP, then list tools, resources, and prompts."
              : "Use an API key and your MCP URL to run JSON-RPC over HTTP, then list tools, resources, and prompts."}
          </p>
        </div>
        {oauthOnHost && oauthOrigin ? (
          <div
            className={cn(
              MCP_CATALOG_SUBPANEL,
              "text-muted-foreground space-y-2 text-sm",
            )}
          >
            <p className="font-medium text-foreground">OAuth on This MCP Host</p>
            <p>
              Claude Code:{" "}
              <code className="font-mono text-xs break-all">
                claude mcp add --transport http mycontext {url ?? "https://your-mcp-host/mcp"}
              </code>
              , then run <code className="font-mono text-xs">/mcp</code> and authenticate.
              Use <code className="font-mono text-xs">--callback-port</code> if your OAuth
              redirect URI setup requires a fixed localhost port. If the browser cannot
              reach Claude&apos;s local callback, paste the full callback URL back into Claude
              Code. Claude.ai Custom Connectors should use the full HTTPS MCP URL.
            </p>
            <p>
              OAuth discovery (same origin as your MCP URL):{" "}
              <code className="font-mono text-xs break-all">
                {oauthOrigin}/.well-known/oauth-protected-resource
              </code>{" "}
              and{" "}
              <code className="font-mono text-xs break-all">
                {oauthOrigin}/.well-known/oauth-authorization-server
              </code>
              . Interactive clients use authorization code + PKCE (
              <code className="font-mono text-xs">/authorize</code>, consent, then{" "}
              <code className="font-mono text-xs">POST /token</code>
              ). Machine clients use{" "}
              <code className="font-mono text-xs">client_credentials</code> on{" "}
              <code className="font-mono text-xs">POST /token</code> when the server has a
              registered confidential client. Send MCP traffic with{" "}
              <code className="font-mono text-xs">
                Authorization: Bearer &lt;access_token&gt;
              </code>
              .
            </p>
            <p>
              If Claude authentication fails, check that protected-resource metadata,
              authorization-server metadata, dynamic registration scopes, and token
              response scopes all agree on{" "}
              <code className="font-mono text-xs">mcp:invoke</code>
              . For custom domains, verify the public scheme/host seen by the MCP host
              matches the URL Claude uses. On WSL2, default NAT networking can prevent a
              Windows browser from reaching Claude Code&apos;s WSL localhost callback.
            </p>
          </div>
        ) : null}
        <ol className="text-muted-foreground list-inside list-decimal space-y-1 text-sm">
          <li>
            {oauthOnHost ? (
              <>
                Create an API key (API Keys tab) or obtain an OAuth access token via the MCP host
                flow (GitHub sign-in when the consent step redirects you).
              </>
            ) : (
              <>Create an API Key (API Keys Tab).</>
            )}
          </li>
          <li>
            Send JSON-RPC <code className="font-mono text-xs">POST</code> to
            your MCP URL with header{" "}
            <code className="font-mono text-xs">
              Authorization: Bearer &lt;
              {oauthOnHost ? "api_key_or_access_token" : "key"}
              &gt;
            </code>
            .
          </li>
          <li>
            Call <code className="font-mono text-xs">initialize</code>, then{" "}
            <code className="font-mono text-xs">tools/list</code> (includes{" "}
            <code className="font-mono text-xs">{MYCONTEXT_CATALOG_TOOL_NAME}</code>
            ), <code className="font-mono text-xs">resources/list</code>, or{" "}
            <code className="font-mono text-xs">prompts/list</code>. Use{" "}
            <strong className="font-medium text-foreground">
              Agent Discovery
            </strong>{" "}
            below to copy a{" "}
            <code className="font-mono text-xs">tools/call</code> payload and
            preview the catalog markdown agents should read first.
          </li>
          <li>
            Optional: open a long-lived{" "}
            <code className="font-mono text-xs">GET</code> to{" "}
            <code className="font-mono text-xs">…/events</code> with the same
            bearer key for SSE{" "}
            <code className="font-mono text-xs">list_changed</code>{" "}
            notifications; otherwise compare{" "}
            <code className="font-mono text-xs">X-MCP-Catalog-Revision</code> on
            responses.
          </li>
        </ol>
        {url ? (
          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <code
                className={cn(
                  MCP_CATALOG_INSET_SURFACE,
                  "max-w-full flex-1 break-all px-2 py-1 text-xs",
                )}
              >
                {url}
              </code>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() =>
                  void copyTextToClipboard(url, {
                    success: "Catalog URL copied to clipboard",
                    error: "Could not copy catalog URL",
                  })
                }
              >
                <CopyIcon className="mr-1 h-3.5 w-3.5" />
                Copy URL
              </Button>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <code
                className={cn(
                  MCP_CATALOG_INSET_SURFACE,
                  "max-w-full flex-1 break-all px-2 py-1 text-xs",
                )}
              >
                {mcpEventsUrl(url)}
              </code>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() =>
                  void copyTextToClipboard(mcpEventsUrl(url), {
                    success: "MCP events URL copied",
                    error: "Could not copy events URL",
                  })
                }
              >
                <CopyIcon className="mr-1 h-3.5 w-3.5" />
                Copy events URL
              </Button>
            </div>
          </div>
        ) : (
          <p className="text-muted-foreground text-xs">
            MCP URL unavailable — set{" "}
            <code className="font-mono">SAAS_MCP_BASE_DOMAIN</code> on the API
            or verify a custom domain.
          </p>
        )}
        <div className="space-y-1">
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground text-xs">
              Sample Initialize Body
            </span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={() =>
                void copyTextToClipboard(sampleInitialize, {
                  success: "Sample JSON copied to clipboard",
                  error: "Could not copy sample JSON",
                })
              }
            >
              <CopyIcon className="mr-1 h-3 w-3" />
              Copy JSON
            </Button>
          </div>
          <pre
            className={cn(
              MCP_CATALOG_INSET_SURFACE,
              "max-h-40 overflow-auto p-3 text-xs leading-relaxed",
            )}
          >
            {sampleInitialize}
          </pre>
        </div>
        <div className="space-y-1">
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground text-xs">
              Example Initialize Response (Shape)
            </span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={() =>
                void copyTextToClipboard(sampleInitializeResponse, {
                  success: "Sample response copied to clipboard",
                  error: "Could not copy sample response",
                })
              }
            >
              <CopyIcon className="mr-1 h-3 w-3" />
              Copy JSON
            </Button>
          </div>
          <pre
            className={cn(
              MCP_CATALOG_INSET_SURFACE,
              "max-h-48 overflow-auto p-3 text-xs leading-relaxed",
            )}
          >
            {sampleInitializeResponse}
          </pre>
        </div>
      </section>

      <section
        className={MCP_CATALOG_SECTION}
        aria-labelledby="mcp-agent-discovery-heading"
      >
        <div className="space-y-1.5">
          <h2
            id="mcp-agent-discovery-heading"
            className="text-base leading-snug font-medium text-foreground"
          >
            Agent Discovery
          </h2>
        </div>
        <div className="space-y-3">
          <div className="space-y-1">
            <p className="text-muted-foreground text-sm leading-relaxed">
              After <code className="font-mono text-xs">initialize</code>,
              agents should call{" "}
              <code className="font-mono text-xs">tools/call</code> with tool
              name{" "}
              <code className="font-mono text-xs">
                {MYCONTEXT_CATALOG_TOOL_NAME}
              </code>{" "}
              to get a markdown map of skill tools, resources, and prompts
              (routing hints, URIs, and when to use each). That steers how the
              agent accesses the rest of this project without guessing from{" "}
              <code className="font-mono text-xs">tools/list</code> alone. Any{" "}
              <code className="font-mono text-xs">detail</code> argument is
              ignored for this tool (it is only used for other compiled skill
              tools).
            </p>
            {catalogToolRow?.description ? (
              <p className="text-muted-foreground text-xs leading-relaxed">
                {catalogToolRow.description}
              </p>
            ) : null}
            <p className="text-muted-foreground text-xs leading-relaxed">
              Recent usage for{" "}
              <code className="font-mono text-[0.7rem]">
                {MYCONTEXT_CATALOG_TOOL_NAME}
              </code>{" "}
              (last 7 days):{" "}
              {!dashboardMetricsFetched ? (
                <span className="text-muted-foreground">Loading…</span>
              ) : catalogToolUsage ? (
                <>
                  {catalogToolUsage.invocations_last_7d.toLocaleString()}{" "}
                  {pluralEn(
                    catalogToolUsage.invocations_last_7d,
                    "invocation",
                    "invocations",
                  )}
                  , {catalogToolUsage.successful_last_7d.toLocaleString()}{" "}
                  {pluralEn(
                    catalogToolUsage.successful_last_7d,
                    "success",
                    "successes",
                  )}{" "}
                  (HTTP 2xx/3xx with no logged JSON-RPC error).
                </>
              ) : (
                "— (no recorded calls yet)."
              )}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() =>
                void copyTextToClipboard(MYCONTEXT_CATALOG_TOOL_NAME, {
                  success: "Tool name copied",
                  error: "Could not copy",
                })
              }
            >
              <CopyIcon className="mr-1 h-3.5 w-3.5" />
              Copy tool name
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() =>
                void copyTextToClipboard(sampleCatalogToolsCall, {
                  success: "tools/call JSON copied",
                  error: "Could not copy JSON",
                })
              }
            >
              <CopyIcon className="mr-1 h-3.5 w-3.5" />
              Copy tools/call JSON
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() =>
                void copyTextToClipboard(data.catalog_markdown ?? "", {
                  success: "Catalog markdown copied",
                  error: "Could not copy markdown",
                })
              }
            >
              <CopyIcon className="mr-1 h-3.5 w-3.5" />
              Copy Catalog Markdown
            </Button>
          </div>
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2">
              <span className="text-muted-foreground text-xs">
                Sample <code className="font-mono">tools/call</code> body
              </span>
            </div>
            <pre
              className={cn(
                MCP_CATALOG_INSET_SURFACE,
                "max-h-40 overflow-auto p-3 text-xs leading-relaxed",
              )}
            >
              {sampleCatalogToolsCall}
            </pre>
          </div>
        </div>
      </section>

      <section
        className={cn(MCP_CATALOG_SECTION, "space-y-6")}
        aria-labelledby="mcp-capabilities-heading"
      >
        <div className="space-y-1.5">
          <h2
            id="mcp-capabilities-heading"
            className="text-base leading-snug font-medium text-foreground"
          >
            Tools, Resources, and Prompts
          </h2>
          <p className="text-sm text-muted-foreground">
            Skill tools, resources, and prompts from the active release. Usage
            columns count MCP{" "}
            <code className="font-mono text-xs">tools/call</code>,{" "}
            <code className="font-mono text-xs">resources/read</code>, and{" "}
            <code className="font-mono text-xs">prompts/get</code> for this
            project in the last 7 days (from request logs).
            Resource entries can include agent routing from SKILL.md (
            <code className="font-mono text-xs">use_when</code>,{" "}
            <code className="font-mono text-xs">avoid_when</code>,{" "}
            <code className="font-mono text-xs">failure_modes</code>,{" "}
            <code className="font-mono text-xs">invoke_first</code>) — the same
            data is returned in{" "}
            <code className="font-mono text-xs">resources/list</code> and
            summarized at the top of{" "}
            <code className="font-mono text-xs">resources/read</code>.
          </p>
        </div>

        <div className="space-y-2">
          <h3 className="text-sm font-medium">Skill Tools</h3>
          {skillTools.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              None from the active release yet. The discovery tool{" "}
              <code className="font-mono text-xs">
                {MYCONTEXT_CATALOG_TOOL_NAME}
              </code>{" "}
              is still listed above once you connect over MCP.
            </p>
          ) : (
            <Table>
              <TableCaption className="sr-only">
                MCP skill tools from the active release (excludes synthetic
                catalog tool).
              </TableCaption>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Description</TableHead>
                  <TableHead className="text-right">Invocations (7d)</TableHead>
                  <TableHead className="text-right">Successful (7d)</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {skillTools.map((t) => (
                  <TableRow key={t.name}>
                    <TableCell className="font-mono text-xs">
                      {t.name}
                    </TableCell>
                    <TableCell className="max-w-md text-sm">
                      {t.description ?? "—"}
                    </TableCell>
                    <CapabilityUsageCells
                      usage={lookupCapabilityUsage(
                        capabilityUsageByTarget,
                        "tool",
                        t.name,
                      )}
                    />
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </div>

        <div className="space-y-2">
          <h3 className="text-sm font-medium">Resources</h3>
          {data.resources.length === 0 ? (
            <p className="text-muted-foreground text-sm">None.</p>
          ) : (
            <div className="space-y-4">
              {data.resources.map((r) => {
                const useWhen = r.use_when ?? [];
                const avoidWhen = r.avoid_when ?? [];
                const failureModes = r.failure_modes ?? [];
                const hasHints =
                  useWhen.length > 0 ||
                  avoidWhen.length > 0 ||
                  failureModes.length > 0 ||
                  r.invoke_first === true;
                const readRpc = sampleResourcesRead(r.uri);
                const resourceUsage = lookupCapabilityUsage(
                  capabilityUsageByTarget,
                  "resource",
                  r.uri,
                );
                return (
                  <div
                    key={r.uri}
                    className={cn(MCP_CATALOG_SUBPANEL, "text-sm")}
                  >
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div className="min-w-0 flex-1 space-y-1">
                        <p className="font-medium leading-tight">
                          {r.name ?? "—"}
                        </p>
                        <code className="text-muted-foreground block break-all font-mono text-xs">
                          {r.uri}
                        </code>
                        {r.description ? (
                          <p className="text-muted-foreground text-xs leading-relaxed">
                            {r.description}
                          </p>
                        ) : null}
                        <p className="text-muted-foreground text-xs tabular-nums">
                          Usage (7d):{" "}
                          {resourceUsage
                            ? `${resourceUsage.invocations_last_7d.toLocaleString()} reads · ${resourceUsage.successful_last_7d.toLocaleString()} successful`
                            : "—"}
                        </p>
                      </div>
                      <div className="flex w-full flex-shrink-0 flex-wrap items-center justify-center gap-2 sm:w-auto sm:justify-end">
                        {r.invoke_first ? (
                          <span className="bg-primary/10 text-primary inline-flex items-center rounded-md px-2 py-1 text-xs font-medium leading-none">
                            Invoke First
                          </span>
                        ) : null}
                        {hasHints && !r.invoke_first ? (
                          <span className="bg-muted inline-flex items-center rounded-md px-2 py-1 text-xs leading-none">
                            Agent Hints
                          </span>
                        ) : null}
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          className="h-8"
                          onClick={() =>
                            void copyTextToClipboard(r.uri, {
                              success: "Resource URI copied",
                              error: "Could not copy URI",
                            })
                          }
                        >
                          <CopyIcon className="mr-1 h-3 w-3" />
                          Copy URI
                        </Button>
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          className="h-8"
                          onClick={() =>
                            void copyTextToClipboard(readRpc, {
                              success: "resources/read JSON copied",
                              error: "Could not copy JSON",
                            })
                          }
                        >
                          <CopyIcon className="mr-1 h-3 w-3" />
                          Copy read RPC
                        </Button>
                      </div>
                    </div>
                    {hasHints ? (
                      <div className="grid gap-3 border-t pt-3 sm:grid-cols-3">
                        {useWhen.length > 0 ? (
                          <div>
                            <p className="mb-1 text-xs font-medium text-foreground/90">
                              Read When
                            </p>
                            <ul className="text-muted-foreground list-inside list-disc space-y-0.5 text-xs">
                              {useWhen.map((line) => (
                                <li key={line}>{line}</li>
                              ))}
                            </ul>
                          </div>
                        ) : null}
                        {avoidWhen.length > 0 ? (
                          <div>
                            <p className="mb-1 text-xs font-medium text-foreground/90">
                              Skip When
                            </p>
                            <ul className="text-muted-foreground list-inside list-disc space-y-0.5 text-xs">
                              {avoidWhen.map((line) => (
                                <li key={line}>{line}</li>
                              ))}
                            </ul>
                          </div>
                        ) : null}
                        {failureModes.length > 0 ? (
                          <div className="sm:col-span-3">
                            <p className="mb-1 text-xs font-medium text-foreground/90">
                              Failure Modes / Fallbacks
                            </p>
                            <ul className="text-muted-foreground list-inside list-disc space-y-0.5 text-xs">
                              {failureModes.map((line) => (
                                <li key={line}>{line}</li>
                              ))}
                            </ul>
                          </div>
                        ) : null}
                      </div>
                    ) : (
                      <p className="text-muted-foreground border-t pt-2 text-xs">
                        No routing metadata on this resource yet. Add lists in
                        SKILL.md front matter (
                        <code className="font-mono">use_when</code>, etc.) and
                        re-sync the repo.
                      </p>
                    )}
                    <details className="text-xs">
                      <summary className="cursor-pointer font-medium text-foreground/90">
                        Sample <code className="font-mono">resources/read</code>{" "}
                        body
                      </summary>
                      <pre
                        className={cn(
                          MCP_CATALOG_INSET_SURFACE,
                          "mt-2 max-h-36 overflow-auto p-2 leading-relaxed",
                        )}
                      >
                        {readRpc}
                      </pre>
                    </details>
                  </div>
                );
              })}
            </div>
          )}
        </div>

        <div className="space-y-2">
          <h3 className="text-sm font-medium">Prompts</h3>
          {data.prompts.length === 0 ? (
            <p className="text-muted-foreground text-sm">None.</p>
          ) : (
            <Table>
              <TableCaption className="sr-only">
                MCP prompts exposed for this project&apos;s active release.
              </TableCaption>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Description</TableHead>
                  <TableHead className="text-right">Invocations (7d)</TableHead>
                  <TableHead className="text-right">Successful (7d)</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.prompts.map((p) => (
                  <TableRow key={p.name}>
                    <TableCell className="font-mono text-xs">
                      {p.name}
                    </TableCell>
                    <TableCell className="max-w-md text-sm">
                      {p.description ?? "—"}
                    </TableCell>
                    <CapabilityUsageCells
                      usage={lookupCapabilityUsage(
                        capabilityUsageByTarget,
                        "prompt",
                        p.name,
                      )}
                    />
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </div>
      </section>
    </div>
  );
}

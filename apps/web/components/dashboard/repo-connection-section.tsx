"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  fetchRepoConnection,
  type RepositoryMigrationSource,
  connectRepo,
  fetchUserGithubRepos,
  triggerSync,
} from "@/lib/projects-api";
import { ApiError } from "@/lib/api";
import { assertGitHubInstallUrl } from "@/lib/trusted-redirect";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { repositoryProviderLabel, repositoryOwnerLabel } from "./project-connection-summary";
import { RepositoryMigrationPreparation } from "./repository-migration-preparation";
import { TangledRepositoryForm } from "./tangled-repository-form";
import { RefreshCwIcon } from "lucide-react";

const schema = z.object({
  full_name: z.string().min(1, "Select a repository"),
  branch: z.string().min(1, "Branch is required"),
});

type FormData = z.infer<typeof schema>;

interface RepoConnectionSectionProps {
  projectId: string;
  onReviewPublication?: () => void;
}

function reposErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 400) {
      return "Sign out and sign in again so we can load your GitHub repositories.";
    }
    if (err.status === 502) {
      return "GitHub did not return your repository list. Try again in a moment.";
    }
  }
  return "Could not load your repositories.";
}

export function RepoConnectionSection({ projectId, onReviewPublication }: RepoConnectionSectionProps) {
  const [provider, setProvider] = useState<"github" | "tangled">("github");
  const [syncPending, setSyncPending] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [migrationSource, setMigrationSource] = useState<RepositoryMigrationSource>();
  const [tangledPending, setTangledPending] = useState(false);
  const [migrationConfirmed, setMigrationConfirmed] = useState(false);
  const resumedRepository = useRef<string | null>(null);
  const [installUrl, setInstallUrl] = useState("");
  const [repoFilter, setRepoFilter] = useState("");
  const [syncError, setSyncError] = useState<string | null>(null);
  const [connectError, setConnectError] = useState<string | null>(null);
  const queryClient = useQueryClient();
  const pendingConnectKey = `pendingRepoConnect:${projectId}`;

  const { data: connection, isLoading, isError: connectionError, refetch: refetchConnection } = useQuery({
    queryKey: ["repo-connection", projectId],
    queryFn: () => fetchRepoConnection(projectId),
  });

  const reposQuery = useQuery({
    queryKey: ["github-repos"],
    queryFn: fetchUserGithubRepos,
    enabled: showForm && provider === "github",
    staleTime: 60_000,
  });

  const filteredRepos = useMemo(() => {
    const repos = reposQuery.data;
    if (!repos) return [];
    const q = repoFilter.trim().toLowerCase();
    if (!q) return repos;
    return repos.filter((r) => r.full_name.toLowerCase().includes(q));
  }, [reposQuery.data, repoFilter]);

  const {
    register,
    control,
    handleSubmit,
    reset,
    setValue,
    getValues,
    watch,
    formState: { errors },
  } = useForm<FormData>({
    resolver: zodResolver(schema),
    defaultValues: { full_name: "", branch: "main" },
  });

  // react-hook-form watch is synchronous; exhaustive-deps lint treats the library as incompatible.
  /* eslint-disable-next-line react-hooks/incompatible-library -- form watch for dependent UI */
  const fullName = watch("full_name");

  useEffect(() => {
    const params = new URLSearchParams({
      project_id: projectId,
      return_to: `${window.location.origin}/projects/${encodeURIComponent(projectId)}?tab=repo`,
    });
    setInstallUrl(`/api/auth/github/app/install?${params}`);
  }, [projectId]);

  // Refresh access after installation, even when no repository was selected yet.
  useEffect(() => {
    if (typeof window === "undefined") return;
    const params = new URLSearchParams(window.location.search);
    if (params.get("github_app_installed") !== "1") return;

    void queryClient.invalidateQueries({ queryKey: ["github-repos"], refetchType: "none" });
    setShowForm(true);
    setRepoFilter("");

    const stored = sessionStorage.getItem(pendingConnectKey);
    if (stored) {
      try {
        const parsed = JSON.parse(stored) as {
          full_name: string;
          branch: string;
          migrate_from?: RepositoryMigrationSource;
        };
        sessionStorage.removeItem(pendingConnectKey);
        resumedRepository.current = parsed.full_name;
        setMigrationSource(parsed.migrate_from);
        setMigrationConfirmed(false);
        setShowForm(true);
        reset({ full_name: parsed.full_name, branch: parsed.branch || "main" });
      } catch {
        sessionStorage.removeItem(pendingConnectKey);
      }
    } else {
      const ro = params.get("resume_owner");
      const rr = params.get("resume_repo");
      if (ro && rr) {
        setShowForm(true);
        reset({ full_name: `${ro}/${rr}`, branch: "main" });
      }
    }

    params.set("tab", "repo");
    params.delete("github_app_installed");
    params.delete("resume_owner");
    params.delete("resume_repo");
    const qs = params.toString();
    const path = window.location.pathname + (qs ? `?${qs}` : "") + window.location.hash;
    window.history.replaceState({}, "", path);
  }, [projectId, pendingConnectKey, queryClient, reset]);

  useEffect(() => {
    if (!reposQuery.data?.length || !fullName) return;
    // Preserve an explicitly chosen branch when installation resumes a connection.
    if (resumedRepository.current === fullName) return;
    resumedRepository.current = null;
    const row = reposQuery.data.find((r) => r.full_name === fullName);
    if (row) {
      setValue("branch", row.default_branch || "main");
    }
  }, [fullName, reposQuery.data, setValue]);

  const connectMutation = useMutation({
    mutationFn: async (data: FormData) => {
      if (migrationSource && !migrationConfirmed) throw new Error("Confirm the destination before migrating.");
      const i = data.full_name.indexOf("/");
      const owner = data.full_name.slice(0, i);
      const repo = data.full_name.slice(i + 1);
      const branch = data.branch.trim();
      try {
        return await connectRepo(projectId, {
          owner,
          repo,
          branch,
          ...(migrationSource ? { migrate_from: migrationSource } : {}),
        });
      } catch (err) {
        if (err instanceof ApiError && err.status === 409 && err.body && typeof err.body === "object") {
          const installUrl = (err.body as { install_url?: string }).install_url;
          if (installUrl) {
            if (typeof window !== "undefined") {
              assertGitHubInstallUrl(installUrl, { origin: window.location.origin, projectId });
              sessionStorage.setItem(
                pendingConnectKey,
                JSON.stringify({
                  full_name: data.full_name,
                  branch,
                  owner,
                  repo,
                  migrate_from: migrationSource,
                })
              );
              window.location.href = installUrl;
            }
            return undefined as unknown as Awaited<ReturnType<typeof connectRepo>>;
          }
        }
        throw err;
      }
    },
    onMutate: () => setConnectError(null),
    onSuccess: (data) => {
      if (data == null) return;
      queryClient.invalidateQueries({ queryKey: ["repo-connection", projectId] });
      queryClient.invalidateQueries({ queryKey: ["releases", projectId] });
      queryClient.invalidateQueries({ queryKey: ["project", projectId] });
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      queryClient.invalidateQueries({ queryKey: ["project-dashboard-summary", projectId] });
      queryClient.invalidateQueries({ queryKey: ["account-dashboard-summary"] });
      resumedRepository.current = null;
      setShowForm(false);
      setMigrationSource(undefined);
      setMigrationConfirmed(false);
      setRepoFilter("");
      reset({ full_name: "", branch: "main" });
    },
    onError: (err) => {
      if (err instanceof ApiError && typeof err.body === "object" && err.body && "reason" in err.body) {
        setConnectError(String((err.body as { reason: unknown }).reason));
      } else {
        setConnectError("Could not connect that repository.");
      }
    },
  });

  const syncMutation = useMutation({
    mutationFn: () => triggerSync(projectId),
    onMutate: () => setSyncError(null),
    onSuccess: (result) => {
      setSyncPending(result?.status === "publishing");
      queryClient.invalidateQueries({ queryKey: ["releases", projectId] });
      queryClient.invalidateQueries({ queryKey: ["repo-connection", projectId] });
      queryClient.invalidateQueries({ queryKey: ["project-catalog", projectId] });
      queryClient.invalidateQueries({ queryKey: ["project", projectId] });
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      queryClient.invalidateQueries({ queryKey: ["project-dashboard-summary", projectId] });
      queryClient.invalidateQueries({ queryKey: ["account-dashboard-summary"] });
    },
    onError: (err) => {
      setSyncPending(false);
      if (err instanceof ApiError && err.status === 429) {
        setSyncError("Too many sync requests. Try again after a short wait.");
      } else {
        setSyncError("Sync failed. Try again.");
      }
    },
  });

  const syncRunning = syncMutation.isPending;
  const continueSync = syncMutation.mutate;
  useEffect(() => {
    if (!syncPending || syncRunning) return;
    const timer = window.setTimeout(() => continueSync(), 1500);
    return () => window.clearTimeout(timer);
  }, [syncPending, syncRunning, continueSync]);

  if (isLoading) {
    return <Skeleton className="h-48" />;
  }

  if (connectionError) {
    return <Card><CardContent className="space-y-3 pt-6"><p role="alert">Could not load the repository connection.</p><Button onClick={() => refetchConnection()}>Retry connection</Button></CardContent></Card>;
  }

  if (connection && !showForm) {
    const automaticSync = connection.provider === "tangled" || Boolean(connection.webhook_id || connection.github_installation_configured);
    return (
      <Card>
        <CardHeader>
          <CardTitle>Repository Connection</CardTitle>
          <CardDescription>
            {connection.provider === "tangled" ? "Scheduled reconciliation checks this Tangled repository for changes. You can also sync manually." : automaticSync
              ? "Pushes to the default branch trigger a sync automatically."
              : "Automatic sync is not configured for this connection. Use Sync Now after you push."}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <dl className="space-y-2 text-sm">
            <div>
              <dt className="text-muted-foreground">Connected through</dt>
              <dd className="font-medium">{repositoryProviderLabel(connection.provider)}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Repository</dt>
              <dd className="font-medium">
                {repositoryOwnerLabel(connection)}/{connection.repo_name}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Branch</dt>
              <dd className="font-medium">{connection.default_branch}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Change detection</dt>
              <dd className="font-medium">
                {connection.provider === "tangled" ? "Scheduled reconciliation" : connection.github_installation_configured
                  ? "Configured through GitHub App"
                  : connection.webhook_id ? "Configured" : "Not configured (manual sync)"}
              </dd>
            </div>
          </dl>
          {connection.provider === "tangled" && connection.atproto_publication_completed === false && <div className="space-y-3 rounded-md border p-4">
            <h3 className="font-medium">Finish ATProto migration</h3>
            <p className="text-muted-foreground text-sm">Your repository is connected. The project’s MCP metadata has not been published to your ATProto account yet. Review the records, explicitly authorize publication, then verify them to finish.</p>
            <Button variant="outline" onClick={onReviewPublication}>Review and publish project records</Button>
          </div>}
          {syncPending && <div className="space-y-2"><p role="status" className="text-sm">Publishing the next batch automatically. The previous release remains active until completion.</p><Button variant="outline" onClick={() => setSyncPending(false)}>Pause automatic continuation</Button></div>}
          {syncError && (
            <p className="text-destructive text-sm">{syncError}</p>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <Button
              onClick={() => syncMutation.mutate()}
              disabled={syncMutation.isPending}
            >
              <RefreshCwIcon
                className={`h-4 w-4 ${syncMutation.isPending ? "animate-spin" : ""}`}
              />
              {syncMutation.isPending ? "Syncing…" : syncPending ? "Continue Sync" : "Sync Now"}
            </Button>
            {(connection.provider === "github" || connection.provider === "tangled") && <Button variant="outline" disabled={syncMutation.isPending || syncPending} onClick={() => {
              setMigrationSource({ provider: connection.provider, repo_owner: connection.repo_owner, repo_name: connection.repo_name, default_branch: connection.default_branch });
              setMigrationConfirmed(false);
              setProvider(connection.provider === "github" ? "tangled" : "github");
              setShowForm(true);
            }}>Migrate to {connection.provider === "github" ? "ATProto · Tangled" : "GitHub"}</Button>}
          </div>
        </CardContent>
      </Card>
    );
  }

  if (showForm) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{migrationSource ? "Step 1: Connect destination repository" : "Connect Repository"}</CardTitle>
          <CardDescription>
            Choose GitHub or Tangled to sync SKILL.md files.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {migrationSource && <>
            <RepositoryMigrationPreparation source={migrationSource} sourceHandle={connection?.repo_owner === migrationSource.repo_owner ? connection.owner_handle : undefined} confirmed={migrationConfirmed} onConfirmedChange={setMigrationConfirmed} />
            <Button className="mb-4" variant="outline" disabled={connectMutation.isPending || tangledPending} onClick={() => { setShowForm(false); setMigrationSource(undefined); setMigrationConfirmed(false); setConnectError(null); }}>Cancel migration</Button>
          </>}
          {!migrationSource && <div className="mb-4 flex gap-2" role="group" aria-label="Repository provider">
            <Button variant={provider === "github" ? "default" : "outline"} onClick={() => setProvider("github")}>GitHub</Button>
            <Button variant={provider === "tangled" ? "default" : "outline"} onClick={() => setProvider("tangled")}>Tangled</Button>
          </div>}
          {provider === "tangled" ? <TangledRepositoryForm projectId={projectId} migrateFrom={migrationSource} onPendingChange={setTangledPending} onDestinationChange={() => setMigrationConfirmed(false)} disabled={!!migrationSource && !migrationConfirmed} onConnected={() => { setShowForm(false); setMigrationSource(undefined); setMigrationConfirmed(false); if (connection?.atproto_publication_completed !== true) onReviewPublication?.(); }} /> : <>
          {reposQuery.isPending && (
            <div className="space-y-3">
              <Skeleton className="h-8 w-full" />
              <Skeleton className="h-8 w-full" />
              <Skeleton className="h-8 w-32" />
            </div>
          )}
          {reposQuery.isError && (
            <p className="text-destructive mb-4 text-sm">
              {reposErrorMessage(reposQuery.error)}
            </p>
          )}
          {reposQuery.isSuccess && reposQuery.data.length === 0 && (
            <p className="text-muted-foreground mb-4 text-sm">
              No repositories are accessible to MyContextProtocol yet. Configure GitHub
              access to install the app or grant access to the repository containing your
              skills, then refresh the list.
            </p>
          )}
          {reposQuery.isSuccess && reposQuery.data.length > 0 && (
            <form
              onSubmit={handleSubmit((data) => connectMutation.mutate(data))}
              className="space-y-4"
            >
              <div className="space-y-2">
                <Label htmlFor="repo-filter">Search</Label>
                <Input
                  id="repo-filter"
                  value={repoFilter}
                  onChange={(e) => setRepoFilter(e.target.value)}
                  placeholder="Filter by name…"
                  autoComplete="off"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="full_name">Repository</Label>
                <Controller
                  name="full_name"
                  control={control}
                  render={({ field }) => (
                    <Select
                      value={field.value ? field.value : null}
                      onValueChange={(v) => { field.onChange(v ?? ""); setMigrationConfirmed(false); }}
                    >
                      <SelectTrigger
                        id="full_name"
                        className={cn(
                          "w-full min-w-0",
                          errors.full_name &&
                            "border-destructive ring-destructive/20 dark:border-destructive/50"
                        )}
                        aria-invalid={errors.full_name ? true : undefined}
                      >
                        <SelectValue placeholder="Choose a repository…" />
                      </SelectTrigger>
                      <SelectContent>
                        {filteredRepos.map((r) => (
                          <SelectItem key={r.full_name} value={r.full_name}>
                            <span className="min-w-0 truncate">
                              {r.full_name}
                              {r.is_private ? " (private)" : ""}
                            </span>
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )}
                />
                {errors.full_name && (
                  <p className="text-destructive text-sm">{errors.full_name.message}</p>
                )}
                {repoFilter && filteredRepos.length === 0 && (
                  <p className="text-muted-foreground text-sm">No matches.</p>
                )}
              </div>
              <div className="space-y-2">
                <Label htmlFor="branch">Default Branch</Label>
                <Input
                  id="branch"
                  {...register("branch", { onChange: () => setMigrationConfirmed(false) })}
                  placeholder="main"
                />
                {errors.branch && (
                  <p className="text-destructive text-sm">{errors.branch.message}</p>
                )}
                <p className="text-muted-foreground text-xs">
                  Usually filled from GitHub; change if your skills live on another branch.
                </p>
              </div>
              {connectError && (
                <p className="text-destructive text-sm">{connectError}</p>
              )}
              <div className="flex gap-2">
                <Button
                  type="button"
                  variant="outline"
                  disabled={connectMutation.isPending}
                  onClick={() => {
                    resumedRepository.current = null;
                    setShowForm(false);
                    setRepoFilter("");
                    reset({ full_name: "", branch: "main" });
                    setConnectError(null);
                  }}
                >
                  Cancel
                </Button>
                <Button type="submit" disabled={connectMutation.isPending || (!!migrationSource && !migrationConfirmed)}>
                  {connectMutation.isPending ? "Connecting…" : migrationSource ? "Migrate to GitHub" : "Connect"}
                </Button>
              </div>
            </form>
          )}
          <div className="mt-4 flex flex-wrap gap-2">
            <Button
              variant="outline"
              nativeButton={false}
              render={<a href={installUrl} onClick={() => {
                if (migrationSource) {
                  const values = getValues();
                  sessionStorage.setItem(pendingConnectKey, JSON.stringify({ ...values, migrate_from: migrationSource }));
                }
              }} />}
            >
              Configure GitHub Access
            </Button>
            {reposQuery.isSuccess && (
              <Button
                type="button"
                variant="outline"
                disabled={reposQuery.isFetching}
                onClick={() => reposQuery.refetch()}
              >
                {reposQuery.isFetching ? "Refreshing…" : "Refresh Repositories"}
              </Button>
            )}
          </div>
          {reposQuery.isSuccess && reposQuery.data.length === 0 && (
            <div className="flex gap-2 pt-2">
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  setShowForm(false);
                  setConnectError(null);
                }}
              >
                Back
              </Button>
            </div>
          )}
          {reposQuery.isError && (
            <div className="flex gap-2 pt-2">
              <Button type="button" variant="outline" onClick={() => setShowForm(false)}>
                Back
              </Button>
              <Button type="button" onClick={() => reposQuery.refetch()}>
                Retry
              </Button>
            </div>
          )}
          </>}
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Repository Connection</CardTitle>
        <CardDescription>
          Connect a GitHub or Tangled repository to sync SKILL.md files.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button onClick={() => setShowForm(true)}>Connect Repository</Button>
      </CardContent>
    </Card>
  );
}

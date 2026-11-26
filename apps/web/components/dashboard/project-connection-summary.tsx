import { Badge } from "@/components/ui/badge";
import type { Project, RepoConnection } from "@/lib/types";

export function repositoryProviderLabel(provider: string) {
  if (provider === "github") return "GitHub";
  if (provider === "tangled") return "ATProto · Tangled";
  return provider;
}

export function repositoryOwnerLabel(connection: Pick<RepoConnection, "provider" | "repo_owner" | "owner_handle">) {
  return connection.provider === "tangled" ? connection.owner_handle || connection.repo_owner : connection.repo_owner;
}

export function ProjectConnectionSummary({ project }: { project: Project }) {
  const connection = project.repo_connection;
  if (connection === undefined) {
    return <p className="text-muted-foreground text-sm">Repository connection unavailable</p>;
  }
  if (connection === null) {
    return <p className="text-muted-foreground text-sm">No repository connected</p>;
  }
  return (
    <div className="min-w-0 space-y-1.5 text-sm" aria-label="Repository connection">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline">{repositoryProviderLabel(connection.provider)}</Badge>
        {connection.provider === "tangled" && project.atproto_publication_completed === false && <Badge variant="secondary">Records not published</Badge>}
      </div>
      <p className="break-all font-medium">{repositoryOwnerLabel(connection)}/{connection.repo_name}</p>
      <p className="text-muted-foreground break-all">Branch: {connection.default_branch}</p>
    </div>
  );
}

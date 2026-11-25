import { Badge } from "@/components/ui/badge";
import type { Project } from "@/lib/types";

export function repositoryProviderLabel(provider: string) {
  if (provider === "github") return "GitHub";
  if (provider === "tangled") return "ATProto · Tangled";
  return provider;
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
      <Badge variant="outline">{repositoryProviderLabel(connection.provider)}</Badge>
      <p className="break-all font-medium">{connection.repo_owner}/{connection.repo_name}</p>
      <p className="text-muted-foreground break-all">Branch: {connection.default_branch}</p>
    </div>
  );
}

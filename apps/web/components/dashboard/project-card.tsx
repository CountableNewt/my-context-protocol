"use client";

import Link from "next/link";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { ProjectConnectionSummary } from "./project-connection-summary";
import type { Project } from "@/lib/types";

interface ProjectCardProps {
  project: Project;
  onRequestRename?: () => void;
}

export function ProjectCard({ project, onRequestRename }: ProjectCardProps) {
  return (
    <ContextMenu>
      <ContextMenuTrigger
        title="Right-Click for Actions"
        render={
          <Link
            href={`/projects/${project.id}`}
            className="block h-full min-h-0 rounded-xl outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
          />
        }
      >
        <Card className="@container/project-card h-full cursor-context-menu transition-colors hover:bg-muted/50">
          <CardHeader className="shrink-0 flex flex-col items-start gap-2 pb-2 @sm/project-card:flex-row @sm/project-card:items-center @sm/project-card:justify-between @sm/project-card:gap-3">
            <h3 className="min-w-0 font-semibold">{project.name}</h3>
            <div className="flex shrink-0 flex-wrap items-center gap-1.5">
              <Badge variant="secondary">{project.slug}</Badge>
              {project.mcp_oauth_enabled ? (
                <Badge variant="outline" className="text-xs font-normal">
                  MCP OAuth
                </Badge>
              ) : null}
            </div>
          </CardHeader>
          <CardContent className="flex flex-1 flex-col gap-4">
            <ProjectConnectionSummary project={project} />
            <p className="text-muted-foreground mt-auto font-mono text-xs break-all">
              {project.mcp_url ?? "MCP URL not configured on server"}
            </p>
          </CardContent>
        </Card>
      </ContextMenuTrigger>
      <ContextMenuContent>
        <ContextMenuItem
          onClick={() => {
            onRequestRename?.();
          }}
        >
          Edit Name
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
  );
}

"use client";

import { useState } from "react";
import type { RepositoryMigrationSource } from "@/lib/projects-api";
import { Button } from "@/components/ui/button";
import { repositoryProviderLabel } from "./project-connection-summary";

export function RepositoryMigrationPreparation({ source, sourceHandle, confirmed, onConfirmedChange }: {
  source: RepositoryMigrationSource;
  sourceHandle?: string;
  confirmed: boolean;
  onConfirmedChange: (confirmed: boolean) => void;
}) {
  const [mode, setMode] = useState<"existing" | "copy">("existing");
  const toTangled = source.provider === "github";
  return <div className="mb-6 space-y-4 rounded-md border p-4">
    <p className="break-all text-sm">From {repositoryProviderLabel(source.provider)}: <strong>{sourceHandle || source.repo_owner}/{source.repo_name}</strong> ({source.default_branch})</p>
    <div className="flex flex-wrap gap-2" role="group" aria-label="Migration method">
      <Button type="button" variant={mode === "existing" ? "default" : "outline"} aria-pressed={mode === "existing"} onClick={() => { setMode("existing"); onConfirmedChange(false); }}>Use existing repository</Button>
      <Button type="button" variant={mode === "copy" ? "default" : "outline"} aria-pressed={mode === "copy"} onClick={() => { setMode("copy"); onConfirmedChange(false); }}>Create and copy repository</Button>
    </div>
    {mode === "copy" && <div className="space-y-3 text-sm">
      <ol className="list-decimal space-y-2 pl-5">
        <li>Open the destination importer in a new tab and create a repository with the visibility you want.</li>
        <li>Import this project’s Git history, branches and tags. For private sources, authenticate directly with the provider. Wait for the import to finish.</li>
        <li>Return here, refresh the repository list, and select the imported repository and branch below.</li>
      </ol>
      <a className="inline-block underline underline-offset-4" href={toTangled ? "https://tangled.org/repo/migrate" : "https://github.com/new/import"} target="_blank" rel="noopener noreferrer">Open {toTangled ? "Tangled" : "GitHub"} importer</a>
      <p className="text-muted-foreground">The provider performs the copy. Issues, pull requests and other forge settings need separate migration.</p>
      {!toTangled && <p className="text-muted-foreground">If GitHub Importer cannot authenticate to your Tangled repository, <a className="underline" href="https://github.com/new" target="_blank" rel="noopener noreferrer">create an empty GitHub repository</a> and copy all branches and tags with Git using the clone URLs supplied by each provider. <a className="underline" href="https://docs.github.com/en/repositories/creating-and-managing-repositories/duplicating-a-repository" target="_blank" rel="noopener noreferrer">Git copying instructions</a></p>}
      <details><summary className="cursor-pointer">Copy with Git instead</summary>
        <p className="my-2">Create an empty destination repository first. Replace the two URL placeholders with the authenticated clone URLs shown by your providers, then run these commands locally:</p>
        <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs">{`git clone --bare <source-clone-url> project-migration.git
git -C project-migration.git push <destination-clone-url> 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'`}</pre>
        <p className="mt-2 text-muted-foreground">For Git LFS repositories, transfer the LFS objects too before switching. Keep the source repository until the destination has been verified.</p>
      </details>
    </div>}
    <p className="text-muted-foreground text-sm">Your project URL, API keys and releases stay with this project. Copy the Git history before switching. After connecting an ATProto repository, review and publish the project records to finish migration. This requires separate publication consent.</p>
    <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={confirmed} onChange={(event) => onConfirmedChange(event.target.checked)} />The destination contains my project’s Git history and I want future syncs to use it.</label>
  </div>;
}

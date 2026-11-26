"use client";

import { useState } from "react";
import { ChevronDownIcon } from "lucide-react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getCurrentUser } from "@/lib/auth";
import { fetchTangledRepositories } from "@/lib/atproto-projects-api";
import { connectRepo, type RepositoryMigrationSource } from "@/lib/projects-api";
import { ApiError, formatApiErrorDetail } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function TangledRepositoryForm({ projectId, onConnected, migrateFrom, disabled = false, onPendingChange, onDestinationChange }: { projectId: string; onConnected: () => void; migrateFrom?: RepositoryMigrationSource; disabled?: boolean; onPendingChange?: (pending: boolean) => void; onDestinationChange?: () => void }) {
  const account = useQuery({ queryKey: ["repository-account"], queryFn: getCurrentUser });
  const linked = Boolean(account.data?.atproto_did);
  const repositories = useInfiniteQuery({ queryKey: ["tangled-repositories"], queryFn: ({ pageParam }) => fetchTangledRepositories(pageParam), initialPageParam: undefined as string | undefined, getNextPageParam: (last) => last.cursor, enabled: linked });
  const [owner, setOwner] = useState("");
  const [name, setName] = useState("");
  const [branch, setBranch] = useState("main");
  const [privateRepo, setPrivateRepo] = useState(false);
  const client = useQueryClient();
  const mutation = useMutation({ mutationFn: () => connectRepo(projectId, { provider: "tangled", owner: owner.trim(), repo: name.trim(), branch: branch.trim(), is_private: privateRepo, ...(migrateFrom ? { migrate_from: migrateFrom } : {}) }), onMutate: () => onPendingChange?.(true), onSettled: () => onPendingChange?.(false), onSuccess: () => { void client.invalidateQueries({ queryKey: ["repo-connection", projectId] }); void client.invalidateQueries({ queryKey: ["project", projectId] }); void client.invalidateQueries({ queryKey: ["projects"] }); onConnected(); } });
  if (account.isPending) return <p role="status">Checking ATProto identity…</p>;
  if (account.isError) return <p role="alert">Could not check your identity. <Button onClick={() => account.refetch()}>Retry identity check</Button></p>;
  if (!linked) return <p>Connect an ATProto identity in <a className="underline" href="/account">Account settings</a> before using Tangled.</p>;
  const rows = repositories.data?.pages.flatMap((page) => page.repositories) ?? [];
  return <div className="space-y-4">
    <p className="text-muted-foreground text-sm">Choose a public repository or enter its exact owner DID and name. Private access is verified with your linked account.</p>
    <Label htmlFor="tangled-selection">Public Tangled repositories</Label>
    <div className="relative">
    <select id="tangled-selection" className="h-9 w-full min-w-0 appearance-none rounded-md border border-input bg-background py-2 pl-3 pr-10 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50" value={rows.find((row) => row.owner_did === owner && row.name === name)?.repository_did ?? ""} onChange={(event) => { const row = rows.find((r) => r.repository_did === event.target.value); if (row) { onDestinationChange?.(); setOwner(row.owner_did); setName(row.name); setBranch(row.default_branch || "main"); setPrivateRepo(false); } }}>
      <option value="">Choose a repository…</option>
      {rows.map((row) => <option key={row.repository_did} value={row.repository_did}>{row.owner_handle || row.owner_did}/{row.name}</option>)}
    </select>
    <ChevronDownIcon aria-hidden className="pointer-events-none absolute right-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
    </div>
    {repositories.isPending && <p role="status">Loading Tangled repositories…</p>}
    {repositories.isError && <p role="alert">Could not load public repositories. Exact selection remains available. <Button variant="outline" onClick={() => repositories.refetch()}>Retry repositories</Button></p>}
    <div className="flex flex-wrap items-center gap-2">
    <Button type="button" variant="outline" disabled={repositories.isFetching} onClick={() => repositories.refetch()}>{repositories.isFetching ? "Refreshing…" : "Refresh Tangled repositories"}</Button>
    {repositories.hasNextPage && <Button variant="outline" disabled={repositories.isFetchingNextPage} onClick={() => repositories.fetchNextPage()}>Load more repositories</Button>}
    </div>
    <form className="space-y-4" onSubmit={(event) => { event.preventDefault(); if (!disabled) mutation.mutate(); }}>
      <div className="space-y-2"><Label htmlFor="tangled-owner">Owner DID</Label><Input id="tangled-owner" required pattern="did:.+" value={owner} onChange={(event) => { onDestinationChange?.(); setOwner(event.target.value); }} placeholder="did:plc:…" /></div>
      <div className="space-y-2"><Label htmlFor="tangled-name">Repository name</Label><Input id="tangled-name" required value={name} onChange={(event) => { onDestinationChange?.(); setName(event.target.value); }} /></div>
      <div className="space-y-2"><Label htmlFor="tangled-branch">Branch</Label><Input id="tangled-branch" required value={branch} onChange={(event) => { onDestinationChange?.(); setBranch(event.target.value); }} /></div>
      <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={privateRepo} onChange={(event) => setPrivateRepo(event.target.checked)} />Private repository</label>
      <p className="text-muted-foreground text-xs">Connecting a private repository does not authorize publishing its contents or metadata publicly.</p>
      {mutation.isError && <p role="alert" className="text-destructive text-sm">{mutation.error instanceof ApiError ? formatApiErrorDetail(mutation.error.body) || "Could not connect this repository." : "Could not connect this repository."}</p>}
      <Button disabled={disabled || mutation.isPending || !owner.trim() || !name.trim() || !branch.trim()} type="submit">{mutation.isPending ? "Connecting…" : migrateFrom ? "Migrate to ATProto · Tangled" : "Connect Tangled repository"}</Button>
    </form>
  </div>;
}

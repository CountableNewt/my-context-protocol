"use client";

import { useState } from "react";
import { useAuth } from "@/contexts/auth-context";
import { ATProtoAuth } from "@/components/atproto-auth";
import { linkGitHub } from "@/lib/auth";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Skeleton } from "@/components/ui/skeleton";
import {
  ExternalLinkIcon,
  LogOutIcon,
  UserIcon,
} from "lucide-react";

export default function AccountPage() {
  const { user, isLoading, logout } = useAuth();
  const [linkingGitHub, setLinkingGitHub] = useState(false);
  const [linkError, setLinkError] = useState<string | null>(null);

  async function connectGitHub() {
    if (linkingGitHub) return;
    setLinkingGitHub(true);
    setLinkError(null);
    try { window.location.assign(await linkGitHub()); }
    catch { setLinkError("Could not start GitHub authorization. Please try again."); setLinkingGitHub(false); }
  }

  if (isLoading || !user) {
    return (
      <div className="mx-auto max-w-2xl space-y-6">
        <Skeleton className="h-9 w-48" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }

  const initials = user.login
    ? user.login.slice(0, 2).toUpperCase()
    : user.email
      ? user.email.slice(0, 2).toUpperCase()
      : "?";
  const githubConnected = user.github_connected ?? !user.atproto_did;
  const githubHref = githubConnected && user.login ? `https://github.com/${encodeURIComponent(user.login)}` : null;
  const displayName = user.atproto_handle ?? user.login ?? "Account";

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Account</h1>
        <p className="text-muted-foreground mt-1">
          Your profile, connected identities, and browser session.
        </p>
      </div>

      <Card>
        <CardHeader>
          <div className="flex items-start gap-4">
            <Avatar size="lg" className="size-14">
              {user.avatar_url ? (
                <AvatarImage src={user.avatar_url} alt="" />
              ) : null}
              <AvatarFallback className="text-lg">{initials}</AvatarFallback>
            </Avatar>
            <div className="min-w-0 flex-1 space-y-1">
              <CardTitle className="flex flex-wrap items-center gap-2 text-xl">
                <UserIcon className="text-muted-foreground size-5 shrink-0" />
                <span className="truncate">{displayName}</span>

              </CardTitle>
              <CardDescription>
                {user.email ?? "No public email available"}
              </CardDescription>
              {githubHref && (
                <a
                  href={githubHref}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-primary inline-flex items-center gap-1 text-sm font-medium hover:underline"
                >
                  View GitHub Profile
                  <ExternalLinkIcon className="size-3.5" />
                </a>
              )}
            </div>
          </div>
        </CardHeader>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Connected Accounts</CardTitle>
          <CardDescription>Use GitHub or ATProto to sign in to the same account.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          <div className="space-y-2">
            <h2 className="text-sm font-medium">GitHub</h2>
            {githubConnected ? <p className="text-muted-foreground text-sm">Connected{user.login ? ` as ${user.login}` : ""}</p> : <Button variant="outline" onClick={connectGitHub} disabled={linkingGitHub}>{linkingGitHub ? "Opening authorization…" : "Link GitHub Account"}</Button>}
            {linkError ? <p role="alert" className="text-sm text-destructive">{linkError}</p> : null}
          </div>
          <div className="space-y-2">
            <h2 className="text-sm font-medium">ATProto</h2>
            {user.atproto_did ? <div className="space-y-1 text-sm"><p>{user.atproto_handle ?? "Connected ATProto identity"}</p><p className="text-muted-foreground break-all font-mono text-xs">{user.atproto_did}</p></div> : user.atproto_enabled ? <ATProtoAuth linking enabled returnTo="/account" /> : <p className="text-muted-foreground text-sm">ATProto sign-in is not enabled on this server.</p>}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Session</CardTitle>
          <CardDescription>
            Sign out on this browser. Your projects stay on the server.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Button variant="outline" onClick={() => logout()} className="gap-2">
            <LogOutIcon className="size-4" />
            Sign Out
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}

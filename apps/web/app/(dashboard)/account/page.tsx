"use client";

import { useAuth } from "@/contexts/auth-context";
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
  const githubHref = user.login ? `https://github.com/${user.login}` : null;

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Account</h1>
        <p className="text-muted-foreground mt-1">
          Your profile and session. You sign in with GitHub.
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
                <span className="truncate">{user.login ?? "GitHub User"}</span>

              </CardTitle>
              <CardDescription>
                {user.email ?? "No public email on GitHub"}
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

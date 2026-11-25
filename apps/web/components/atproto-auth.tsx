"use client";

import { useEffect, useState, type FormEvent } from "react";
import { getATProtoLoginUrl, getATProtoStatus, linkATProto } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function ATProtoAuth({ linking = false, enabled, returnTo = "/" }: { linking?: boolean; enabled?: boolean; returnTo?: string }) {
  const [available, setAvailable] = useState(enabled === true);
  const [handle, setHandle] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (enabled !== undefined) return;
    let active = true;
    void getATProtoStatus().then((value) => { if (active) setAvailable(value); });
    return () => { active = false; };
  }, [enabled]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!handle.trim() || pending) return;
    setPending(true);
    setError(null);
    try {
      const target = linking ? await linkATProto(handle, returnTo) : getATProtoLoginUrl(handle, returnTo);
      // Initiation must be a full browser navigation so the backend can establish
      // the browser-bound OAuth state cookie before the authorization redirect.
      window.location.assign(target);
    } catch {
      setError("Could not start ATProto authorization. Please try again.");
      setPending(false);
    }
  }

  if (!(enabled ?? available)) return null;
  return (
    <form onSubmit={submit} className="space-y-3" aria-label={linking ? "Link ATProto account" : "Sign in with ATProto"}>
      <div className="space-y-1.5">
        <Label htmlFor={linking ? "atproto-link-handle" : "atproto-login-handle"}>ATProto handle</Label>
        <Input id={linking ? "atproto-link-handle" : "atproto-login-handle"} name="handle" autoComplete="username" autoCapitalize="none" spellCheck={false} placeholder="you.example.com" value={handle} onChange={(event) => setHandle(event.target.value)} disabled={pending} required maxLength={253} />
      </div>
      {error ? <p role="alert" className="text-sm text-destructive">{error}</p> : null}
      <Button type="submit" variant="outline" className="w-full" disabled={pending || !handle.trim()}>{pending ? "Opening authorization…" : linking ? "Link ATProto Account" : "Sign in with ATProto"}</Button>
      {linking ? <p className="text-muted-foreground text-xs">Authorize your ATProto account to connect it to this account. Linking alone does not publish project data.</p> : null}
    </form>
  );
}

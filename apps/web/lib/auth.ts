import { api, ApiError } from "./api";
import type { User } from "./types";
import { safeReturnPath } from "./safe-redirect";

function authReturnUrl(returnTo: string): string {
  const origin = typeof window !== "undefined" ? window.location.origin : process.env.NEXT_PUBLIC_APP_URL ?? "http://localhost:3000";
  return `${origin}${safeReturnPath(returnTo)}`;
}

export function getATProtoLoginUrl(handle: string, returnTo = "/"): string {
  const base = typeof window !== "undefined" ? "/api" : process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
  const query = new URLSearchParams({ handle: handle.trim(), return_to: authReturnUrl(returnTo) });
  return `${base.replace(/\/$/, "")}/auth/atproto?${query}`;
}

export async function getATProtoStatus(): Promise<boolean> {
  try { return (await api.get<{ enabled: boolean }>("/auth/atproto/status")).enabled === true; } catch { return false; }
}

function checkedAuthorizationUrl(value: string): string {
  const url = new URL(value);
  if (url.protocol !== "https:" || url.username || url.password || url.hash) throw new Error("The authorization server returned an invalid redirect");
  return url.href;
}

export async function linkATProto(handle: string, returnTo = "/account"): Promise<string> {
  const result = await api.post<{ authorization_url: string }>("/auth/atproto/link", { handle: handle.trim(), return_to: authReturnUrl(returnTo) });
  return checkedAuthorizationUrl(result.authorization_url);
}

export async function linkGitHub(returnTo = "/account"): Promise<string> {
  const result = await api.post<{ authorization_url: string }>("/auth/github/link", { return_to: authReturnUrl(returnTo) });
  return checkedAuthorizationUrl(result.authorization_url);
}

export function getGitHubLoginUrl(returnTo = "/", selectAccount = false): string {
  const baseUrl =
    typeof window !== "undefined" ? "/api" : process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
  const appUrl =
    typeof window !== "undefined"
      ? window.location.origin
      : process.env.NEXT_PUBLIC_APP_URL ?? "http://localhost:3000";
  const returnUrl = `${appUrl}${returnTo.startsWith("/") ? returnTo : `/${returnTo}`}`;
  return `${baseUrl.replace(/\/$/, "")}/auth/github?return_to=${encodeURIComponent(returnUrl)}${selectAccount ? "&prompt=select_account" : ""}`;
}

export async function logout(): Promise<void> {
  await api.post("/auth/logout");
}

/** Exchange one-time OAuth handoff token for session. Call when auth_token is in the URL. */
function normalizeUser(u: {
  id: string;
  email?: string | null;
  login?: string | null;
  avatar_url?: string | null;
  plan?: string | null;
  is_admin?: boolean | null;
  internal_pro_bypass?: boolean | null;
  can_manage_subscription?: boolean | null;
  app_env?: string | null;
  non_production_bypasses?: boolean | null;
  github_connected?: boolean | null;
  atproto_enabled?: boolean | null;
  atproto_did?: string | null;
  atproto_handle?: string | null;
}): User {
  const rawEnv = u.app_env?.trim().toLowerCase();
  const app_env =
    rawEnv === "local" || rawEnv === "dev" || rawEnv === "prod" ? rawEnv : undefined;
  return {
    id: u.id,
    email: u.email ?? undefined,
    login: u.login ?? undefined,
    avatar_url: u.avatar_url ?? undefined,
    github_connected: u.github_connected ?? !u.atproto_did,
    atproto_enabled: u.atproto_enabled === true,
    atproto_did: u.atproto_did ?? undefined,
    atproto_handle: u.atproto_handle ?? undefined,
    plan: u.plan === "pro" ? "pro" : "free",
    is_admin: Boolean(u.is_admin),
    internal_pro_bypass: Boolean(u.internal_pro_bypass),
    can_manage_subscription: Boolean(u.can_manage_subscription),
    app_env,
    non_production_bypasses:
      u.non_production_bypasses === undefined || u.non_production_bypasses === null
        ? undefined
        : Boolean(u.non_production_bypasses),
  };
}

export async function confirmAuth(token: string): Promise<User | null> {
  const user = await api.get<User>(`/auth/confirm?token=${encodeURIComponent(token)}`);
  return user ? normalizeUser(user) : null;
}

export async function getCurrentUser(): Promise<User | null> {
  try {
    const user = await api.get<User>("/auth/me");
    return user ? normalizeUser(user) : null;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return null;
    }
    return null;
  }
}

import { buildAuthConfirmRedirect } from "@/lib/auth-token-handoff";
import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

/**
 * Intercepts OAuth callback with auth_token before React mounts.
 * Redirects to /api/auth/confirm once per request, avoiding duplicate
 * client-side effect runs that consume the one-time token.
 *
 * Next.js 16+: middleware.ts was renamed to proxy.ts (same API).
 */
export const config = { matcher: ["/((?!api|_next/static|_next/image|favicon.ico).*)" ] };

export function proxy(request: NextRequest) {
  if (request.nextUrl.pathname.startsWith("/xrpc/") || request.nextUrl.pathname.startsWith("/.well-known/")) {
    return NextResponse.next();
  }
  if (request.nextUrl.pathname === "/auth/mcp-oauth-resume") {
    return NextResponse.next();
  }

  const authToken = request.nextUrl.searchParams.get("auth_token");
  if (authToken) {
    const confirmUrl = buildAuthConfirmRedirect(request.url, authToken);
    return NextResponse.redirect(new URL(confirmUrl, request.url));
  }
  return NextResponse.next();
}

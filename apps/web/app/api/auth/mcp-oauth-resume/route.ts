import { NextRequest, NextResponse } from "next/server";
import { getBackendOrigin } from "@/lib/backend-origin";
import { forwardBackendResponseCookies } from "@/lib/forward-backend-response-cookies";

/**
 * Proxy for the backend's MCP OAuth resume endpoint.
 *
 * After GitHub login completes, the backend redirects the browser to
 * `FRONTEND_URL/auth/mcp-oauth-resume?pending=…&auth_token=…`. Because the
 * Next.js app only exposes the backend under `/api/*`, that path would
 * otherwise 404. A rewrite in next.config.ts maps the bare path to here,
 * and this handler proxies to the Vapor backend with proper redirect + cookie
 * forwarding so the session handshake completes before the browser is sent on
 * to the tenant-host consent page.
 *
 * Flow (two sequential browser navigations):
 *  1. /auth/mcp-oauth-resume?pending=UUID&auth_token=TOKEN
 *     → backend consumes auth_token, sets session, returns 302 to
 *       FRONTEND_URL/auth/mcp-oauth-resume?pending=UUID  (no auth_token)
 *  2. /auth/mcp-oauth-resume?pending=UUID
 *     → backend reads session, issues handoff token, returns 302 to
 *       TENANT_HOST/oauth/consent?pending=UUID&auth_token=HANDOFF
 */
export async function GET(request: NextRequest) {
  const { search } = new URL(request.url);
  const backendUrl = `${getBackendOrigin()}/auth/mcp-oauth-resume${search}`;

  let res: Response;
  try {
    res = await fetch(backendUrl, {
      method: "GET",
      redirect: "manual",
      headers: {
        Cookie: request.headers.get("cookie") || "",
      },
    });
  } catch {
    return NextResponse.redirect(
      new URL("/login?error=auth_service_unavailable", request.url),
      302,
    );
  }

  const location = res.headers.get("location");
  const isRedirect = Boolean(location) && res.status >= 300 && res.status < 400;

  if (isRedirect && location) {
    let target = new URL(location, request.url);
    // The first handshake sets a host-only cookie. Keep its continuation on
    // this deployment URL even when the backend defaults to the branch alias.
    if (request.nextUrl.searchParams.has("auth_token") && target.pathname === "/auth/mcp-oauth-resume") {
      target = new URL(target.pathname + target.search, request.url);
    }
    const response = NextResponse.redirect(target, { status: 302 });
    forwardBackendResponseCookies(res, response, new URL(request.url));
    return response;
  }

  // Unexpected non-redirect response — send user to login with a diagnostic code.
  return NextResponse.redirect(
    new URL(
      `/login?error=mcp_oauth_resume_failed&status=${res.status}`,
      request.url,
    ),
    302,
  );
}

import { NextRequest, NextResponse } from "next/server";
import { getBackendOrigin } from "@/lib/backend-origin";
import { forwardBackendResponseCookies } from "@/lib/forward-backend-response-cookies";
import { assertSafeRelativeRedirectPath } from "@/lib/safe-redirect";

export async function GET(request: NextRequest) {
  const { searchParams } = new URL(request.url);
  const token = searchParams.get("token");
  const redirectTo = searchParams.get("redirect");

  if (!token || redirectTo === "") {
    return NextResponse.redirect(new URL("/login?error=missing_params", request.url), 302);
  }
  try {
    if (redirectTo != null) assertSafeRelativeRedirectPath(redirectTo);
  } catch {
    return NextResponse.redirect(new URL("/login?error=invalid_redirect", request.url), 302);
  }

  const backendUrl = `${getBackendOrigin()}/auth/confirm?token=${encodeURIComponent(token)}${redirectTo == null ? "" : `&redirect=${encodeURIComponent(redirectTo)}`}`;

  const res = await fetch(backendUrl, {
    method: "GET",
    redirect: "manual",
    headers: {
      Cookie: request.headers.get("cookie") || "",
    },
  });

  // Vapor uses 303 See Other for req.redirect(.normal); only checking 302 skipped Set-Cookie forwarding.
  const location = res.headers.get("location");
  const isBackendRedirect =
    Boolean(location) && res.status >= 300 && res.status < 400;

  if (isBackendRedirect && location && redirectTo != null) {
    const requestUrl = new URL(request.url);
    // Use 302 to the browser even when Vapor returns 303 — avoids rare clients mishandling Set-Cookie on 303.
    // Keep host-only cookies on the origin that consumed the handoff. A preview
    // deployment URL and its branch alias are different cookie hosts.
    const response = NextResponse.redirect(new URL(redirectTo, requestUrl), 302);
    forwardBackendResponseCookies(res, response, requestUrl);
    return response;
  }

  if (res.ok) {
    const response = NextResponse.json(await res.json());
    response.headers.set("Cache-Control", "no-store");
    const frontendOrigin = res.headers.get("x-context-frontend-origin");
    if (frontendOrigin) response.headers.set("X-Context-Frontend-Origin", frontendOrigin);
    forwardBackendResponseCookies(res, response, new URL(request.url));
    return response;
  }

  // 401 = token already consumed. First request succeeded; redirect to return URL (or /) instead of auth_failed.
  if (redirectTo == null) {
    return NextResponse.json({ error: "Authentication failed" }, { status: res.status >= 400 ? res.status : 502, headers: { "Cache-Control": "no-store" } });
  }

  if (res.status === 401) {
    try {
      assertSafeRelativeRedirectPath(redirectTo);
      return NextResponse.redirect(new URL(redirectTo, request.url), 302);
    } catch {
      /* invalid */
    }
    return NextResponse.redirect(new URL("/", request.url), 302);
  }

  return NextResponse.redirect(new URL(`/login?error=auth_failed`, request.url), 302);
}

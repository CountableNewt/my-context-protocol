import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";
import { getBackendOrigin } from "@/lib/backend-origin";
import { forwardBackendResponseCookies } from "@/lib/forward-backend-response-cookies";

/** Server-side only: prefer non-public var to avoid client bundle assumptions; fall back to public URL. */
export function getAdminBackendOrigin(): string {
  return getBackendOrigin("control");
}

/**
 * Forwards to Vapor `GET|POST /admin/<adminPath>` with cookies (session).
 * Always bypasses Next fetch caching for the upstream request.
 */
export async function proxyAdminToBackend(
  request: NextRequest,
  adminPath: string,
  method: "GET" | "POST"
): Promise<NextResponse> {
  const base = getAdminBackendOrigin();
  const requestUrl = new URL(request.url);
  const search = requestUrl.search;
  const normalizedPath = adminPath.replace(/^\/+/, "").replace(/\/+$/, "");
  const backendUrl = `${base}/admin/${normalizedPath}${search}`;

  const cookie = request.headers.get("cookie") ?? "";
  const headers = new Headers();
  headers.set("Cookie", cookie);
  const origin = request.headers.get("origin");
  if (origin) headers.set("Origin", origin);
  const referer = request.headers.get("referer");
  if (referer) headers.set("Referer", referer);
  const accept = request.headers.get("accept");
  if (accept) headers.set("Accept", accept);

  let body: string | undefined;
  if (method === "POST") {
    const ct = request.headers.get("content-type");
    if (ct) headers.set("Content-Type", ct);
    const text = await request.text();
    if (text.length > 0) body = text;
  }

  const res = await fetch(backendUrl, {
    method,
    headers,
    body,
    cache: "no-store",
    next: { revalidate: 0 },
  });

  const nextRes = new NextResponse(await res.arrayBuffer(), {
    status: res.status,
    headers: {
      "content-type": res.headers.get("content-type") ?? "application/json",
    },
  });
  forwardBackendResponseCookies(res, nextRes, requestUrl);
  return nextRes;
}

import { NextRequest, NextResponse } from "next/server";
import { getBackendOrigin, type BackendService } from "@/lib/backend-origin";
import { forwardBackendResponseCookies } from "@/lib/forward-backend-response-cookies";

const services: Record<string, BackendService> = {
  auth: "identity",
  github: "identity",
  projects: "control",
  repositories: "control",
  dashboard: "control",
  admin: "control",
  health: "control",
  webhooks: "sync",
};

/** Bindings exist at runtime, so a build-time Next rewrite cannot route them. */
async function proxy(request: NextRequest, context: { params: Promise<{ path: string[] }> }) {
  if (process.env.VERCEL_SERVICE_MODE !== "1") {
    return NextResponse.json({ error: "Not found" }, { status: 404 });
  }
  const { path } = await context.params;
  const service = services[path[0]];
  if (!service || path.some((part) => !part || /[\\/?#]/.test(part) || part === "." || part === "..")) {
    return NextResponse.json({ error: "Not found" }, { status: 404 });
  }

  try {
    const url = new URL(`${getBackendOrigin(service)}/${path.map(encodeURIComponent).join("/")}`);
    url.search = request.nextUrl.search;
    const headers = new Headers();
    for (const name of ["accept", "authorization", "content-type", "cookie", "origin", "referer", "x-github-event", "x-github-delivery", "x-hub-signature-256"]) {
      const value = request.headers.get(name);
      if (value) headers.set(name, value);
    }
    // This is request metadata for redirect resolution, never authentication.
    // Stamp it from the app request; an incoming caller value is never forwarded.
    headers.set("x-context-frontend-origin", new URL(request.url).origin);
    const upstream = await fetch(url, {
      method: request.method,
      headers,
      body: request.method === "GET" || request.method === "HEAD" ? undefined : await request.arrayBuffer(),
      redirect: "manual",
      cache: "no-store",
      signal: request.signal,
    });
    const responseHeaders = new Headers({ "cache-control": "private, no-store" });
    for (const name of ["content-type", "location", "www-authenticate", "retry-after"]) {
      const value = upstream.headers.get(name);
      if (value) responseHeaders.set(name, value);
    }
    const response = new NextResponse(upstream.body, {
      status: upstream.status,
      headers: responseHeaders,
    });
    forwardBackendResponseCookies(upstream, response, new URL(request.url));
    return response;
  } catch {
    return NextResponse.json({ error: "Service unavailable" }, { status: 503 });
  }
}

export const GET = proxy;
export const HEAD = proxy;
export const POST = proxy;
export const PATCH = proxy;
export const PUT = proxy;
export const DELETE = proxy;

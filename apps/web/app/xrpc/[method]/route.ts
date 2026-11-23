import { NextRequest, NextResponse } from "next/server";
import { XRPC_SERVICES } from "@mycontext/web-client";
import { getBackendOrigin, type BackendService } from "@/lib/backend-origin";

/** Static generated dispatch; callers cannot supply a destination URL. */
async function proxy(request: NextRequest, context: { params: Promise<{ method: string }> }) {
  const { method } = await context.params;
  if (!Object.prototype.hasOwnProperty.call(XRPC_SERVICES, method)) {
    return NextResponse.json({ error: "MethodNotImplemented", message: "Unknown XRPC method" }, { status: 404 });
  }
  const owner = XRPC_SERVICES[method as keyof typeof XRPC_SERVICES];
  const service: BackendService = owner === "githubapp" ? "identity" : owner;
  try {
    const target = new URL(`/xrpc/${encodeURIComponent(method)}`, `${getBackendOrigin(service)}/`);
    target.search = request.nextUrl.search;
    const headers = new Headers();
    for (const name of ["authorization", "dpop", "accept", "content-type"]) {
      const value = request.headers.get(name);
      if (value) headers.set(name, value);
    }
    // Stamp from the actual app request, never a caller-supplied forwarding header.
    headers.set("x-forwarded-host", new URL(request.url).host);
    const upstream = await fetch(target, {
      method: request.method,
      headers,
      body: request.method === "GET" || request.method === "HEAD" ? undefined : await request.arrayBuffer(),
      redirect: "manual",
      credentials: "omit",
      cache: "no-store",
      signal: request.signal,
    });
    if (upstream.status >= 300 && upstream.status < 400) {
      return NextResponse.json({ error: "ServiceUnavailable", message: "Unexpected XRPC redirect" }, { status: 502 });
    }
    const responseHeaders = new Headers({ "cache-control": "private, no-store" });
    for (const name of ["content-type", "www-authenticate", "retry-after", "dpop-nonce"]) {
      const value = upstream.headers.get(name);
      if (value) responseHeaders.set(name, value);
    }
    return new NextResponse(upstream.body, { status: upstream.status, headers: responseHeaders });
  } catch {
    return NextResponse.json({ error: "ServiceUnavailable", message: "XRPC service is unavailable" }, { status: 503 });
  }
}

export const GET = proxy;
export const POST = proxy;

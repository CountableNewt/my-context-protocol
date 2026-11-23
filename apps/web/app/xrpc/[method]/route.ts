import { NextRequest, NextResponse } from "next/server";
import { XRPC_SERVICES } from "@mycontext/web-client";
import { getBackendOrigin, type BackendService } from "@/lib/backend-origin";

const corsHeaders = {
  "access-control-allow-origin": "*",
  "access-control-allow-methods": "GET, POST, OPTIONS",
  "access-control-allow-headers": "authorization, dpop, content-type",
  "access-control-expose-headers": "www-authenticate, retry-after, dpop-nonce, x-myctx-telemetry-publication",
};

export function OPTIONS() {
  return new NextResponse(null, { status: 204, headers: corsHeaders });
}

async function boundedBody(request: NextRequest): Promise<ArrayBuffer | undefined> {
  if (!request.body) return undefined;
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > 2 * 1024 * 1024) { await reader.cancel(); throw new RangeError("XRPC body is too large"); }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const body = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { body.set(chunk, offset); offset += chunk.byteLength; }
  return body.buffer;
}

/** Static generated dispatch; callers cannot supply a destination URL. */
async function proxy(request: NextRequest, context: { params: Promise<{ method: string }> }) {
  const { method } = await context.params;
  if (!Object.prototype.hasOwnProperty.call(XRPC_SERVICES, method)) {
    return NextResponse.json({ error: "MethodNotImplemented", message: "Unknown XRPC method" }, { status: 404, headers: corsHeaders });
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
      body: request.method === "GET" || request.method === "HEAD" ? undefined : await boundedBody(request),
      redirect: "manual",
      credentials: "omit",
      cache: "no-store",
      signal: request.signal,
    });
    if (upstream.status >= 300 && upstream.status < 400) {
      return NextResponse.json({ error: "ServiceUnavailable", message: "Unexpected XRPC redirect" }, { status: 502, headers: corsHeaders });
    }
    const responseHeaders = new Headers({ ...corsHeaders, "cache-control": "private, no-store" });
    for (const name of ["content-type", "www-authenticate", "retry-after", "dpop-nonce", "x-myctx-telemetry-publication"]) {
      const value = upstream.headers.get(name);
      if (value) responseHeaders.set(name, value);
    }
    return new NextResponse(upstream.body, { status: upstream.status, headers: responseHeaders });
  } catch (error) {
    if (error instanceof RangeError) return NextResponse.json({ error: "InvalidRequest", message: "XRPC body is too large" }, { status: 413, headers: corsHeaders });
    return NextResponse.json({ error: "ServiceUnavailable", message: "XRPC service is unavailable" }, { status: 503, headers: corsHeaders });
  }
}

export const GET = proxy;
export const POST = proxy;

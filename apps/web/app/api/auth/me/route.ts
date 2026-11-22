import { NextRequest, NextResponse } from "next/server";
import { getBackendOrigin } from "@/lib/backend-origin";

/**
 * Proxy /api/auth/me to backend, forwarding the session cookie.
 * Ensures the cookie set by /api/auth/confirm is sent to the backend.
 */
export async function GET(request: NextRequest) {
  const backendUrl = `${getBackendOrigin()}/auth/me`;
  const cookie = request.headers.get("cookie") || "";

  let res: Response;
  try {
    res = await fetch(backendUrl, {
      method: "GET",
      headers: { Cookie: cookie },
      cache: "no-store",
    });
  } catch {
    return NextResponse.json(
      { error: "Auth service unreachable" },
      { status: 503 }
    );
  }

  if (!res.ok) {
    return NextResponse.json(
      { error: res.statusText },
      { status: res.status }
    );
  }

  const data = await res.json();
  return NextResponse.json(data, { headers: { "cache-control": "private, no-store" } });
}

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";

function request(url: string, cookie?: string) {
  const headers = cookie ? new Headers({ cookie }) : new Headers();
  return new NextRequest(url, { headers });
}

describe("GET /api/auth/mcp-oauth-resume", () => {
  const fetchMock = vi.fn<typeof fetch>();

  beforeEach(() => {
    vi.resetModules();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  async function loadRoute() {
    return import("./route");
  }

  it("proxies pending and auth_token to backend", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://api.test");
    const { GET } = await loadRoute();

    const backendHeaders = new Headers();
    backendHeaders.set("location", "/auth/mcp-oauth-resume?pending=pid");

    fetchMock.mockResolvedValueOnce({
      status: 302,
      ok: false,
      headers: backendHeaders,
    } as Response);

    const res = await GET(
      request(
        "https://app.test/api/auth/mcp-oauth-resume?pending=pid&auth_token=tok",
        "sid=existing"
      )
    );

    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.test/auth/mcp-oauth-resume?pending=pid&auth_token=tok",
      expect.objectContaining({
        method: "GET",
        redirect: "manual",
        headers: { Cookie: "sid=existing" },
      })
    );
    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toBe(
      "https://app.test/auth/mcp-oauth-resume?pending=pid"
    );
  });

  it("forwards backend cookies during resume redirect", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://api.test");
    const { GET } = await loadRoute();

    const backendHeaders = new Headers();
    backendHeaders.set("location", "/auth/mcp-oauth-resume?pending=pid");
    backendHeaders.set(
      "set-cookie",
      "sid=xyz; Path=/; Domain=.api.test; Secure; HttpOnly"
    );

    fetchMock.mockResolvedValueOnce({
      status: 303,
      ok: false,
      headers: backendHeaders,
    } as Response);

    const res = await GET(
      request("https://app.test/api/auth/mcp-oauth-resume?pending=pid&auth_token=tok")
    );

    const setCookie = res.headers.getSetCookie();
    expect(setCookie.length).toBeGreaterThan(0);
    expect(setCookie[0]).toContain("sid=xyz");
    expect(setCookie[0].toLowerCase()).not.toContain("domain=");
  });

  it("preserves absolute tenant consent redirects", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://api.test");
    const { GET } = await loadRoute();

    const tenantConsent =
      "https://p2zttjqd2g79.mcp.testing.mycontextprotocol.dev/oauth/consent?pending=pid&auth_token=handoff";
    const backendHeaders = new Headers();
    backendHeaders.set("location", tenantConsent);

    fetchMock.mockResolvedValueOnce({
      status: 302,
      ok: false,
      headers: backendHeaders,
    } as Response);

    const res = await GET(
      request("https://app.test/api/auth/mcp-oauth-resume?pending=pid", "sid=xyz")
    );

    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toBe(tenantConsent);
  });

  it("redirects to login when backend is unavailable", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://api.test");
    const { GET } = await loadRoute();

    fetchMock.mockRejectedValueOnce(new Error("network"));

    const res = await GET(
      request("https://app.test/api/auth/mcp-oauth-resume?pending=pid")
    );

    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toContain("auth_service_unavailable");
  });
  it("keeps the first resume handshake on its deployment host", async () => {
    vi.stubEnv("IDENTITY_SERVICE_URL", "https://identity.internal");
    const { GET } = await loadRoute();
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 303, headers: {
      location: "https://branch.vercel.app/auth/mcp-oauth-resume?pending=pid",
      "set-cookie": "vapor-session=opaque; Path=/; HttpOnly; Secure",
    } }));
    const res = await GET(request("https://deployment.vercel.app/api/auth/mcp-oauth-resume?pending=pid&auth_token=tok"));
    expect(res.headers.get("location")).toBe("https://deployment.vercel.app/auth/mcp-oauth-resume?pending=pid");
    expect(res.headers.get("set-cookie")).toContain("vapor-session=opaque");
  });

});

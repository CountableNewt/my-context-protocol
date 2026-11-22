import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";

function request(url: string, cookie?: string) {
  const headers = cookie ? new Headers({ cookie }) : new Headers();
  return new NextRequest(url, { headers });
}

describe("GET /api/auth/confirm", () => {
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

  it("redirects to login when token is missing", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await loadRoute();
    const res = await GET(request("http://app.test/api/auth/confirm?redirect=%2F"));
    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toContain("missing_params");
  });

  it("redirects to login when redirect param is missing", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await loadRoute();
    const res = await GET(request("http://app.test/api/auth/confirm?token=abc"));
    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toContain("missing_params");
  });

  it("redirects to login for unsafe redirect paths", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await loadRoute();
    const res = await GET(
      request("http://app.test/api/auth/confirm?token=t&redirect=https%3A%2F%2Fevil.com")
    );
    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toContain("invalid_redirect");
  });

  it("proxies backend redirect and forwards Set-Cookie (single header)", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await loadRoute();

    const backendHeaders = new Headers();
    backendHeaders.set("location", "http://app.test/dashboard");
    backendHeaders.set(
      "set-cookie",
      "sid=xyz; Path=/; Domain=.api.test; Secure; HttpOnly"
    );

    fetchMock.mockResolvedValueOnce({
      status: 302,
      ok: false,
      headers: backendHeaders,
    } as Response);

    const res = await GET(
      request(
        "http://app.test/api/auth/confirm?token=tok&redirect=%2Fdashboard",
        "existing=1"
      )
    );

    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toBe("http://app.test/dashboard");
    const setCookie = res.headers.getSetCookie();
    expect(setCookie.length).toBeGreaterThan(0);
    expect(setCookie[0]).toContain("sid=xyz");
    expect(setCookie[0].toLowerCase()).not.toContain("domain=");
    expect(setCookie[0]).toContain("SameSite=Lax");

    expect(fetchMock).toHaveBeenCalledWith(
      "http://api.test/auth/confirm?token=tok&redirect=%2Fdashboard",
      expect.objectContaining({
        method: "GET",
        redirect: "manual",
        headers: { Cookie: "existing=1" },
      })
    );
  });

  it("strips Secure from Set-Cookie on http URLs", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await loadRoute();

    const backendHeaders = new Headers();
    backendHeaders.set("location", "http://app.test/");
    backendHeaders.set("set-cookie", "a=1; Path=/; Secure");

    fetchMock.mockResolvedValueOnce({
      status: 303,
      ok: false,
      headers: backendHeaders,
    } as Response);

    const res = await GET(
      request("http://app.test/api/auth/confirm?token=t&redirect=%2F")
    );
    const c = res.headers.getSetCookie()[0];
    expect(c.toLowerCase()).not.toMatch(/;\s*secure\b/);
  });

  it("keeps a deployment-link login on that host even when backend uses a branch alias", async () => {
    vi.stubEnv("IDENTITY_SERVICE_URL", "https://identity.internal");
    const { GET } = await loadRoute();
    fetchMock.mockResolvedValueOnce(new Response(null, {
      status: 303,
      headers: {
        location: "https://branch-preview.vercel.app/projects",
        "set-cookie": "vapor-session=opaque; Domain=branch-preview.vercel.app; Path=/; Secure; HttpOnly",
      },
    }));
    const res = await GET(request("https://deployment-preview.vercel.app/api/auth/confirm?token=t&redirect=%2Fprojects"));
    expect(res.headers.get("location")).toBe("https://deployment-preview.vercel.app/projects");
    expect(res.headers.get("set-cookie")).toContain("vapor-session=opaque");
    expect(res.headers.get("set-cookie")).not.toContain("Domain=");
  });

  it("returns JSON when backend responds 200 with body", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await loadRoute();

    fetchMock.mockResolvedValueOnce({
      status: 200,
      ok: true,
      headers: new Headers({ "content-type": "application/json" }),
      json: () => Promise.resolve({ user: { id: "1" } }),
    } as Response);

    const res = await GET(
      request("http://app.test/api/auth/confirm?token=t&redirect=%2F")
    );
    expect(res.status).toBe(200);
    await expect(res.json()).resolves.toEqual({ user: { id: "1" } });
  });

  it("on 401 redirects to redirect param when still safe", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await loadRoute();

    fetchMock.mockResolvedValueOnce({
      status: 401,
      ok: false,
      headers: new Headers(),
    } as Response);

    const res = await GET(
      request("http://app.test/api/auth/confirm?token=t&redirect=%2Fsettings")
    );
    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toBe("http://app.test/settings");
  });

  it("on other errors redirects to auth_failed", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await loadRoute();

    fetchMock.mockResolvedValueOnce({
      status: 500,
      ok: false,
      headers: new Headers(),
    } as Response);

    const res = await GET(
      request("http://app.test/api/auth/confirm?token=expired&redirect=%2F")
    );
    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toContain("auth_failed");
  });
});

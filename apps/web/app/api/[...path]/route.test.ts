import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";
import { GET, POST } from "./route";

const context = (...path: string[]) => ({ params: Promise.resolve({ path }) });
const fetchMock = vi.fn<typeof fetch>();

beforeEach(() => {
  vi.stubEnv("VERCEL_SERVICE_MODE", "1");
  vi.stubEnv("CONTROL_SERVICE_URL", "https://control.internal");
  vi.stubEnv("IDENTITY_SERVICE_URL", "https://identity.internal");
  vi.stubEnv("SYNC_SERVICE_URL", "https://sync.internal");
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
});
afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
});

describe("deployment-bound API routing", () => {
  it("routes projects and query parameters to control with uncached cookie forwarding", async () => {
    fetchMock.mockResolvedValue(new Response("[]", { headers: { "content-type": "application/json" } }));
    const response = await GET(new NextRequest("https://preview.example/api/projects?limit=2", {
      headers: { cookie: "session=opaque", host: "attacker.example", "x-forwarded-host": "attacker.example" },
    }), context("projects"));
    const [url, options] = fetchMock.mock.calls[0];
    expect(String(url)).toBe("https://control.internal/projects?limit=2");
    expect(options?.cache).toBe("no-store");
    expect(new Headers(options?.headers).get("cookie")).toBe("session=opaque");
    expect(new Headers(options?.headers).has("host")).toBe(false);
    expect(new Headers(options?.headers).has("x-forwarded-host")).toBe(false);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });

  it("preserves raw webhook bytes and signature for sync verification", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    const payload = '{ "event": "push" }\n';
    const response = await POST(new NextRequest("https://preview.example/api/webhooks/github", {
      method: "POST", body: payload,
      headers: { "x-hub-signature-256": "sha256=raw-signature", "x-github-delivery": "delivery-id" },
    }), context("webhooks", "github"));
    const [url, options] = fetchMock.mock.calls[0];
    expect(String(url)).toBe("https://sync.internal/webhooks/github");
    expect(new TextDecoder().decode(options?.body as ArrayBuffer)).toBe(payload);
    expect(new Headers(options?.headers).get("x-hub-signature-256")).toBe("sha256=raw-signature");
    expect(response.status).toBe(204);
  });

  it("keeps auth redirects manual and cookies scoped to the preview host", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 302, headers: {
      location: "https://github.com/login/oauth/authorize",
      "set-cookie": "state=opaque; Domain=production.example; Path=/; HttpOnly; Secure",
    } }));
    const response = await GET(new NextRequest("https://preview.example/api/auth/github"), context("auth", "github"));
    expect(String(fetchMock.mock.calls[0][0])).toBe("https://identity.internal/auth/github");
    expect(fetchMock.mock.calls[0][1]?.redirect).toBe("manual");
    expect(response.status).toBe(302);
    expect(response.headers.get("location")).toBe("https://github.com/login/oauth/authorize");
    expect(response.headers.get("set-cookie")).not.toContain("Domain=");
    expect(response.headers.get("set-cookie")).toContain("Secure");
  });

  it.each([["billing", "checkout-session"], ["internal", "secret"], ["projects", ".."], ["projects", "x/y"]])(
    "does not expose forbidden route %s/%s", async (...path) => {
      expect((await GET(new NextRequest("https://preview.example/api/ignored"), context(...path))).status).toBe(404);
      expect(fetchMock).not.toHaveBeenCalled();
    }
  );

  it("does not silently call production when binding is missing", async () => {
    vi.stubEnv("CONTROL_SERVICE_URL", "");
    vi.stubEnv("BACKEND_URL", "https://production.example");
    expect((await GET(new NextRequest("https://preview.example/api/projects"), context("projects"))).status).toBe(503);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("leaves legacy deployments on their existing rewrites", async () => {
    vi.stubEnv("VERCEL_SERVICE_MODE", "");
    expect((await GET(new NextRequest("https://legacy.example/api/projects"), context("projects"))).status).toBe(404);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

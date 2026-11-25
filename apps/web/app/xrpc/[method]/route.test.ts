import { afterEach, describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";
import { GET, POST, OPTIONS } from "./route";

afterEach(() => { vi.unstubAllGlobals(); vi.unstubAllEnvs(); });

describe("XRPC gateway", () => {
  it("routes only generated methods and never forwards browser credentials or injected hosts", async () => {
    vi.stubEnv("CONTROL_SERVICE_URL", "https://control.internal");
    const fetcher = vi.fn().mockResolvedValue(new Response('{"projects":[]}', { status: 200, headers: { "content-type": "application/json", "x-myctx-telemetry-publication": "pending" } }));
    vi.stubGlobal("fetch", fetcher);
    const request = new NextRequest("https://app.example/xrpc/dev.mycontextprotocol.project.listProjects", { headers: { authorization: "Bearer signed", cookie: "secret", "x-forwarded-host": "attacker.example" } });
    const response = await GET(request, { params: Promise.resolve({ method: "dev.mycontextprotocol.project.listProjects" }) });
    expect(response.status).toBe(200);
    expect(response.headers.get("x-myctx-telemetry-publication")).toBe("pending");
    expect(response.headers.get("access-control-expose-headers")).toContain("x-myctx-telemetry-publication");
    const [url, init] = fetcher.mock.calls[0];
    expect(String(url)).toBe("https://control.internal/xrpc/dev.mycontextprotocol.project.listProjects");
    expect(init.headers.get("authorization")).toBe("Bearer signed");
    expect(init.headers.get("cookie")).toBeNull();
    expect(init.headers.get("x-forwarded-host")).toBe("app.example");
    expect(init.redirect).toBe("manual");
    const unknown = await GET(request, { params: Promise.resolve({ method: "https://attacker.example/" }) });
    expect(unknown.status).toBe(404);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("fails closed for missing branch binding and upstream redirects", async () => {
    vi.stubEnv("VERCEL_SERVICE_MODE", "1");
    vi.stubEnv("CONTROL_SERVICE_URL", "");
    vi.stubEnv("BACKEND_URL", "https://production.example");
    const fetcher = vi.fn(); vi.stubGlobal("fetch", fetcher);
    const request = new NextRequest("https://preview.example/xrpc/dev.mycontextprotocol.project.createProject", { method: "POST", body: "{}" });
    expect((await POST(request, { params: Promise.resolve({ method: "dev.mycontextprotocol.project.createProject" }) })).status).toBe(503);
    expect(fetcher).not.toHaveBeenCalled();
    vi.stubEnv("CONTROL_SERVICE_URL", "https://control.internal");
    fetcher.mockResolvedValue(new Response(null, { status: 302, headers: { location: "https://attacker.example" } }));
    expect((await GET(new NextRequest("https://preview.example/xrpc/dev.mycontextprotocol.project.listProjects"), { params: Promise.resolve({ method: "dev.mycontextprotocol.project.listProjects" }) })).status).toBe(502);
  });
});

it("allows scoped browser clients to preflight without cookies and bounds procedure bodies", async () => {
  expect(OPTIONS().headers.get("access-control-allow-origin")).toBe("*");
  expect(OPTIONS().headers.get("access-control-allow-credentials")).toBeNull();
  vi.stubEnv("CONTROL_SERVICE_URL", "https://control.internal");
  const fetcher = vi.fn(); vi.stubGlobal("fetch", fetcher);
  const response = await POST(new NextRequest("https://app.example/xrpc/dev.mycontextprotocol.project.createProject", { method: "POST", body: "x".repeat(2 * 1024 * 1024 + 1) }), { params: Promise.resolve({ method: "dev.mycontextprotocol.project.createProject" }) });
  expect(response.status).toBe(413);
  expect(fetcher).not.toHaveBeenCalled();
});

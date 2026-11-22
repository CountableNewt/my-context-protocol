import { stubNextPublicApiUrlUnset } from "@/lib/testing/stub-public-env";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";

describe("GET /api/auth/me", () => {
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

  it("returns user JSON when backend ok", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await import("./route");
    fetchMock.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ id: "u1", plan: "free" }),
    } as Response);

    const res = await GET(
      new NextRequest("http://app.test/api/auth/me", {
        headers: { cookie: "session=abc" },
      })
    );

    expect(res.status).toBe(200);
    await expect(res.json()).resolves.toEqual({ id: "u1", plan: "free" });
    expect(fetchMock).toHaveBeenCalledWith("http://api.test/auth/me", {
      method: "GET",
      headers: { Cookie: "session=abc" },
      cache: "no-store",
    });
  });

  it("forwards non-ok status from backend", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://api.test");
    const { GET } = await import("./route");
    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 401,
      statusText: "Unauthorized",
    } as Response);

    const res = await GET(new NextRequest("http://app.test/api/auth/me"));
    expect(res.status).toBe(401);
    await expect(res.json()).resolves.toEqual({ error: "Unauthorized" });
  });

  it("defaults to localhost:8080 when NEXT_PUBLIC_API_URL is unset", async () => {
    stubNextPublicApiUrlUnset();
    const { GET } = await import("./route");
    fetchMock.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({}),
    } as Response);
    await GET(new NextRequest("http://app.test/api/auth/me"));
    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/auth/me",
      expect.anything()
    );
  });
});

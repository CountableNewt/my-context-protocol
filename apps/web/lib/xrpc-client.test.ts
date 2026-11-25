import { describe, expect, it } from "vitest";
import { XRPCClient, XRPCError } from "@mycontext/web-client";

describe("generated XRPC client transport", () => {
  it("keeps proof-bound requests on the configured service and omits browser sessions", async () => {
    let request: Request | undefined;
    const client = new XRPCClient("https://api.example.com", { async fetch(value) { request = value; return Response.json({ projects: [] }); } });
    expect(await client.call("dev.mycontextprotocol.project.listProjects", {})).toEqual({ projects: [] });
    expect(request?.url).toBe("https://api.example.com/xrpc/dev.mycontextprotocol.project.listProjects");
    expect(request?.method).toBe("GET");
    expect(request?.credentials).toBe("omit");
    expect(request?.redirect).toBe("error");
    expect(request?.cache).toBe("no-store");
  });

  it("encodes query fields and procedures according to generated contracts", async () => {
    const requests: Request[] = [];
    const client = new XRPCClient("https://api.example.com", { async fetch(value) { requests.push(value); return Response.json({ id: "result" }); } });
    await client.call("dev.mycontextprotocol.project.getProject", { project_id: "id?not-a-query=1" });
    expect(new URL(requests[0].url).searchParams.get("project_id")).toBe("id?not-a-query=1");
    await client.call("dev.mycontextprotocol.project.createProject", { name: "Project", slug: "project" });
    expect(requests[1].method).toBe("POST");
    expect(await requests[1].json()).toEqual({ name: "Project", slug: "project" });
  });

  it("preserves public error codes and rate limit timing", async () => {
    const client = new XRPCClient("https://api.example.com", { async fetch() { return Response.json({ error: "RateLimitExceeded", message: "Try later" }, { status: 429, headers: { "retry-after": "30" } }); } });
    await expect(client.call("dev.mycontextprotocol.project.listProjects", {})).rejects.toMatchObject({ status: 429, code: "RateLimitExceeded", retryAfterSeconds: 30 });
  });

  it("rejects insecure origins and missing required responses", async () => {
    const transport = { async fetch() { return new Response(null, { status: 204 }); } };
    for (const origin of ["http://api.example.com", "https://user:secret@api.example.com", "https://api.example.com/path", "https://api.example.com?token=secret"]) expect(() => new XRPCClient(origin, transport)).toThrow();
    const client = new XRPCClient("https://api.example.com", transport);
    await expect(client.call("dev.mycontextprotocol.project.listProjects", {})).rejects.toBeInstanceOf(XRPCError);
    expect(await client.call("dev.mycontextprotocol.account.logout", { session_id: "id" })).toBeUndefined();
  });

  it("bounds response bodies before parsing", async () => {
    const client = new XRPCClient("https://api.example.com", { async fetch() { return new Response("x".repeat(4 * 1024 * 1024 + 1)); } });
    await expect(client.call("dev.mycontextprotocol.project.listProjects", {})).rejects.toThrow("size limit");
  });
});

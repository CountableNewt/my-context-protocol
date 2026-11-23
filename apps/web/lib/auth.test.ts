import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}));

vi.mock("./api", () => ({
  api: { get, post },
  ApiError: class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  },
}));

import { stubNextPublicAuthDefaultsUnset } from "@/lib/testing/stub-public-env";
import { ApiError } from "./api";
import { confirmAuth, getCurrentUser, getGitHubLoginUrl, getATProtoLoginUrl, getATProtoStatus, linkATProto, linkGitHub, logout } from "./auth";

describe("getGitHubLoginUrl (server / no window)", () => {
  const originalWindow = globalThis.window;

  beforeEach(() => {
    // @ts-expect-error server path
    delete globalThis.window;
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://api.example.com");
    vi.stubEnv("NEXT_PUBLIC_APP_URL", "https://app.example.com");
  });

  afterEach(() => {
    globalThis.window = originalWindow;
    vi.unstubAllEnvs();
  });

  it("builds URL with env and encodes return_to", () => {
    const u = getGitHubLoginUrl("/projects");
    expect(u.startsWith("https://api.example.com/auth/github?return_to=")).toBe(true);
    expect(u).toContain(encodeURIComponent("https://app.example.com/projects"));
  });

  it("prefixes return path when missing slash", () => {
    const u = getGitHubLoginUrl("x");
    expect(u).toContain(encodeURIComponent("https://app.example.com/x"));
  });

  it("defaults to localhost when env vars are absent", () => {
    stubNextPublicAuthDefaultsUnset();
    const u = getGitHubLoginUrl("/");
    expect(u.startsWith("http://localhost:8080/auth/github?return_to=")).toBe(true);
    expect(u).toContain(encodeURIComponent("http://localhost:3000/"));
  });
});

describe("getGitHubLoginUrl (browser)", () => {
  const originalWindow = globalThis.window;

  beforeEach(() => {
    vi.stubGlobal(
      "window",
      { location: { origin: "https://app.example.com" } } as unknown as Window
    );
  });

  afterEach(() => {
    globalThis.window = originalWindow;
    vi.unstubAllGlobals();
  });

  it("uses /api base and window origin", () => {
    const u = getGitHubLoginUrl("/settings");
    expect(u.startsWith("/api/auth/github?return_to=")).toBe(true);
    expect(u).toContain(encodeURIComponent("https://app.example.com/settings"));
  });

  it("requests an account picker only when explicitly selected", () => {
    expect(getGitHubLoginUrl("/account", true)).toContain("&prompt=select_account");
    expect(getGitHubLoginUrl("/account")).not.toContain("prompt=");
  });

  it("builds ATProto browser initiation with an encoded handle and safe local return path", () => {
    const value = new URL(getATProtoLoginUrl(" samclemente.me ", "/projects?view=all"), "https://app.example.com");
    expect(value.pathname).toBe("/api/auth/atproto");
    expect(value.searchParams.get("handle")).toBe("samclemente.me");
    expect(value.searchParams.get("return_to")).toBe("https://app.example.com/projects?view=all");
    const unsafe = new URL(getATProtoLoginUrl("samclemente.me", "//evil.example/path"), "https://app.example.com");
    expect(unsafe.searchParams.get("return_to")).toBe("https://app.example.com/");
  });
});

describe("auth api wrappers", () => {
  afterEach(() => {
    get.mockReset();
    post.mockReset();
  });

  it("logout posts", async () => {
    post.mockResolvedValueOnce(undefined);
    await logout();
    expect(post).toHaveBeenCalledWith("/auth/logout");
  });

  it("preserves separate ATProto and GitHub account state", async () => {
    get.mockResolvedValueOnce({ id: "native", login: "samclemente.me", atproto_did: "did:plc:owner", atproto_handle: "samclemente.me", atproto_enabled: true, github_connected: false });
    const user = await getCurrentUser();
    expect(user).toMatchObject({ github_connected: false, atproto_enabled: true, atproto_did: "did:plc:owner", atproto_handle: "samclemente.me" });
  });

  it("hides ATProto entry when configuration lookup fails", async () => {
    get.mockRejectedValueOnce(new Error("unavailable"));
    expect(await getATProtoStatus()).toBe(false);
    get.mockResolvedValueOnce({ enabled: true });
    expect(await getATProtoStatus()).toBe(true);
  });

  it("links identities through authenticated POST operations", async () => {
    post.mockResolvedValueOnce({ authorization_url: "https://auth.example/authorize?request_uri=one-use" });
    expect(await linkATProto(" samclemente.me ")).toContain("https://auth.example/authorize");
    expect(post).toHaveBeenLastCalledWith("/auth/atproto/link", expect.objectContaining({ handle: "samclemente.me" }));
    post.mockResolvedValueOnce({ authorization_url: "https://github.com/login/oauth/authorize?state=one-use" });
    expect(await linkGitHub()).toContain("https://github.com/login/oauth/authorize");
    expect(post).toHaveBeenLastCalledWith("/auth/github/link", expect.objectContaining({ return_to: expect.stringContaining("/account") }));
  });

  it.each(["javascript:alert(1)", "http://auth.example/authorize", "https://user:secret@auth.example/authorize", "https://auth.example/authorize#secret"])("rejects unsafe authorization redirect %s", async (authorization_url) => {
    post.mockResolvedValueOnce({ authorization_url });
    await expect(linkATProto("samclemente.me")).rejects.toThrow();
  });

  it("confirmAuth normalizes user", async () => {
    get.mockResolvedValueOnce({
      id: "1",
      plan: "pro",
      app_env: " DEV ",
      non_production_bypasses: true,
    });
    const u = await confirmAuth("tok");
    expect(get).toHaveBeenCalled();
    expect(u?.plan).toBe("pro");
    expect(u?.app_env).toBe("dev");
    expect(u?.non_production_bypasses).toBe(true);
  });

  it("confirmAuth maps plans, envs, and flags", async () => {
    get.mockResolvedValueOnce({
      id: "2",
      plan: "free",
      app_env: "staging",
      internal_pro_bypass: true,
      can_manage_subscription: true,
      non_production_bypasses: null,
    });
    const u = await confirmAuth("t2");
    expect(u?.plan).toBe("free");
    expect(u?.app_env).toBeUndefined();
    expect(u?.internal_pro_bypass).toBe(true);
    expect(u?.can_manage_subscription).toBe(true);
    expect(u?.non_production_bypasses).toBeUndefined();
  });

  it("confirmAuth returns null when API returns empty", async () => {
    get.mockResolvedValueOnce(null);
    expect(await confirmAuth("tok")).toBeNull();
  });

  it("getCurrentUser returns null on 401", async () => {
    get.mockRejectedValueOnce(new ApiError("nope", 401));
    expect(await getCurrentUser()).toBeNull();
  });

  it("getCurrentUser returns null on other errors", async () => {
    get.mockRejectedValueOnce(new Error("network"));
    expect(await getCurrentUser()).toBeNull();
  });

  it("getCurrentUser returns null when me payload is empty", async () => {
    get.mockResolvedValueOnce(null);
    expect(await getCurrentUser()).toBeNull();
  });

  it("getCurrentUser normalizes successful payload", async () => {
    get.mockResolvedValueOnce({
      id: "u1",
      plan: "pro",
      email: null,
      login: "octocat",
      app_env: "local",
      non_production_bypasses: false,
    });
    const u = await getCurrentUser();
    expect(u?.id).toBe("u1");
    expect(u?.email).toBeUndefined();
    expect(u?.login).toBe("octocat");
    expect(u?.app_env).toBe("local");
    expect(u?.non_production_bypasses).toBe(false);
  });
});

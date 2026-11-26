/** @vitest-environment jsdom */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import LoginPage from "./page";

const state = vi.hoisted(() => ({
  search: "",
  user: null as null | { id: string },
  replace: vi.fn(),
  loginWithGitHub: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: state.replace }),
  useSearchParams: () => new URLSearchParams(state.search),
}));
vi.mock("@/contexts/auth-context", () => ({
  useAuth: () => ({
    user: state.user,
    isLoading: false,
    loginWithGitHub: state.loginWithGitHub,
  }),
}));
vi.mock("@/components/atproto-auth", () => ({ ATProtoAuth: () => null }));

describe("login destinations", () => {
  let host: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.clearAllMocks();
    state.search = "";
    state.user = null;
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    host.remove();
  });

  it("starts sign-in with the dashboard as the default return destination", async () => {
    await act(async () => root.render(<LoginPage />));
    const button = Array.from(host.querySelectorAll("button")).find(
      (item) => item.textContent === "Sign in with GitHub",
    );
    expect(button).toBeDefined();
    await act(async () => button!.click());
    expect(state.loginWithGitHub).toHaveBeenCalledWith("/dashboard", false);
  });

  it.each([
    ["", "/dashboard"],
    ["redirect=%2Fprojects%2Fproject-1%3Ftab%3Dreleases", "/projects/project-1?tab=releases"],
    ["redirect=%2F", "/"],
    ["redirect=https%3A%2F%2Fevil.example", "/"],
  ])("returns a signed-in user safely for query %s", async (search, destination) => {
    state.search = search;
    state.user = { id: "signed-in" };
    await act(async () => root.render(<LoginPage />));
    expect(state.replace).toHaveBeenCalledWith(destination);
  });
});

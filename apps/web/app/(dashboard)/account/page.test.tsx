import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import AccountPage from "./page";

const { useAuth } = vi.hoisted(() => ({ useAuth: vi.fn() }));
vi.mock("@/contexts/auth-context", () => ({ useAuth }));

describe("AccountPage", () => {
  it.each(["free", "pro"])("shows account controls without payment UI for legacy %s accounts", (plan) => {
    useAuth.mockReturnValue({
      user: { id: "account-1", login: "sam", plan, internal_pro_bypass: true, can_manage_subscription: true },
      isLoading: false,
      logout: vi.fn(),
    });
    const html = renderToStaticMarkup(<AccountPage />);
    expect(html).toContain("View GitHub Profile");
    expect(html).toContain("Sign Out");
    expect(html).not.toMatch(/Upgrade|Billing|Stripe|Current Plan|allowlist/);
    expect(html).not.toContain('href="/billing"');
  });

  it("shows ATProto-native identity without inventing a GitHub profile", () => {
    useAuth.mockReturnValue({user:{id:"native",login:"samclemente.me",github_connected:false,atproto_enabled:true,atproto_did:"did:plc:owner",atproto_handle:"samclemente.me"},isLoading:false,logout:vi.fn()});
    const html = renderToStaticMarkup(<AccountPage />);
    expect(html).toContain("samclemente.me");
    expect(html).toContain("did:plc:owner");
    expect(html).toContain("Link GitHub Account");
    expect(html).not.toContain("View GitHub Profile");
  });

  it("offers explicit ATProto linking for a GitHub account", () => {
    useAuth.mockReturnValue({user:{id:"github",login:"sam",github_connected:true,atproto_enabled:true},isLoading:false,logout:vi.fn()});
    const html = renderToStaticMarkup(<AccountPage />);
    expect(html).toContain("Link ATProto Account");
    expect(html).toContain("View GitHub Profile");
    expect(html).not.toContain("You sign in with GitHub");
  });
});

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
});

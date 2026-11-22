/**
 * Defense-in-depth: only navigate to known third-party hosts from API-provided URLs.
 */

const STRIPE_CHECKOUT_HOSTS = ["checkout.stripe.com", "billing.stripe.com"];
const GITHUB_INSTALL_HOSTS = ["github.com"];

function hostnameOf(urlString: string): string | null {
  try {
    const u = new URL(urlString);
    return u.hostname.toLowerCase();
  } catch {
    return null;
  }
}

export function assertStripeRedirectUrl(urlString: string): void {
  const host = hostnameOf(urlString);
  if (!host || !STRIPE_CHECKOUT_HOSTS.includes(host)) {
    throw new Error("untrusted_stripe_url");
  }
}

/** Allow GitHub itself, or the authenticated installer for this exact project. */
export function assertGitHubInstallUrl(
  urlString: string,
  context?: { origin: string; projectId: string }
): void {
  try {
    if (/[\\\r\n\u0000]/.test(urlString) || urlString.startsWith("//") ||
      (!/^[a-zA-Z][a-zA-Z\d+.-]*:/.test(urlString) && !urlString.startsWith("/"))) {
      throw new Error("untrusted_github_url");
    }
    const base = context ? new URL(context.origin) : undefined;
    const url = new URL(urlString, base);
    if ((url.protocol !== "https:" && url.protocol !== "http:") || url.username || url.password || url.hash) {
      throw new Error("untrusted_github_url");
    }
    if (GITHUB_INSTALL_HOSTS.includes(url.hostname.toLowerCase())) return;

    const projects = url.searchParams.getAll("project_id");
    if (
      context && base &&
      url.origin === base.origin &&
      url.pathname === "/api/auth/github/app/install" &&
      projects.length === 1 &&
      projects[0].toLowerCase() === context.projectId.toLowerCase()
    ) {
      const returns = url.searchParams.getAll("return_to");
      if (returns.length > 1) throw new Error("untrusted_github_url");
      if (returns.length === 1) {
        const rawReturn = returns[0];
        if (/[\\\r\n\u0000]/.test(rawReturn) || rawReturn.startsWith("//") ||
          (!/^[a-zA-Z][a-zA-Z\d+.-]*:/.test(rawReturn) && !rawReturn.startsWith("/"))) {
          throw new Error("untrusted_github_url");
        }
        const target = new URL(rawReturn, base);
        if (
          target.origin !== base.origin || target.username || target.password || target.hash ||
          target.pathname.toLowerCase() !== `/projects/${encodeURIComponent(context.projectId)}`.toLowerCase()
        ) throw new Error("untrusted_github_url");
      }
      return;
    }
  } catch {
    throw new Error("untrusted_github_url");
  }
  throw new Error("untrusted_github_url");
}

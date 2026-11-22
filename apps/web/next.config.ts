import type { NextConfig } from "next";
import path from "node:path";
import { fileURLToPath } from "node:url";

/** App package root (`apps/web`). */
const projectRoot = path.dirname(fileURLToPath(import.meta.url));
/** Bun workspace root (contains `bun.lock` and hoisted `node_modules`). */
const monorepoRoot = path.resolve(projectRoot, "../..");

const rawApiUrl = process.env.NEXT_PUBLIC_API_URL?.trim() ?? "";
const apiUrl = (rawApiUrl || "http://localhost:8080").replace(/\/$/, "");
const serviceMode = process.env.VERCEL_SERVICE_MODE === "1";

/** Rewrites are fixed at build time; missing API URL on Vercel produces 502 on every `/api/*` call. */
if (process.env.VERCEL === "1" && !serviceMode) {
  if (!rawApiUrl) {
    throw new Error(
      "NEXT_PUBLIC_API_URL must be set for Vercel builds. Without it, /api/* rewrites target localhost and the edge returns 502."
    );
  }
  if (/localhost|127\.0\.0\.1/i.test(apiUrl)) {
    throw new Error(
      "NEXT_PUBLIC_API_URL must be a public API origin on Vercel (not localhost). Example: https://api.yourdomain.com"
    );
  }
}

/** Only these backend prefixes are exposed through the app origin (`/api/*`). */
const proxiedApiPrefixes = [
  "auth",
  "admin",
  "dashboard",
  "projects",
  "github",
  "webhooks",
] as const;

const nextConfig: NextConfig = {
  outputFileTracingRoot: monorepoRoot,
  turbopack: {
    root: monorepoRoot,
  },
  async rewrites() {
    return [
      ...(!serviceMode ? [{ source: "/api/health", destination: `${apiUrl}/health` }] : []),
      // Map the bare /auth/mcp-oauth-resume path (used as the OAuth return_to URL by the
      // backend) to the proxied API route that handles redirect + cookie forwarding.
      // Must come before the generic /api/auth/:path* proxy so the filesystem route wins.
      {
        source: "/auth/mcp-oauth-resume",
        destination: "/api/auth/mcp-oauth-resume",
      },
      ...(!serviceMode ? proxiedApiPrefixes.map((prefix) => ({
        source: `/api/${prefix}/:path*`,
        destination: `${apiUrl}/${prefix}/:path*`,
      })) : []),
    ];
  },
  async headers() {
    return [
      {
        source: "/:path*",
        headers: [
          { key: "Strict-Transport-Security", value: "max-age=300" },
          { key: "X-Frame-Options", value: "DENY" },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
          {
            key: "Permissions-Policy",
            value: "camera=(), microphone=(), geolocation=()",
          },
        ],
      },
    ];
  },
};

export default nextConfig;

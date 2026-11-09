import { defineRailway, github, postgres, preserve, service, volume } from "railway/iac";

// Replaces the legacy railway/gateway.json and railway/web.json config files (Railway stops reading
// them on 2026-12-01).
//
// `railway config apply` deletes anything not declared here, so every existing resource and
// variable is listed. Variables use preserve(): values stay in Railway and never enter source.
// Add a variable here before (or instead of) adding it in the dashboard, or the next apply
// removes it. Domains are not managed here: Railway keeps them, and the Gateway provisions tenant
// custom domains at runtime.
const gatewayVariables = [
  "APP_ENV", "CORS_ORIGIN", "DATABASE_HOST", "DATABASE_NAME", "DATABASE_PASSWORD", "DATABASE_PORT",
  "DATABASE_USERNAME", "ENCRYPTION_KEY", "FRONTEND_URL", "GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET",
  "GITHUB_OAUTH_REDIRECT_URI", "HOST", "INTERNAL_ADMIN_GITHUB_LOGINS", "MCP_OAUTH_ENABLED", "PORT",
  "SAAS_MCP_BASE_DOMAIN", "SAAS_MCP_PATH", "SAAS_MCP_URL_SCHEME", "SESSION_COOKIE_DOMAIN",
  "STRIPE_PRICE_PRO_MONTHLY", "STRIPE_PRICE_PRO_YEARLY", "STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET",
  "WEBHOOK_BASE_URL",
  // Stage these before the SQLite cutover. Preserve existing Postgres credentials for rollback.
  "DATABASE_DRIVER", "USE_SQLITE", "SQLITE_PATH", "SQLITE_ALLOW_CREATE",
];
const productionOnlyGatewayVariables = [
  "DATABASE_SSLROOTCERT_PEM", "DATABASE_TLS_PINNED_CA", "INTERNAL_PRO_GITHUB_LOGINS",
  "MCP_OAUTH_API_ORIGIN", "MCP_TRUST_X_FORWARDED_HOST", "RAILWAY_DOMAIN_TARGET_PORT",
];
const webVariables = ["BACKEND_URL", "NEXT_PUBLIC_API_URL", "NEXT_PUBLIC_APP_ENV", "NEXT_PUBLIC_APP_URL"];

const preserved = (names: string[]) => Object.fromEntries(names.map((name) => [name, preserve()]));

export default defineRailway((ctx, project) => {
  const isProduction = ctx.isEnvironment("production");

  const Postgres = postgres("Postgres", { region: "sfo" });
  Postgres.networking = { privateNetworkEndpoint: "postgres" };
  const postgresVolume = volume("postgres-volume", {
    alerts: { usage: { "100": {}, "80": {}, "95": {} } },
    allowOnlineResize: true,
    region: "sfo",
    sizeMB: 50000,
  });
  // Attached before switching drivers; never put the live database in the container filesystem.
  const gatewayVolume = volume("gateway-data", {
    alerts: { usage: { "100": {}, "80": {}, "95": {} } },
    allowOnlineResize: true,
    region: "sfo",
    sizeMB: 50000,
  });

  // Both services build from the monorepo root: the Gateway Dockerfile copies services/mcp-gateway/*
  // and Web's build runs Turbo across the Bun workspace.
  const source = github("Stygian-Tech/my-context-protocol", {
    branch: isProduction ? "main" : "dev",
    checkSuites: true,
    rootDirectory: "/",
  });

  const Gateway = service("Gateway", {
    source,
    replicas: 1,
    volumeMounts: { "/data": gatewayVolume },
    build: {
      builder: "DOCKERFILE",
      dockerfilePath: "/services/mcp-gateway/Dockerfile.railway",
      watchPatterns: ["/services/mcp-gateway/**", "/.railway/**"],
    },
    deploy: {
      healthcheckPath: "/health",
      healthcheckTimeout: 600,
      restartPolicyType: "ALWAYS",
      // Keep the Gateway awake: its retention, billing and analytics schedulers only run while
      // the process is up, and MCP clients should not pay a cold start.
      sleepApplication: false,
    },
    env: preserved(isProduction ? [...gatewayVariables, ...productionOnlyGatewayVariables] : gatewayVariables),
  });

  const Web = service("Web", {
    source,
    build: {
      builder: "RAILPACK",
      buildCommand: "bun run turbo run build --filter=@mycontext/web...",
      watchPatterns: ["/apps/web/**", "/packages/**", "/package.json", "/bun.lock", "/turbo.json", "/.railway/**"],
    },
    deploy: {
      startCommand: "bun --cwd apps/web start",
      healthcheckPath: "/",
      healthcheckTimeout: 300,
      restartPolicyType: "ALWAYS",
      sleepApplication: false,
    },
    env: preserved(webVariables),
  });

  // Keep Postgres and its volume until the SQLite migration and rollback window are complete.
  return project("MyContextProtocol", { resources: [Postgres, postgresVolume, gatewayVolume, Gateway, Web] });
});

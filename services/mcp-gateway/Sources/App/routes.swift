import Vapor

func routes(_ app: Application) throws {
    let mcpIngress = app.grouped(
        TenantHostMiddleware(),
        McpTenantHostRequiredMiddleware(),
        McpIpRateLimitMiddleware()
    )

    app.get { req async throws -> Response in
        if let host = RequestPublicOrigin.routingHostname(for: req) {
            switch try await TenantHostMiddleware.resolveProject(for: host, request: req) {
            case .resolved(let project, _):
                if project.suspendedAt != nil {
                    return Response(status: .paymentRequired, body: .init(string: "This project is suspended. The account owner must select an active project or upgrade to Pro."))
                }
                req.storage[ResolvedHostProjectKey.self] = project
            case .denied(let status, let message):
                return Response(status: status, body: .init(string: message))
            case .unresolved:
                break
            }
        }
        if req.storage[ResolvedHostProjectKey.self] != nil, AppEnvironment.mcpOAuthEnabled {
            return try McpOAuthController.rootOAuthChallenge(req: req)
        }
        return Response(status: .ok, body: .init(string: "MyContextProtocol"))
    }

    app.get("health") { _ in
        "ok"
    }

    // Called by Caddy's on_demand_tls `ask` hook before issuing a TLS cert for a hostname.
    // Returns 200 to allow, 422 to deny. Set INTERNAL_TLS_ASK_SECRET when this endpoint is reachable beyond localhost.
    app.get("internal", "custom-domain", "verify-for-tls") { req in
        try await InternalController.verifyForTls(req: req)
    }

    app.get("auth", "github") { req in
        try await AuthController.githubInitiate(req: req)
    }
    app.get("auth", "cli", "github") { req in
        try await AuthController.cliGithubInitiate(req: req)
    }
    app.get("auth", "github", "mcp-oauth-start") { req in
        try await McpOAuthController.githubMcpOauthStart(req: req)
    }
    app.get("auth", "mcp-oauth-resume") { req in
        try await McpOAuthController.mcpOauthResume(req: req)
    }
    app.get("auth", "github", "callback") { req in
        try await AuthController.githubCallback(req: req)
    }
    app.get("auth", "github", "app", "callback") { req in
        try await GitHubAppController.installCallback(req: req)
    }
    app.get("auth", "me") { req in
        try await AuthController.me(req: req)
    }
    app.get("auth", "confirm") { req in
        try await AuthController.confirm(req: req)
    }
    app.post("auth", "logout") { req in
        try await AuthController.logout(req: req)
    }

    let protected = app.grouped(app.sessions.middleware, SessionAuthMiddleware())
    protected.get("dashboard", "summary") { req in
        try await ProjectController.accountDashboardSummary(req: req)
    }
    protected.get("dashboard", "timeseries") { req in
        try await ProjectController.accountDashboardTimeseries(req: req)
    }
    protected.get("projects") { req in
        try await ProjectController.list(req: req)
    }
    protected.post("projects") { req in
        try await ProjectController.create(req: req)
    }
    protected.get("projects", ":id", "dashboard", "summary") { req in
        try await ProjectController.projectDashboardSummary(req: req)
    }
    protected.get("projects", ":id", "dashboard", "timeseries") { req in
        try await ProjectController.projectDashboardTimeseries(req: req)
    }
    protected.get("projects", ":id") { req in
        try await ProjectController.get(req: req)
    }
    protected.patch("projects", ":id") { req in
        try await ProjectController.update(req: req)
    }
    protected.post("projects", ":id", "set-active") { req in
        try await ProjectController.setActive(req: req)
    }
    protected.get("projects", ":id", "catalog") { req in
        try await ProjectController.catalog(req: req)
    }
    protected.patch("projects", ":id", "catalog-markdown") { req in
        try await ProjectController.updateCatalogMarkdown(req: req)
    }
    protected.get("projects", ":id", "repo-connection") { req in
        try await ProjectController.getRepoConnection(req: req)
    }
    protected.post("projects", ":id", "connect-repo") { req in
        try await ProjectController.connectRepo(req: req)
    }
    protected.post("projects", ":id", "sync") { req in
        try await ProjectController.sync(req: req)
    }
    protected.get("projects", ":id", "releases") { req in
        try await ProjectController.listReleases(req: req)
    }
    protected.post("projects", ":id", "releases", ":releaseId", "activate") { req in
        try await ProjectController.activateRelease(req: req)
    }
    protected.get("projects", ":id", "releases", ":releaseId", "validation") { req in
        try await ProjectController.releaseValidation(req: req)
    }
    protected.get("projects", ":id", "releases", ":releaseId", "compiled-skills") { req in
        try await ProjectController.listCompiledSkills(req: req)
    }
    protected.patch("projects", ":id", "releases", ":releaseId", "compiled-skills", ":compiledSkillId") { req in
        try await ProjectController.updateCompiledSkill(req: req)
    }
    protected.post("projects", ":id", "releases", ":releaseId", "compiled-skills", ":compiledSkillId", "writeback") { req in
        try await ProjectController.writeBackCompiledSkillMetadata(req: req)
    }
    protected.get("projects", ":id", "skill-usage") { req in
        try await SkillUsageController.index(req: req)
    }
    protected.get("projects", ":id", "skill-runtime") { req in
        try await ProjectController.runtimeSettings(req: req)
    }
    protected.patch("projects", ":id", "skill-runtime") { req in
        try await ProjectController.updateRuntimeSettings(req: req)
    }
    protected.get("projects", ":id", "api-keys") { req in
        try await ProjectController.listApiKeys(req: req)
    }
    protected.post("projects", ":id", "api-keys") { req in
        try await ProjectController.createApiKey(req: req)
    }
    protected.patch("projects", ":id", "api-keys", ":keyId") { req in
        try await ProjectController.updateApiKey(req: req)
    }
    protected.delete("projects", ":id", "api-keys", ":keyId") { req in
        try await ProjectController.revokeApiKey(req: req)
    }
    protected.get("projects", ":id", "request-logs") { req in
        try await ProjectController.listRequestLogs(req: req)
    }
    protected.get("projects", ":id", "custom-domain") { req in
        try await ProjectController.getCustomDomain(req: req)
    }
    protected.post("projects", ":id", "custom-domain") { req in
        try await ProjectController.setCustomDomain(req: req)
    }
    protected.post("projects", ":id", "custom-domain", "verify") { req in
        try await ProjectController.verifyCustomDomain(req: req)
    }
    protected.post("billing", "checkout-session") { req in
        try await BillingController.createCheckoutSession(req: req)
    }
    protected.post("billing", "portal-session") { req in
        try await BillingController.createPortalSession(req: req)
    }
    protected.get("github", "repos") { req in
        try await AuthController.listGithubRepos(req: req)
    }
    protected.get("auth", "github", "app", "install") { req in
        try await GitHubAppController.installRedirect(req: req)
    }

    let adminProtected = protected.grouped(AdminAuthMiddleware())
    adminProtected.get("admin", "metrics") { req in
        try await AdminController.platformMetrics(req: req)
    }
    adminProtected.get("admin", "timeseries") { req in
        try await AdminController.adminDashboardTimeseries(req: req)
    }
    adminProtected.post("admin", "analytics", "rollup-refresh") { req in
        try await AdminController.rollupRefresh(req: req)
    }
    adminProtected.post("admin", "lookup") { req in
        try await AdminController.lookup(req: req)
    }
    adminProtected.get("admin", "privileged-accounts") { req in
        try await AdminController.listPrivilegedAccounts(req: req)
    }
    adminProtected.post("admin", "account-flags") { req in
        try await AdminController.updateFlags(req: req)
    }

    app.on(.POST, ["webhooks", "github"], body: .collect(maxSize: ByteCount(value: 512 * 1024))) { req in
        try await WebhookController.github(req: req)
    }
    app.on(.POST, ["webhooks", "github-app"], body: .collect(maxSize: ByteCount(value: 512 * 1024))) { req in
        try await GitHubAppWebhookController.handle(req: req)
    }
    app.on(.POST, ["webhooks", "stripe"], body: .collect(maxSize: ByteCount(value: 1024 * 1024))) { req in
        try await StripeWebhookController.handle(req: req)
    }

    mcpIngress.get(".well-known", "oauth-protected-resource") { req async throws in
        try await McpOAuthController.protectedResourceMetadata(req: req).encodeResponse(for: req)
    }
    mcpIngress.get(".well-known", "oauth-protected-resource", "**") { req async throws in
        try await McpOAuthController.protectedResourceMetadata(req: req).encodeResponse(for: req)
    }
    mcpIngress.get(".well-known", "oauth-authorization-server") { req async throws in
        try await McpOAuthController.authorizationServerMetadata(req: req).encodeResponse(for: req)
    }
    mcpIngress.get(".well-known", "oauth-authorization-server", "**") { req async throws in
        try await McpOAuthController.authorizationServerMetadata(req: req).encodeResponse(for: req)
    }
    mcpIngress.get("authorize") { req async throws in
        try await McpOAuthController.authorize(req: req)
    }
    mcpIngress.get("oauth", "consent") { req async throws in
        try await McpOAuthController.consentPage(req: req)
    }
    mcpIngress.on(.POST, "oauth", "consent", body: .collect(maxSize: ByteCount(value: 256 * 1024))) { req async throws in
        try await McpOAuthController.consentSubmit(req: req)
    }
    mcpIngress.on(.POST, "token", body: .collect(maxSize: ByteCount(value: 64 * 1024))) { req async throws in
        try await McpOAuthController.token(req: req)
    }
    mcpIngress.on(.POST, "register", body: .collect(maxSize: ByteCount(value: 64 * 1024))) { req async throws in
        try await McpOAuthController.registerClient(req: req)
    }

    McpRoutePath.registerPing(on: mcpIngress) { req in
        try await McpPingController.handle(req: req)
    }

    let mcpRoutes = mcpIngress.grouped(McpCredentialMiddleware())
    McpRoutePath.registerPost(on: mcpRoutes) { req in
        try await MCPController.handle(req: req)
    }
    McpRoutePath.registerGet(on: mcpRoutes) { req in
        try await McpSseController.handle(req: req)
    }
    McpRoutePath.registerGetEvents(on: mcpRoutes) { req in
        try await McpSseController.handle(req: req)
    }
}

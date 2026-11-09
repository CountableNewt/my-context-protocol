import Fluent
import FluentPostgresDriver
import FluentSQLiteDriver
import Logging
import NIOSSL
import PostgresKit
import Vapor

private func isTruthyEnv(_ key: String) -> Bool {
    guard let raw = Environment.get(key) else { return false }
    let v = raw.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    return v == "1" || v == "true" || v == "yes"
}

public func configure(_ app: Application) async throws {
    // Non-production: default root log level to `.debug` so MCP handler traces (`mcpTrace`) are visible unless LOG_LEVEL is set.
    if AppEnvironment.isNonProduction {
        let raw = Environment.get("LOG_LEVEL")?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if raw.isEmpty {
            app.logger.logLevel = .debug
            app.logger.info(
                "Non-production: LOG_LEVEL unset; using debug (Fluent SQL + verbose HTTP). MCP handler lines use info (DEV_LOG_MCP). Set LOG_LEVEL=info to reduce SwiftLog noise."
            )
        }
    }

    let deploy = AppEnvironment.deployKind()
    let bypass = AppEnvironment.nonProductionBypassesActive
    let strict = AppEnvironment.strictProGating
    app.logger.info(
        "APP_ENV=\(deploy.rawValue) non_production_bypasses=\(bypass) STRICT_PRO_GATING=\(strict)"
    )

    // Log critical MCP configuration so routing / OAuth issues are immediately visible in container logs.
    let mcpBaseDomain = Environment.get("SAAS_MCP_BASE_DOMAIN")?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    let mcpOauthEnabled = AppEnvironment.mcpOAuthEnabled
    let mcpTrustFwdHost = AppEnvironment.mcpTrustXForwardedHost
    let mcpOauthApiOrigin = Environment.get("MCP_OAUTH_API_ORIGIN")?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    app.logger.info("MCP config: SAAS_MCP_BASE_DOMAIN=\(mcpBaseDomain.isEmpty ? "(not set)" : mcpBaseDomain) MCP_OAUTH_ENABLED=\(mcpOauthEnabled) MCP_TRUST_X_FORWARDED_HOST=\(mcpTrustFwdHost) MCP_OAUTH_API_ORIGIN=\(mcpOauthApiOrigin.isEmpty ? "(not set)" : mcpOauthApiOrigin)")

    if let loginRedirect = try? GitHubOAuthLoginConfig.redirectURI(logger: app.logger) {
        app.logger.info("GitHub login OAuth redirect_uri=\(loginRedirect)")
    }

    if DevLoggingConfig.verboseHttpEnabled {
        app.logger.info("Verbose HTTP request tracing on (non-production default; set DEV_LOG_HTTP=0 to disable)")
        app.middleware.use(VerboseRequestLoggingMiddleware(), at: .beginning)
    }
    if AppEnvironment.isNonProduction, DevLoggingConfig.sqlLogEnabled {
        app.logger.info(
            "DEV_LOG_SQL: Fluent SQL logging at debug (non-prod default on; set DEV_LOG_SQL=0 to disable)"
        )
    }

    app.middleware.use(SecurityHeadersMiddleware(), at: .beginning)
    app.middleware.use(BrowserOriginValidationMiddleware(), at: .beginning)

    let corsOrigin = Environment.get("FRONTEND_URL") ?? Environment.get("CORS_ORIGIN") ?? "http://localhost:3000"
    // Echo Sec-Fetch / localhost origins only in local dev; staging/prod must use an explicit allow-list.
    // Non-prod + localhost in config: echo request Origin (localhost vs 127.0.0.1). Prod uses fixed allow-list.
    let allowedOrigin: CORSMiddleware.AllowOriginSetting =
        (AppEnvironment.isNonProduction && corsOrigin.contains("localhost"))
        ? .originBased
        : .custom(corsOrigin)
    let corsConfig = CORSMiddleware.Configuration(
        allowedOrigin: allowedOrigin,
        allowedMethods: [.GET, .POST, .PUT, .OPTIONS, .DELETE, .PATCH],
        allowedHeaders: [.accept, .authorization, .contentType, .origin, .xRequestedWith, .init("MCP-Protocol-Version")],
        allowCredentials: true
    )
    app.middleware.use(CORSMiddleware(configuration: corsConfig), at: .beginning)
    // Outermost: final status after ErrorMiddleware (Vapor’s default route log is request-only, no status).
    app.middleware.use(HTTPAccessLogMiddleware(), at: .beginning)
    if DevLoggingConfig.httpAccessLogEnabled {
        app.logger.info(
            "HTTP access log: http_access … status=… lines after each request (alongside req_start/req_end when DEV_LOG_HTTP is on). Prod: HTTP_ACCESS_LOG=1; disable: HTTP_ACCESS_LOG=0."
        )
    }

    app.middleware.use(FileMiddleware(publicDirectory: app.directory.publicDirectory))
    app.routes.defaultMaxBodySize = "5mb"

    if AppEnvironment.useMemorySessions {
        app.sessions.use(.memory)
    } else {
        app.sessions.use { _ in FluentSessionDriver() }
    }
    // Allow session cookie over HTTP on localhost (isSecure: false) so OAuth redirect flow works
    let isLocalhost = corsOrigin.contains("localhost")
    // When the browser hits both the app host (e.g. testing.app.com) and the API host (e.g. api.testing.app.com),
    // host-only cookies are not shared. Set SESSION_COOKIE_DOMAIN to the registrable domain (e.g. .mycontextprotocol.dev)
    // so GitHub App Setup URL can use the API host and still receive the same session + install fallback keys.
    let sessionCookieDomain: String? = {
        if isLocalhost { return nil }
        let raw = Environment.get("SESSION_COOKIE_DOMAIN")?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return raw.isEmpty ? nil : raw
    }()
    if let domain = sessionCookieDomain {
        app.logger.info("Session cookie Domain=\(domain) (shared across subdomains)")
    }
    app.sessions.configuration.cookieFactory = { sessionID in
        .init(
            string: sessionID.string,
            domain: sessionCookieDomain,
            path: "/",
            isSecure: !isLocalhost,
            isHTTPOnly: true,
            sameSite: .lax
        )
    }
    app.middleware.use(app.sessions.middleware)

    /// When `true`, integration tests may use `DATABASE_URL` / `SUPABASE_DB_URL` instead of in-memory SQLite.
    let usePostgresInTests = isTruthyEnv("TEST_USE_POSTGRES")

    let sqlLiteLevel = DevLoggingConfig.sqliteSqlLogLevel
    let sqlPostgresLevel = DevLoggingConfig.postgresSqlLogLevel

    if app.environment == .testing, !usePostgresInTests {
        app.databases.use(.sqlite(.memory, sqlLogLevel: sqlLiteLevel), as: .sqlite)
    } else if try SQLiteBootstrap.driver() == .sqlite {
        let path = try SQLiteBootstrap.path(
            raw: Environment.get("SQLITE_PATH"), deployKind: deploy, workingDirectory: app.directory.workingDirectory
        )
        try await SQLiteBootstrap.install(on: app, path: path, deployKind: deploy, allowCreate: isTruthyEnv("SQLITE_ALLOW_CREATE"))
    } else if let databaseURL = Environment.get("DATABASE_URL"), !databaseURL.isEmpty {
        let useInsecureTLS = Environment.get("DATABASE_INSECURE_TLS").map { $0.lowercased() }.map { $0 == "1" || $0 == "true" } ?? false
        try DatabaseBootstrap.assertInsecurePostgresTLSAllowed(useInsecureTLS, deployKind: deploy)
        try DatabaseBootstrap.assertPostgresConnectionURLHostAllowedIfResolvable(databaseURL)
        if useInsecureTLS {
            var config = try SQLPostgresConfiguration(url: databaseURL)
            var tlsConfig = TLSConfiguration.makeClientConfiguration()
            tlsConfig.certificateVerification = .none
            config.coreConfiguration.tls = .require(try NIOSSLContext(configuration: tlsConfig))
            app.databases.use(.postgres(configuration: config, sqlLogLevel: sqlPostgresLevel), as: .psql)
        } else if deploy == .prod {
            var config = try SQLPostgresConfiguration(url: databaseURL)
            config.coreConfiguration.tls = .require(try DatabaseBootstrap.verifiedPostgresSSLContext(connectionURL: databaseURL))
            app.databases.use(
                .postgres(configuration: config, sqlLogLevel: sqlPostgresLevel),
                as: .psql
            )
        } else {
            app.databases.use(try .postgres(url: databaseURL, sqlLogLevel: sqlPostgresLevel), as: .psql)
        }
    } else if let url = Environment.get("SUPABASE_DB_URL"), !url.isEmpty {
        try DatabaseBootstrap.assertPostgresConnectionURLHostAllowedIfResolvable(url)
        if deploy == .prod {
            var config = try SQLPostgresConfiguration(url: url)
            config.coreConfiguration.tls = .require(try DatabaseBootstrap.verifiedPostgresSSLContext(connectionURL: url))
            app.databases.use(
                .postgres(configuration: config, sqlLogLevel: sqlPostgresLevel),
                as: .psql
            )
        } else {
            app.databases.use(try .postgres(url: url, sqlLogLevel: sqlPostgresLevel), as: .psql)
        }
    } else if app.environment == .testing {
        app.databases.use(.sqlite(.memory, sqlLogLevel: sqlLiteLevel), as: .sqlite)
    } else {
        let params: PostgresDiscreteConnectionParams
        do {
            params = try DatabaseBootstrap.postgresDiscreteParameters(for: AppEnvironment.deployKind())
            try DatabaseBootstrap.assertPostgresHostAllowedForDeployedAppEnv(params.hostname)
        } catch let error as DatabaseBootstrapError {
            app.logger.critical("\(error.description)")
            throw error
        }
        let pgConfig: SQLPostgresConfiguration
        if deploy == .prod {
            pgConfig = SQLPostgresConfiguration(
                hostname: params.hostname,
                port: params.port,
                username: params.username,
                password: params.password,
                database: params.database,
                tls: .require(try DatabaseBootstrap.verifiedPostgresSSLContext())
            )
        } else {
            pgConfig = SQLPostgresConfiguration(
                hostname: params.hostname,
                port: params.port,
                username: params.username,
                password: params.password,
                database: params.database,
                tls: .disable
            )
        }
        app.databases.use(.postgres(configuration: pgConfig, sqlLogLevel: sqlPostgresLevel), as: .psql)
    }

    app.migrations.add(CreateAccounts())
    app.migrations.add(CreateProjects())
    app.migrations.add(CreateRepoConnections())
    app.migrations.add(CreateReleases())
    app.migrations.add(CreateSkillPackages())
    app.migrations.add(CreateToolsIndex())
    app.migrations.add(CreateAuthConfigs())
    app.migrations.add(CreateApiKeys())
    app.migrations.add(CreateRequestLogs())
    app.migrations.add(SeedPersonalUse())
    app.migrations.add(AlterAccountsForOAuth())
    app.migrations.add(AddSaaSFields())
    app.migrations.add(CreateCompiledSkills())
    app.migrations.add(CreateRoutingRules())
    app.migrations.add(CreateCapabilityDefs())
    app.migrations.add(CreateValidationReports())
    app.migrations.add(AddBillingToAccounts())
    // Before AlterProjectsTenantAndDomain: `Project` queries (e.g. subdomain backfill) and the
    // SQLite table swap must see all columns the model declares at that point.
    app.migrations.add(AddMcpCatalogMarkdownOverrideToProjects())
    app.migrations.add(AddSuspendedAtToProjects())
    app.migrations.add(AlterProjectsTenantAndDomain())
    app.migrations.add(AddGithubInstallationToRepoConnections())
    app.migrations.add(CreateGitHubAppInstallIntents())
    app.migrations.add(AddGithubAppInstallationIdToAccounts())
    app.migrations.add(AddCompiledSkillSkillBody())
    app.migrations.add(AddRequestLogErrorMessage())
    app.migrations.add(AddMcpCapabilityColumnsToRequestLogs())
    app.migrations.add(AddNameToApiKeys())
    app.migrations.add(AddAgentHintsToRoutingRules())
    app.migrations.add(AddCompiledSkillBodyDiffAndReleaseCounts())
    app.migrations.add(AddYamlFrontmatterPresentToCompiledSkills())
    app.migrations.add(NormalizeRepoDefaultBranchToMain())
    app.migrations.add(CreateAppSessions())
    app.migrations.add(CreateOAuthHandoffTokens())
    app.migrations.add(AddAdminFlagsToAccounts())
    app.migrations.add(AddAccountPrivilegeGrantedAt())
    app.migrations.add(CreateAdminAnalyticsHourly())
    app.migrations.add(AddPerformanceIndexes())
    app.migrations.add(CreateMcpOauthClients())
    app.migrations.add(AddProjectIdToMcpOauthClients())
    app.migrations.add(CreateMcpOauthPendingAuthorizations())
    app.migrations.add(CreateMcpOauthAuthorizationCodes())
    app.migrations.add(CreateMcpOauthAccessTokens())
    app.migrations.add(StripLegacySkillPrefixFromMcpWireNames())
    app.migrations.add(AddStripeStatusCheckedAt())
    app.migrations.add(AddPortableSkillRuntime())
    app.migrations.add(HardenPortableSkillRuntime())
    app.migrations.add(AddSkillUsageAnalytics())

    try await app.autoMigrate()

    app.lifecycle.use(AdminAnalyticsRollupLifecycle())
    app.lifecycle.use(SkillUsageRetentionLifecycle())
    app.lifecycle.use(StripeReconciliationLifecycle())
    app.lifecycle.use(LocalDevFixtureLifecycle())

    try routes(app)
}

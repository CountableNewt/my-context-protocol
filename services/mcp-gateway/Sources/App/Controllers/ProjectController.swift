import Crypto
import Fluent
import Foundation
import Vapor

struct ProjectController {
    private static func formatDate(_ date: Date?) -> String {
        guard let date = date else { return "" }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: date)
    }

    private static func projectResponse(_ project: Project) -> ProjectResponse {
        ProjectResponse(
            id: project.id!.uuidString,
            account_id: project.$account.id.uuidString,
            name: project.name,
            slug: project.slug,
            subdomain: project.subdomain ?? "",
            created_at: formatDate(project.createdAt),
            custom_domain: project.customDomain,
            custom_domain_verified_at: project.customDomainVerifiedAt.map { formatDate($0) },
            active_release_id: project.activeReleaseId?.uuidString,
            mcp_url: McpUrlBuilder.publicMcpUrl(for: project),
            mcp_oauth_enabled: AppEnvironment.mcpOAuthEnabled,
            suspended_at: nil
        )
    }

    private static func allocateSubdomain(db: Database) async throws -> String {
        for _ in 0 ..< 32 {
            let candidate = TenantSubdomainGenerator.make()
            let taken = try await Project.query(on: db).filter(\.$subdomain == candidate).first() != nil
            if !taken { return candidate }
        }
        throw Abort(.internalServerError, reason: "Could not allocate subdomain")
    }

    private static func isValidSlug(_ s: String) -> Bool {
        guard !s.isEmpty, s.count <= 128 else { return false }
        guard let r = try? NSRegularExpression(pattern: "^[a-z0-9]+(-[a-z0-9]+)*$") else { return false }
        let range = NSRange(s.startIndex..., in: s)
        return r.firstMatch(in: s, options: [], range: range) != nil
    }

    private static func syncRateConfig() -> (max: Int, windowSeconds: TimeInterval) {
        let max = Int(Environment.get("SYNC_RATE_LIMIT_MAX") ?? "") ?? 60
        let window = TimeInterval(Int(Environment.get("SYNC_RATE_LIMIT_WINDOW_SECONDS") ?? "") ?? 3600)
        return (max, window)
    }

    private static func requireAccount(_ req: Request) throws -> Account {
        guard let account = req.storage[AccountKey.self], let account = account else {
            throw Abort(.unauthorized, reason: "Not authenticated")
        }
        return account
    }

    private static func requireProject(_ req: Request, accountId: UUID) async throws -> Project {
        guard let projectId = req.parameters.get("id", as: UUID.self) else {
            throw Abort(.badRequest, reason: "Invalid project ID")
        }
        guard let project = try await Project.query(on: req.db)
            .filter(\.$id == projectId)
            .filter(\.$account.$id == accountId)
            .first() else {
            throw Abort(.notFound, reason: "Project not found")
        }
        return project
    }

    /// True when GitHub App + private key are configured so `connect-repo` can use installation tokens for webhooks.
    private static func isGitHubAppConfiguredForWebhooks() -> Bool {
        guard let slug = Environment.get("GITHUB_APP_SLUG"), !slug.isEmpty else { return false }
        guard let cid = Environment.get("GITHUB_APP_CLIENT_ID"), !cid.isEmpty else { return false }
        return (try? GitHubAppInstallationTokenService.loadPrivatePEM()) != nil
    }

    /// Public URL of the project page (same origin as `FRONTEND_URL` / `CORS_ORIGIN` — used for GitHub App install `return_to`).
    private static func frontendProjectPageUrl(projectId: UUID) throws -> String {
        guard let base = AppFrontendURL.normalizedBase() else {
            throw Abort(.internalServerError, reason: "FRONTEND_URL or CORS_ORIGIN must be set for GitHub App install flow")
        }
        return "\(base)/projects/\(projectId.uuidString)"
    }

    /// Browser navigates here (same-origin `/api` proxy) so session cookies apply; includes `owner`/`repo` so install state can resume connect.
    private static func buildGitHubAppInstallUrl(projectId: UUID, owner: String, repo: String, returnTo: String) throws -> String {
        guard let base = AppFrontendURL.normalizedBase() else {
            throw Abort(.internalServerError, reason: "FRONTEND_URL or CORS_ORIGIN must be set for GitHub App install flow")
        }
        guard let baseUrl = URL(string: base) else {
            throw Abort(.internalServerError, reason: "Invalid FRONTEND_URL or CORS_ORIGIN")
        }
        var components = URLComponents()
        components.scheme = baseUrl.scheme
        components.host = baseUrl.host
        components.port = baseUrl.port
        components.path = "/api/auth/github/app/install"
        components.queryItems = [
            URLQueryItem(name: "project_id", value: projectId.uuidString),
            URLQueryItem(name: "owner", value: owner),
            URLQueryItem(name: "repo", value: repo),
            URLQueryItem(name: "return_to", value: returnTo)
        ]
        guard let url = components.url else {
            throw Abort(.internalServerError, reason: "Could not build GitHub App install URL")
        }
        return url.absoluteString
    }

    private static func compiledSkillResponse(
        _ cs: CompiledSkill,
        schemaJson: String?,
        routingRule: RoutingRule?
    ) -> CompiledSkillResponse {
        let h = RoutingHints.from(rule: routingRule)
        return CompiledSkillResponse(
            id: cs.id!.uuidString,
            release_id: cs.$release.id.uuidString,
            skill_package_id: cs.$skillPackage.id.uuidString,
            path: cs.path,
            name: cs.name,
            summary: cs.summary,
            skill_body: cs.skillBody,
            schema_json: schemaJson,
            yaml_frontmatter_present: cs.yamlFrontmatterPresent,
            exposure_type: cs.exposureType,
            risk_level: cs.riskLevel,
            repo_specific: cs.repoSpecific,
            status: cs.status,
            canonical_schema_version: cs.canonicalSchemaVersion,
            skill_id: cs.skillId,
            kind: cs.kind,
            scope: cs.scope,
            activation_mode: cs.activationMode,
            enforcement: cs.enforcement,
            priority: cs.priority,
            version: cs.version,
            source_checksum: cs.sourceChecksum,
            canonical_json: cs.canonicalJson,
            clarification_required: cs.clarificationRequired,
            clarification_questions: SkillRuntimeJSON.decode([SkillClarificationQuestion].self, from: cs.clarificationJson) ?? [],
            use_when: h.useWhen ?? [],
            avoid_when: h.avoidWhen ?? [],
            failure_modes: h.failureModes ?? [],
            invoke_first: h.invokeFirst ?? false,
            body_diff_unified: cs.bodyDiffUnified,
            body_diff_prior_release_id: cs.bodyDiffPriorReleaseId?.uuidString
        )
    }

    private static func jsonRoutingArray(_ strings: [String]) throws -> String? {
        if strings.isEmpty { return nil }
        let data = try JSONEncoder().encode(strings)
        return String(data: data, encoding: .utf8)
    }

    private static func applyRoutingPatch(
        compiledSkillId: UUID,
        patch: CompiledSkillRoutingPatch,
        db: Database
    ) async throws {
        let useList = patch.use_when ?? []
        let avoidList = patch.avoid_when ?? []
        let failList = patch.failure_modes ?? []
        let invokeFlag = patch.invoke_first ?? false

        let useJson = try jsonRoutingArray(useList)
        let avoidJson = try jsonRoutingArray(avoidList)
        let failJson = try jsonRoutingArray(failList)

        let existing = try await RoutingRule.query(on: db)
            .filter(\.$compiledSkill.$id == compiledSkillId)
            .first()
        let rule = existing ?? RoutingRule(compiledSkillId: compiledSkillId)
        rule.useWhenJson = useJson
        rule.avoidWhenJson = avoidJson
        rule.failureModesJson = failJson
        rule.invokeFirst = invokeFlag
        try await rule.save(on: db)
    }

    static func list(req: Request) async throws -> [ProjectResponse] {
        let account = try requireAccount(req)
        let projects = try await Project.query(on: req.db)
            .filter(\.$account.$id == account.id!)
            .all()
        return projects.map { projectResponse($0) }
    }

    static func get(req: Request) async throws -> ProjectResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        return projectResponse(project)
    }

    /// Update project metadata (display name only for now).
    static func update(req: Request) async throws -> ProjectResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        struct PatchBody: Content {
            var name: String
        }
        let body = try req.content.decode(PatchBody.self)
        let name = body.name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else { throw Abort(.badRequest, reason: "Name is required") }
        guard name.count <= 256 else { throw Abort(.badRequest, reason: "Name is too long") }
        project.name = name
        try await project.save(on: req.db)
        return projectResponse(project)
    }

    /// Active-release MCP catalog (tools, resources, prompts) for dashboard clients.
    static func catalog(req: Request) async throws -> ProjectCatalogResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let mcpUrl = McpUrlBuilder.publicMcpUrl(for: project)
        let catalogGenerated = try await McpCatalogMarkdown.buildGenerated(db: req.db, projectId: project.id!)
        let catalogMarkdown = try await McpCatalogMarkdown.build(db: req.db, projectId: project.id!)
        let syntheticTools = Self.dashboardRuntimeTools()

        guard let releaseId = project.activeReleaseId else {
            return ProjectCatalogResponse(
                release_id: nil,
                release_status: nil,
                mcp_url: mcpUrl,
                mcp_oauth_enabled: AppEnvironment.mcpOAuthEnabled,
                catalog_markdown: catalogMarkdown,
                catalog_markdown_generated: catalogGenerated,
                catalog_markdown_override: project.mcpCatalogMarkdownOverride,
                tools: syntheticTools,
                resources: [],
                prompts: []
            )
        }

        let release = try await Release.find(releaseId, on: req.db)
        let compiledIds = try await MCPCatalogService.readyCompiledSkillIds(releaseId: releaseId, db: req.db)

        let toolCaps = try await MCPCatalogService.capabilityDefs(
            compiledSkillIds: compiledIds,
            types: ["tool"],
            db: req.db
        )
        let resourceCaps = try await MCPCatalogService.capabilityDefs(
            compiledSkillIds: compiledIds,
            types: ["resource"],
            db: req.db
        )
        let promptCaps = try await MCPCatalogService.capabilityDefs(
            compiledSkillIds: compiledIds,
            types: ["prompt"],
            db: req.db
        )

        let skillTools = toolCaps.map { cap in
            ProjectCatalogTool(
                name: cap.capabilityName,
                description: cap.compiledSkill.summary,
                input_schema_json: cap.schemaJson
            )
        }

        let resources: [ProjectCatalogResource] = resourceCaps.compactMap { cap in
            guard let meta = CapabilitySchemaBuilder.parseResourceMeta(cap.schemaJson) else { return nil }
            return ProjectCatalogResource(
                uri: meta.uri,
                name: cap.compiledSkill.name,
                description: cap.compiledSkill.summary,
                mime_type: meta.mimeType,
                use_when: meta.useWhen,
                avoid_when: meta.avoidWhen,
                failure_modes: meta.failureModes,
                invoke_first: meta.invokeFirst
            )
        }

        let prompts = promptCaps.map { cap in
            ProjectCatalogPrompt(
                name: cap.capabilityName,
                description: cap.compiledSkill.summary
            )
        }

        return ProjectCatalogResponse(
            release_id: releaseId.uuidString,
            release_status: release?.status,
            mcp_url: mcpUrl,
            mcp_oauth_enabled: AppEnvironment.mcpOAuthEnabled,
            catalog_markdown: catalogMarkdown,
            catalog_markdown_generated: catalogGenerated,
            catalog_markdown_override: project.mcpCatalogMarkdownOverride,
            tools: syntheticTools + skillTools,
            resources: resources,
            prompts: prompts
        )
    }

    /// Set or clear custom markdown for MCP tool `mycontext_catalog` (empty / whitespace clears).
    static func updateCatalogMarkdown(req: Request) async throws -> ProjectCatalogMarkdownUpdateResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let body = try req.content.decode(ProjectCatalogMarkdownPatch.self)
        let trimmed = body.markdown.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty {
            project.mcpCatalogMarkdownOverride = nil
        } else {
            guard trimmed.count <= McpCatalogMarkdown.catalogOverrideMaxCharacterCount else {
                throw Abort(.badRequest, reason: "Catalog markdown must be 512KB or smaller")
            }
            project.mcpCatalogMarkdownOverride = trimmed
        }
        try await project.save(on: req.db)
        if let pid = project.id {
            req.application.mcpCatalogNotifications.bumpCatalog(for: pid)
        }
        let catalogGenerated = try await McpCatalogMarkdown.buildGenerated(db: req.db, projectId: project.id!)
        let catalogMarkdown = try await McpCatalogMarkdown.build(db: req.db, projectId: project.id!)
        return ProjectCatalogMarkdownUpdateResponse(
            catalog_markdown: catalogMarkdown,
            catalog_markdown_generated: catalogGenerated,
            catalog_markdown_override: project.mcpCatalogMarkdownOverride
        )
    }

    /// Mirrors MCP `tools/list` synthetic row for `mycontext_catalog`.
    private static func dashboardSyntheticCatalogTool() -> ProjectCatalogTool {
        let schemaJson = CapabilitySchemaBuilder.toolInputSchemaJson(
            description: "Returns a markdown overview of tools, resources, and prompts for this project.",
            summary: nil
        )
        return ProjectCatalogTool(
            name: MCPConstants.catalogToolName,
            description: "Overview of this project’s MCP catalog—call first when unsure which skill to use.",
            input_schema_json: schemaJson
        )
    }

    private static func dashboardRuntimeTools() -> [ProjectCatalogTool] {
        let catalog = dashboardSyntheticCatalogTool()
        let runtime = MCPConstants.runtimeToolNames.map { name in
            ProjectCatalogTool(
                name: name,
                description: "Portable skill runtime operation: \(name.replacingOccurrences(of: "_", with: " ")).",
                input_schema_json: CapabilitySchemaBuilder.runtimeToolInputSchemaJson(name: name)
            )
        }
        return [catalog] + runtime
    }

    static func create(req: Request) async throws -> ProjectResponse {
        let account = try requireAccount(req)
        struct CreateBody: Content {
            let name: String
            let slug: String
        }
        let body = try req.content.decode(CreateBody.self)
        let name = body.name.trimmingCharacters(in: .whitespacesAndNewlines)
        let slug = body.slug.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else { throw Abort(.badRequest, reason: "Name is required") }
        guard isValidSlug(slug) else {
            throw Abort(.badRequest, reason: "Invalid slug (lowercase letters, numbers, hyphens only)")
        }
        if try await Project.query(on: req.db)
            .filter(\.$account.$id == account.id!)
            .filter(\.$slug == slug)
            .first() != nil {
            throw Abort(.conflict, reason: "Slug already in use for this account")
        }

        let subdomain = try await allocateSubdomain(db: req.db)
        let project = Project(
            accountId: account.id!,
            name: name,
            slug: slug,
            subdomain: subdomain
        )
        try await project.save(on: req.db)
        return projectResponse(project)
    }

    /// Retained for older clients; all projects remain active.
    static func setActive(req: Request) async throws -> ProjectResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        try await ProjectEntitlementReconciler.reconcileProjects(for: account, db: req.db, logger: req.logger)
        project.suspendedAt = nil
        return projectResponse(project)
    }

    static func getRepoConnection(req: Request) async throws -> RepoConnectionResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let connections = try await project.$repoConnections.get(on: req.db)
        guard let conn = connections.first else {
            throw Abort(.notFound, reason: "No repo connected")
        }
        return RepoConnectionResponse(
            project_id: project.id!.uuidString,
            provider: conn.provider,
            repo_owner: conn.repoOwner,
            repo_name: conn.repoName,
            default_branch: conn.defaultBranch,
            auth_type: conn.authType,
            webhook_id: conn.webhookId,
            github_installation_configured: conn.githubInstallationId != nil
        )
    }

    static func connectRepo(req: Request) async throws -> Response {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        struct ConnectBody: Content {
            let owner: String
            let repo: String
            let branch: String?
        }
        let body = try req.content.decode(ConnectBody.self)
        let branch = body.branch ?? "main"
        guard RepoFetcher.isValidGitHubOwnerOrRepo(body.owner),
              RepoFetcher.isValidGitHubOwnerOrRepo(body.repo),
              RepoFetcher.isValidGitHubRef(branch) else {
            throw Abort(.badRequest, reason: "Invalid GitHub repository owner, name, or branch")
        }

        guard let encrypted = account.githubTokenEncrypted,
              let token = try? TokenEncryption.decrypt(encrypted), !token.isEmpty else {
            throw Abort(.badRequest, reason: "No GitHub token. Re-authorize with repo scope.")
        }

        var pendingInstallation: Int64?
        if let ppid = req.session.data["github_app_pending_installation_project_id"],
           let pid = UUID(uuidString: ppid), pid == project.id!,
           let iidStr = req.session.data["github_app_pending_installation_id"],
           let iid = Int64(iidStr) {
            pendingInstallation = iid
            req.session.data["github_app_pending_installation_project_id"] = nil
            req.session.data["github_app_pending_installation_id"] = nil
        }

        let existing = try await project.$repoConnections.get(on: req.db)
        let existingFirst = existing.first
        let effectiveInstallationId: Int64? =
            pendingInstallation ?? existingFirst?.githubInstallationId ?? account.githubAppInstallationId

        // Require GitHub App installation (when app + webhook env are configured) before we can use installation tokens.
        if let wb = Environment.get("WEBHOOK_BASE_URL"), !wb.isEmpty,
           Self.isGitHubAppConfiguredForWebhooks(),
           effectiveInstallationId == nil {
            let returnTo = try Self.frontendProjectPageUrl(projectId: project.id!)
            let installUrl = try Self.buildGitHubAppInstallUrl(
                projectId: project.id!,
                owner: body.owner,
                repo: body.repo,
                returnTo: returnTo
            )
            let payload = GitHubAppInstallRequiredResponse(
                reason: "github_app_install_required",
                install_url: installUrl
            )
            let response = try await payload.encodeResponse(for: req)
            response.status = .conflict
            response.headers.replaceOrAdd(name: .contentType, value: "application/json; charset=utf-8")
            return response
        }

        if let first = existingFirst, let oldWebhookId = first.webhookId {
            let deleteToken = try await GitHubAppInstallationTokenService.bearerTokenForGitHubREST(
                installationId: first.githubInstallationId,
                oauthToken: token,
                client: req.client,
                logger: req.logger,
                db: req.db
            )
            try? await GitHubWebhookService.deleteWebhook(
                owner: first.repoOwner,
                repo: first.repoName,
                webhookId: oldWebhookId,
                token: deleteToken,
                client: req.client
            )
        }

        let verifyToken = try await GitHubAppInstallationTokenService.bearerTokenForGitHubREST(
            installationId: effectiveInstallationId,
            oauthToken: token,
            client: req.client,
            logger: req.logger,
            db: req.db
        )
        let userFallbackForVerify: String? = effectiveInstallationId != nil ? token : nil
        try await GitHubWebhookService.verifyRepoAccess(
            owner: body.owner,
            repo: body.repo,
            primaryToken: verifyToken,
            userFallbackToken: userFallbackForVerify,
            client: req.client,
            logger: req.logger
        )

        var webhookId: String?
        var webhookSecret: String?
        do {
            guard let baseURL = Environment.get("WEBHOOK_BASE_URL"), !baseURL.isEmpty else {
                throw Abort(.internalServerError, reason: "WEBHOOK_BASE_URL not configured")
            }
            let hookToken = try await GitHubAppInstallationTokenService.bearerTokenForGitHubREST(
                installationId: effectiveInstallationId,
                oauthToken: token,
                client: req.client,
                logger: req.logger,
                db: req.db
            )
            let created = try await GitHubWebhookService.createWebhook(
                owner: body.owner,
                repo: body.repo,
                token: hookToken,
                baseURL: baseURL,
                client: req.client
            )
            webhookId = created.webhookId
            webhookSecret = created.webhookSecret
        }

        let conn: RepoConnection
        if let first = existingFirst {
            first.repoOwner = body.owner
            first.repoName = body.repo
            first.defaultBranch = branch
            first.provider = "github"
            first.authType = "oauth"
            first.webhookId = webhookId
            first.webhookSecret = webhookSecret
            first.githubInstallationId = effectiveInstallationId
            try await first.save(on: req.db)
            conn = first
        } else {
            conn = RepoConnection(
                projectId: project.id!,
                provider: "github",
                repoOwner: body.owner,
                repoName: body.repo,
                defaultBranch: branch,
                authType: "oauth",
                webhookId: webhookId,
                webhookSecret: webhookSecret,
                githubInstallationId: effectiveInstallationId
            )
            try await conn.save(on: req.db)
        }
        let repoPayload = RepoConnectionResponse(
            project_id: project.id!.uuidString,
            provider: conn.provider,
            repo_owner: conn.repoOwner,
            repo_name: conn.repoName,
            default_branch: conn.defaultBranch,
            auth_type: conn.authType,
            webhook_id: conn.webhookId,
            github_installation_configured: conn.githubInstallationId != nil
        )
        return try await repoPayload.encodeResponse(for: req)
    }

    static func sync(req: Request) async throws -> Response {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        if !AppEnvironment.nonProductionBypassesActive {
            let cfg = syncRateConfig()
            let allowed = req.application.syncRateLimiter.allow(
                accountId: account.id!,
                projectId: project.id!,
                maxRequests: cfg.max,
                windowSeconds: cfg.windowSeconds
            )
            guard allowed else {
                var headers = HTTPHeaders()
                headers.replaceOrAdd(name: .retryAfter, value: String(Int(ceil(cfg.windowSeconds))))
                return Response(status: .tooManyRequests, headers: headers)
            }
        }
        let pipeline = SyncPipeline(db: req.db, app: req.application)
        try await pipeline.run(projectId: project.id!)
        return Response(status: .noContent)
    }

    static func getCustomDomain(req: Request) async throws -> CustomDomainResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let verified = project.customDomainVerifiedAt != nil
        let token = verified ? nil : project.customDomainVerificationToken
        let platform = await customDomainPlatformStatus(project: project, req: req)
        let verificationRecordName = project.customDomain.flatMap { host in
            token == nil ? nil : customDomainVerificationRecordName(hostname: host)
        }
        let instructions: String?
        if let host = project.customDomain, !host.isEmpty, let t = token, !t.isEmpty {
            var parts = ["Add a TXT record on \(customDomainVerificationRecordName(hostname: host)) with value: \(t)"]
            parts.append(contentsOf: dnsInstructions(platform?.dnsRecords ?? []))
            instructions = parts.joined(separator: "\n")
        } else if verified, platform?.status != .issued {
            var parts: [String] = []
            if let message = platform?.message, !message.isEmpty {
                parts.append(message)
            }
            parts.append(contentsOf: dnsInstructions(platform?.dnsRecords ?? []))
            instructions = parts.isEmpty ? nil : parts.joined(separator: "\n")
        } else {
            instructions = nil
        }
        return CustomDomainResponse(
            hostname: project.customDomain,
            verified: verified,
            verification_token: token,
            verification_record_name: verificationRecordName,
            instructions: instructions,
            ownership_verification_record_name: verificationRecordName,
            ownership_verification_record_value: token,
            fly_ownership_verification_record_name: nil,
            fly_ownership_verification_record_value: nil,
            fly_a_record_values: nil,
            fly_aaaa_record_values: nil,
            fly_cname_record_value: nil,
            platform_dns_records: platform?.dnsRecords.map(CustomDomainDNSRecordResponse.init),
            certificate_status: platform?.status.rawValue,
            certificate_message: platform?.message
        )
    }

    static func setCustomDomain(req: Request) async throws -> CustomDomainResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        struct Body: Content { let hostname: String }
        let body = try req.content.decode(Body.self)
        guard let raw = McpUrlBuilder.canonicalCustomDomainHost(body.hostname),
              raw.count <= 253 else {
            throw Abort(.badRequest, reason: "Invalid hostname")
        }
        guard raw.allSatisfy({ $0.isLetter || $0.isNumber || $0 == "." || $0 == "-" }) else {
            throw Abort(.badRequest, reason: "Invalid hostname characters")
        }
        let others = try await Project.query(on: req.db).filter(\.$customDomain == raw).all()
        if let conflict = others.first(where: { $0.id != project.id }) {
            _ = conflict
            throw Abort(.conflict, reason: "Domain is already registered to another project")
        }
        let platform = await RailwayDomainService.ensureDomain(
            hostname: raw,
            client: req.client,
            logger: req.logger
        )
        if platform.status == .failed || (platform.status == .notConfigured && AppEnvironment.deployKind() == .prod) {
            throw Abort(.serviceUnavailable, reason: platform.message ?? "Railway domain provisioning is unavailable")
        }
        let token = UUID().uuidString.replacingOccurrences(of: "-", with: "")
        project.customDomain = raw
        project.customDomainVerificationToken = token
        project.customDomainVerifiedAt = nil
        try await project.save(on: req.db)
        return try await getCustomDomain(req: req)
    }

    static func verifyCustomDomain(req: Request) async throws -> CustomDomainResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        guard let host = project.customDomain, !host.isEmpty else {
            throw Abort(.badRequest, reason: "Set a custom domain first")
        }
        if project.customDomainVerifiedAt == nil {
            guard let token = project.customDomainVerificationToken, !token.isEmpty else {
                throw Abort(.badRequest, reason: "Set a custom domain first")
            }
            let txtOkAtVerificationName = try await DnsTxtVerifier.txtRecordsIncludeToken(
                hostname: customDomainVerificationRecordName(hostname: host),
                token: token,
                client: req.client
            )
            let txtOk: Bool
            if txtOkAtVerificationName {
                txtOk = true
            } else {
                txtOk = try await DnsTxtVerifier.txtRecordsIncludeToken(hostname: host, token: token, client: req.client)
            }
            guard txtOk else {
                throw Abort(.badRequest, reason: "TXT record not found or token mismatch. Add a TXT record on \(customDomainVerificationRecordName(hostname: host)) with the shown token and try again.")
            }
        }
        let platform = await RailwayDomainService.ensureDomain(hostname: host, client: req.client, logger: req.logger)
        guard platform.status != .notConfigured else {
            throw Abort(.serviceUnavailable, reason: platform.message ?? "Railway domain provisioning is not configured")
        }
        guard platform.status != .failed else {
            throw Abort(.badGateway, reason: platform.message ?? "Railway domain provisioning failed")
        }
        guard platform.ownershipVerified, platform.routingReady else {
            let setup = dnsInstructions(platform.dnsRecords).joined(separator: " ")
            throw Abort(.badRequest, reason: setup.isEmpty ? "Railway is still waiting for required DNS records." : setup)
        }
        if project.customDomainVerifiedAt == nil || project.customDomainVerificationToken != nil {
            project.customDomainVerifiedAt = project.customDomainVerifiedAt ?? Date()
            project.customDomainVerificationToken = nil
            try await project.save(on: req.db)
        }
        var instructions: [String] = []
        if platform.status != .issued {
            if let message = platform.message, !message.isEmpty {
                instructions.append(message)
            }
            instructions.append(contentsOf: dnsInstructions(platform.dnsRecords))
        }
        return CustomDomainResponse(
            hostname: project.customDomain,
            verified: true,
            verification_token: nil,
            verification_record_name: nil,
            instructions: instructions.isEmpty ? nil : instructions.joined(separator: "\n"),
            ownership_verification_record_name: nil,
            ownership_verification_record_value: nil,
            fly_ownership_verification_record_name: nil,
            fly_ownership_verification_record_value: nil,
            fly_a_record_values: nil,
            fly_aaaa_record_values: nil,
            fly_cname_record_value: nil,
            platform_dns_records: platform.dnsRecords.map(CustomDomainDNSRecordResponse.init),
            certificate_status: platform.status.rawValue,
            certificate_message: platform.message
        )
    }

    private static func customDomainPlatformStatus(
        project: Project,
        req: Request
    ) async -> RailwayDomainService.Result? {
        guard let host = project.customDomain,
              !host.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return nil
        }
        return await RailwayDomainService.checkDomainStatus(
            hostname: host,
            client: req.client,
            logger: req.logger
        )
    }

    private static func customDomainVerificationRecordName(hostname: String) -> String {
        "_mcp-verify.\(hostname.trimmingCharacters(in: .whitespacesAndNewlines).trimmingCharacters(in: CharacterSet(charactersIn: ".")).lowercased())"
    }

    private static func dnsInstructions(_ records: [RailwayDomainService.DNSRecord]) -> [String] {
        records.map { record in
            "Add a \(record.type) record on \(record.name) with value: \(record.value)"
        }
    }

    static func listReleases(req: Request) async throws -> [ReleaseResponse] {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let releases = try await Release.query(on: req.db)
            .filter(\.$project.$id == project.id!)
            .sort(\.$createdAt, .descending)
            .all()
        let activeId = project.activeReleaseId
        let releaseIds = releases.compactMap(\.id)
        let mcpCounts: [UUID: (blocking: Int, warning: Int)]
        if releaseIds.isEmpty {
            mcpCounts = [:]
        } else {
            let compiled = try await CompiledSkill.query(on: req.db)
                .filter(\.$release.$id ~~ releaseIds)
                .all()
            let skillIds = compiled.compactMap(\.id)
            var schemaBySkillId: [UUID: String] = [:]
            var ruleBySkillId: [UUID: RoutingRule] = [:]
            if !skillIds.isEmpty {
                let caps = try await CapabilityDef.query(on: req.db)
                    .filter(\.$compiledSkill.$id ~~ skillIds)
                    .all()
                for cap in caps {
                    let sid = cap.$compiledSkill.id
                    if schemaBySkillId[sid] == nil, let sj = cap.schemaJson, !sj.isEmpty {
                        schemaBySkillId[sid] = sj
                    }
                }
                let rules = try await RoutingRule.query(on: req.db)
                    .filter(\.$compiledSkill.$id ~~ skillIds)
                    .all()
                for rule in rules {
                    ruleBySkillId[rule.$compiledSkill.id] = rule
                }
            }
            mcpCounts = McpMetadataHealth.blockingAndWarningCountsByRelease(
                releaseIds: releaseIds,
                compiledSkills: compiled,
                schemaBySkillId: schemaBySkillId,
                ruleBySkillId: ruleBySkillId
            )
        }
        return releases.map { r in
            let rid = r.id!
            let pair = mcpCounts[rid] ?? (0, 0)
            return ReleaseResponse(
                id: rid.uuidString,
                project_id: project.id!.uuidString,
                commit_sha: r.commitSha,
                status: r.status,
                created_at: formatDate(r.createdAt),
                error_summary: r.errorSummary,
                is_active: activeId == r.id,
                skill_body_changes_count: r.skillBodyChangesCount,
                mcp_metadata_blocking_skills: pair.blocking,
                mcp_metadata_warning_skills: pair.warning
            )
        }
    }

    static func activateRelease(req: Request) async throws -> Response {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        guard let releaseId = req.parameters.get("releaseId", as: UUID.self) else {
            throw Abort(.badRequest, reason: "Invalid release ID")
        }
        let release = try await Release.query(on: req.db)
            .filter(\.$id == releaseId)
            .filter(\.$project.$id == project.id!)
            .first()
        guard let release = release else {
            throw Abort(.notFound, reason: "Release not found")
        }
        guard release.status == "ready" else {
            throw Abort(.badRequest, reason: "Release must be ready before activation")
        }
        let compiledSkills = try await CompiledSkill.query(on: req.db)
            .filter(\.$release.$id == releaseId)
            .all()
        let allReady = compiledSkills.allSatisfy { $0.status == "ready" }
        guard allReady else {
            throw Abort(.badRequest, reason: "All compiled skills must be ready before activation")
        }
        project.activeReleaseId = releaseId
        try await project.save(on: req.db)
        if let pid = project.id {
            req.application.mcpCatalogNotifications.bumpCatalog(for: pid)
        }
        return Response(status: .noContent)
    }

    static func listCompiledSkills(req: Request) async throws -> [CompiledSkillResponse] {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        guard let releaseId = req.parameters.get("releaseId", as: UUID.self) else {
            throw Abort(.badRequest, reason: "Invalid release ID")
        }
        let release = try await Release.query(on: req.db)
            .filter(\.$id == releaseId)
            .filter(\.$project.$id == project.id!)
            .first()
        guard release != nil else {
            throw Abort(.notFound, reason: "Release not found")
        }
        let compiled = try await CompiledSkill.query(on: req.db)
            .filter(\.$release.$id == releaseId)
            .all()
        let skillIds = compiled.compactMap(\.id)
        var schemaBySkillId: [UUID: String] = [:]
        if !skillIds.isEmpty {
            let caps = try await CapabilityDef.query(on: req.db)
                .filter(\.$compiledSkill.$id ~~ skillIds)
                .all()
            for cap in caps {
                let sid = cap.$compiledSkill.id
                if schemaBySkillId[sid] == nil, let sj = cap.schemaJson, !sj.isEmpty {
                    schemaBySkillId[sid] = sj
                }
            }
        }
        var ruleBySkillId: [UUID: RoutingRule] = [:]
        if !skillIds.isEmpty {
            let rules = try await RoutingRule.query(on: req.db)
                .filter(\.$compiledSkill.$id ~~ skillIds)
                .all()
            for rule in rules {
                ruleBySkillId[rule.$compiledSkill.id] = rule
            }
        }
        return compiled.map { cs in
            let sid = cs.id!
            return Self.compiledSkillResponse(
                cs,
                schemaJson: schemaBySkillId[sid],
                routingRule: ruleBySkillId[sid]
            )
        }
    }

    static func updateCompiledSkill(req: Request) async throws -> CompiledSkillResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let response = try await req.db.transaction { transaction in
            try await updateCompiledSkill(req: req, project: project, db: transaction)
        }
        req.application.mcpCatalogNotifications.bumpCatalog(for: project.id!)
        return response
    }

    private static func updateCompiledSkill(
        req: Request,
        project: Project,
        db: Database
    ) async throws -> CompiledSkillResponse {
        guard let releaseId = req.parameters.get("releaseId", as: UUID.self),
              let compiledSkillId = req.parameters.get("compiledSkillId", as: UUID.self) else {
            throw Abort(.badRequest, reason: "Invalid release or compiled skill ID")
        }
        guard let release = try await Release.query(on: db)
            .filter(\.$id == releaseId)
            .filter(\.$project.$id == project.id!)
            .first() else {
            throw Abort(.notFound, reason: "Release not found")
        }
        guard let compiled = try await CompiledSkill.query(on: db)
            .filter(\.$id == compiledSkillId)
            .filter(\.$release.$id == releaseId)
            .first() else {
            throw Abort(.notFound, reason: "Compiled skill not found")
        }
        struct UpdateBody: Content {
            let exposure_type: String?
            let risk_level: String?
            let status: String?
            let summary: String?
            let skill_body: String?
            /// Mirrors SKILL front matter routing lists / `invoke_first`; updates `routing_rules` and (for resources) default MCP metadata.
            let routing: CompiledSkillRoutingPatch?
            /// When `true`, `schema_json` is applied (empty string → rebuild default from summary/exposure). When `false` or omitted, custom JSON is preserved unless `exposure_type` or `summary` changes (those recompute defaults).
            let replace_schema: Bool?
            /// Replaces `capability_defs.schema_json` when `replace_schema` is true (must be valid JSON unless empty).
            let schema_json: String?
            let runtime: SkillRuntimeOverridePatch?
        }
        let body = try req.content.decode(UpdateBody.self)
        let hadBodyDiff = compiled.bodyDiffUnified != nil
        let skillBodyBefore = compiled.skillBody
        let exposureBefore = compiled.exposureType
        let summaryBefore = compiled.summary
        if let et = body.exposure_type, ["tool", "resource", "prompt"].contains(et) {
            compiled.exposureType = et
        }
        if let rl = body.risk_level, ["low", "medium", "high"].contains(rl) {
            compiled.riskLevel = rl
        }
        if let summary = body.summary {
            let t = summary.trimmingCharacters(in: .whitespacesAndNewlines)
            compiled.summary = t.isEmpty ? nil : t
        }
        if let rawBody = body.skill_body {
            let t = rawBody.trimmingCharacters(in: .whitespacesAndNewlines)
            compiled.skillBody = t.isEmpty ? nil : rawBody
        }
        let bodyTextChanged = body.skill_body != nil && compiled.skillBody != skillBodyBefore
        if bodyTextChanged {
            compiled.bodyDiffUnified = nil
            compiled.bodyDiffPriorReleaseId = nil
        }
        if let runtime = body.runtime,
           var document = SkillRuntimeJSON.decode(CompiledSkillDocument.self, from: compiled.canonicalJson) {
            if let value = runtime.kind { document.kind = value }
            if let value = runtime.scope { document.scope = value }
            if let value = runtime.activation { document.activation = value }
            if let value = runtime.enforcement { document.enforcement = value }
            if let value = runtime.priority { document.priority = min(100, max(0, value)) }
            if let value = runtime.requires { document.requires = value }
            if let value = runtime.conflictsWith { document.conflictsWith = value }
            if let value = runtime.version { document.version = value }
            if let value = runtime.lifecycle { document.lifecycle = value }
            var remaining = Set(document.validation.missingFields)
            if runtime.kind != nil { remaining.remove("kind") }
            if runtime.scope != nil { remaining.remove("scope") }
            if runtime.activation != nil { remaining.remove("activation") }
            if runtime.enforcement != nil { remaining.remove("enforcement") }
            if runtime.version != nil { remaining.remove("version") }
            document.validation.missingFields = remaining.sorted()
            document.validation.clarificationRequired = !remaining.isEmpty
            compiled.kind = document.kind.rawValue; compiled.scope = document.scope.rawValue
            compiled.activationMode = document.activation.mode.rawValue; compiled.enforcement = document.enforcement.rawValue
            compiled.priority = document.priority; compiled.version = document.version
            compiled.canonicalJson = SkillRuntimeJSON.encode(document)
            compiled.clarificationRequired = document.validation.clarificationRequired
            compiled.clarificationJson = SkillRuntimeJSON.encode(SkillCanonicalCompiler.questionsForRuntime(fields: remaining.sorted()))

            let priorOverrides = try await SkillRuntimeOverride.query(on: db)
                .filter(\.$project.$id == project.id!)
                .filter(\.$skillId == document.id)
                .all()
            let existingOverride = priorOverrides.first { $0.scope == document.scope.rawValue }
            for stale in priorOverrides where stale.id != existingOverride?.id {
                try await stale.delete(on: db)
            }
            let overrideRow = existingOverride ?? SkillRuntimeOverride()
            if existingOverride == nil { overrideRow.$project.id = project.id!; overrideRow.skillId = document.id; overrideRow.scope = document.scope.rawValue }
            overrideRow.metadataJson = SkillRuntimeJSON.encode(runtime)
            overrideRow.sourceChecksum = document.source.checksum
            overrideRow.baseChecksum = document.source.checksum
            overrideRow.isStale = false
            try await overrideRow.save(on: db)
        }
        try await compiled.save(on: db)
        if bodyTextChanged, hadBodyDiff {
            release.skillBodyChangesCount = max(0, release.skillBodyChangesCount - 1)
            try await release.save(on: db)
        }
        if let routing = body.routing {
            try await Self.applyRoutingPatch(compiledSkillId: compiled.id!, patch: routing, db: db)
        }
        let exposureChanged = compiled.exposureType != exposureBefore
        let summaryChanged = compiled.summary != summaryBefore
        let routingChanged = body.routing != nil
        let routingRule = try await RoutingRule.query(on: db)
            .filter(\.$compiledSkill.$id == compiled.id!)
            .first()
        let routingHints = RoutingHints.from(rule: routingRule)
        let caps = try await CapabilityDef.query(on: db)
            .filter(\.$compiledSkill.$id == compiled.id!)
            .all()
        let capType = compiled.exposureType == "guidance" ? "prompt" : compiled.exposureType
        let existingSchema = caps.first?.schemaJson
        let newSchema: String?
        if body.replace_schema == true, let raw = body.schema_json {
            let t = raw.trimmingCharacters(in: .whitespacesAndNewlines)
            if t.isEmpty {
                newSchema = Compiler.schemaJson(forCapabilityType: capType, compiled: compiled, routingHints: routingHints)
            } else {
                guard (try? JSONSerialization.jsonObject(with: Data(t.utf8))) != nil else {
                    throw Abort(.badRequest, reason: "schema_json must be valid JSON")
                }
                newSchema = t
            }
        } else if exposureChanged || summaryChanged || (routingChanged && capType == "resource") {
            newSchema = Compiler.schemaJson(forCapabilityType: capType, compiled: compiled, routingHints: routingHints)
        } else {
            newSchema = existingSchema ?? Compiler.schemaJson(forCapabilityType: capType, compiled: compiled, routingHints: routingHints)
        }
        for cap in caps {
            cap.type = capType
            cap.schemaJson = newSchema
            try await cap.save(on: db)
        }

        let routingHintsAfter = RoutingHints.from(rule: routingRule)
        let metadataTier = McpMetadataHealth.metadataOnlyTier(
            exposureType: compiled.exposureType,
            yamlFrontmatterPresent: compiled.yamlFrontmatterPresent,
            skillBody: compiled.skillBody,
            schemaJson: newSchema,
            routing: routingHintsAfter
        )
        let hasDescription: Bool = {
            guard let s = compiled.summary else { return false }
            return !s.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }()
        let inferredStatus = SkillInference.inferPublishabilityStatus(
            exposureType: compiled.exposureType,
            riskLevel: compiled.riskLevel,
            hasDescription: hasDescription
        )
        let autoStatus = McpMetadataHealth.resolvedPublishStatus(
            inferred: inferredStatus,
            metadataTier: metadataTier
        )
        switch metadataTier {
        case .blocking, .warning:
            compiled.status = autoStatus
        case .ok:
            if let st = body.status, ["ready", "needs_review", "not_publishable"].contains(st) {
                compiled.status = st
            } else {
                compiled.status = autoStatus
            }
        }
        try await compiled.save(on: db)

        return Self.compiledSkillResponse(compiled, schemaJson: newSchema, routingRule: routingRule)
    }

    static func writeBackCompiledSkillMetadata(req: Request) async throws -> SkillMetadataWritebackService.Result {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        guard let releaseId = req.parameters.get("releaseId", as: UUID.self),
              let compiledSkillId = req.parameters.get("compiledSkillId", as: UUID.self),
              let compiled = try await CompiledSkill.query(on: req.db).filter(\.$id == compiledSkillId)
                .filter(\.$release.$id == releaseId).first() else { throw Abort(.notFound, reason: "Compiled skill not found") }
        return try await SkillMetadataWritebackService.createDraftPullRequest(compiled: compiled, project: project, app: req.application, db: req.db)
    }

    struct RuntimeSettingsResponse: Content {
        let telemetry_enabled: Bool
        let telemetry_retention_days: Int
        let semantic_enabled: Bool
        let embedding_provider: String?
        let embedding_model: String?
        let feedback_issue_creation_enabled: Bool
        let provider_preferences_json: String?
        let assignments: [RuntimeAssignmentResponse]
        let recent_events: [RuntimeEventResponse]
    }

    /// Browser shape for a saved assignment. Encoding the Fluent model directly leaks camelCase
    /// property names, which the dashboard cannot read or resubmit.
    struct RuntimeAssignmentResponse: Content, Equatable {
        let id: String?
        let skill_id: String
        let scope: String
        let activation_mode: String
        let target_type: String
        let target_id: String
        let required: Bool
        let priority: Int

        init(_ assignment: SkillAssignment) {
            id = assignment.id?.uuidString
            skill_id = assignment.skillId
            scope = assignment.scope
            activation_mode = assignment.activationMode
            target_type = assignment.targetType
            target_id = assignment.targetId
            required = assignment.required
            priority = assignment.priority
        }
    }

    /// Browser shape for a diagnostic runtime event. Omits client identity, request hashes,
    /// detail JSON, and release attribution, which stay server-side.
    struct RuntimeEventResponse: Content, Equatable {
        let id: String?
        let trace_id: String
        let event_type: String
        let skill_id: String?
        let reason_code: String?
        let score: Double?
        let created_at: String?

        init(_ event: SkillRuntimeEvent) {
            id = event.id?.uuidString
            trace_id = event.traceId.uuidString
            event_type = event.eventType
            skill_id = event.skillId
            reason_code = event.reasonCode
            score = event.score
            created_at = event.createdAt.map { ProjectController.formatDate($0) }
        }
    }

    static func runtimeSettings(req: Request) async throws -> RuntimeSettingsResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let settings = try await runtimeSettingsRow(projectId: project.id!, db: req.db)
        async let assignments = SkillAssignment.query(on: req.db).filter(\.$project.$id == project.id!).sort(\.$priority, .descending).all()
        async let events = recentRuntimeEvents(projectId: project.id!, retentionDays: settings.telemetryRetentionDays, db: req.db)
        return try await runtimeSettingsResponse(settings, assignments: assignments, events: events)
    }

    static func updateRuntimeSettings(req: Request) async throws -> RuntimeSettingsResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        struct AssignmentPatch: Content {
            let skill_id: String
            let scope: String
            let activation_mode: String
            let required: Bool
            let priority: Int
            let target_type: String?
            let target_id: String?
        }
        struct Body: Content {
            let telemetry_enabled: Bool?
            let telemetry_retention_days: Int?
            let semantic_enabled: Bool?
            let embedding_provider: String?
            let embedding_model: String?
            let feedback_issue_creation_enabled: Bool?
            let provider_preferences_json: String?
            let assignments: [AssignmentPatch]?
        }
        let body = try req.content.decode(Body.self)
        let normalizedAssignments: [(skillId: String, scope: String, activation: String, required: Bool, priority: Int, targetType: String, targetId: String)]?
        if let patches = body.assignments {
            let activeSkillIds: Set<String>
            if let releaseId = project.activeReleaseId {
                activeSkillIds = Set(try await CompiledSkill.query(on: req.db)
                    .filter(\.$release.$id == releaseId)
                    .filter(\.$status == "ready")
                    .all()
                    .compactMap { $0.skillId ?? $0.name })
            } else {
                activeSkillIds = []
            }
            var uniqueness = Set<String>()
            normalizedAssignments = try patches.map { patch in
                let skillId = patch.skill_id.trimmingCharacters(in: .whitespacesAndNewlines)
                guard !skillId.isEmpty, skillId.count <= 128, activeSkillIds.contains(skillId) else {
                    throw Abort(.badRequest, reason: "Assignments must reference a ready skill in the active release")
                }
                guard SkillScope(rawValue: patch.scope) != nil,
                      SkillActivationMode(rawValue: patch.activation_mode) != nil else {
                    throw Abort(.badRequest, reason: "Invalid assignment scope or activation mode")
                }
                guard (0...100).contains(patch.priority) else {
                    throw Abort(.badRequest, reason: "Assignment priority must be between 0 and 100")
                }
                guard let targetType = patch.target_type?.trimmingCharacters(in: .whitespacesAndNewlines),
                      let targetId = patch.target_id?.trimmingCharacters(in: .whitespacesAndNewlines),
                      !targetType.isEmpty, !targetId.isEmpty, targetId.count <= 512,
                      targetType == patch.scope else {
                    throw Abort(.badRequest, reason: "Assignment target type must match its scope and include an exact target identity")
                }
                if targetType == SkillScope.global.rawValue, targetId != "*", targetId != "global" {
                    throw Abort(.badRequest, reason: "Global assignments must target `*` or `global`")
                }
                let identity = "\(skillId)\u{1f}\(patch.scope)\u{1f}\(targetType)\u{1f}\(targetId)"
                guard uniqueness.insert(identity).inserted else {
                    throw Abort(.badRequest, reason: "Duplicate assignment for skill, scope, and target identity")
                }
                return (skillId, patch.scope, patch.activation_mode, patch.required, patch.priority, targetType, targetId)
            }
        } else {
            normalizedAssignments = nil
        }
        let settings = try await runtimeSettingsRow(projectId: project.id!, db: req.db)
        if let value = body.telemetry_enabled { settings.telemetryEnabled = value }
        if let value = body.telemetry_retention_days { settings.telemetryRetentionDays = min(365, max(1, value)) }
        // Embedding controls are retained in the wire response for compatibility, but resolution
        // is deterministic and never reads or writes embeddings.
        settings.semanticEnabled = false
        settings.embeddingProvider = nil
        settings.embeddingModel = nil
        if let value = body.feedback_issue_creation_enabled { settings.feedbackIssueCreationEnabled = value }
        if let json = body.provider_preferences_json {
            guard json.isEmpty || (try? JSONSerialization.jsonObject(with: Data(json.utf8))) != nil else { throw Abort(.badRequest, reason: "provider_preferences_json must be valid JSON") }
            settings.providerPreferencesJson = json.isEmpty ? nil : json
        }
        try await req.db.transaction { transaction in
            try await settings.save(on: transaction)
            if let patches = normalizedAssignments {
                try await SkillAssignment.query(on: transaction).filter(\.$project.$id == project.id!).delete()
                for patch in patches {
                    let row = SkillAssignment()
                    row.$project.id = project.id!
                    row.skillId = patch.skillId
                    row.scope = patch.scope
                    row.activationMode = patch.activation
                    row.required = patch.required
                    row.priority = patch.priority
                    row.targetType = patch.targetType
                    row.targetId = patch.targetId
                    try await row.save(on: transaction)
                }
            }
        }
        let assignments = try await SkillAssignment.query(on: req.db).filter(\.$project.$id == project.id!).sort(\.$priority, .descending).all()
        let events = try await recentRuntimeEvents(projectId: project.id!, retentionDays: settings.telemetryRetentionDays, db: req.db)
        req.application.mcpCatalogNotifications.bumpCatalog(for: project.id!)
        return runtimeSettingsResponse(settings, assignments: assignments, events: events)
    }

    /// Enforce retention before the background cleanup runs, including when collection is disabled.
    static func recentRuntimeEvents(projectId: UUID, retentionDays: Int, db: Database, now: Date = Date()) async throws -> [SkillRuntimeEvent] {
        let cutoff = now.addingTimeInterval(-Double(max(1, retentionDays)) * 86_400)
        return try await SkillRuntimeEvent.query(on: db)
            .filter(\.$project.$id == projectId)
            .filter(\.$createdAt >= cutoff)
            .filter(\.$createdAt <= now)
            .sort(\.$createdAt, .descending).limit(100).all()
    }

    private static func runtimeSettingsRow(projectId: UUID, db: Database) async throws -> ProjectRuntimeSettings {
        if let existing = try await ProjectRuntimeSettings.query(on: db).filter(\.$project.$id == projectId).first() { return existing }
        let settings = ProjectRuntimeSettings(); settings.$project.id = projectId
        settings.telemetryEnabled = false; settings.telemetryRetentionDays = 30; settings.semanticEnabled = false
        settings.embeddingProvider = nil; settings.embeddingModel = nil
        settings.feedbackIssueCreationEnabled = false
        try await settings.save(on: db); return settings
    }

    private static func runtimeSettingsResponse(_ settings: ProjectRuntimeSettings, assignments: [SkillAssignment], events: [SkillRuntimeEvent]) -> RuntimeSettingsResponse {
        let scopedAssignments = assignments.filter { $0.targetType == $0.scope }
        return .init(telemetry_enabled: settings.telemetryEnabled, telemetry_retention_days: settings.telemetryRetentionDays,
              semantic_enabled: false, embedding_provider: nil,
              embedding_model: nil, feedback_issue_creation_enabled: settings.feedbackIssueCreationEnabled,
              provider_preferences_json: settings.providerPreferencesJson,
              assignments: scopedAssignments.map(RuntimeAssignmentResponse.init),
              recent_events: events.map(RuntimeEventResponse.init))
    }

    private static func apiKeyResponse(_ k: ApiKey, projectId: UUID) -> ApiKeyResponse {
        ApiKeyResponse(
            id: k.id!.uuidString,
            project_id: projectId.uuidString,
            name: k.name,
            key_prefix: k.keyPrefix,
            status: k.status,
            created_at: formatDate(k.createdAt),
            last_used_at: k.lastUsedAt.map { formatDate($0) }
        )
    }

    static func listApiKeys(req: Request) async throws -> [ApiKeyResponse] {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let includeRevoked = req.query[Bool.self, at: "include_revoked"] ?? false
        let keys = try await project.$apiKeys.get(on: req.db)
        let filtered =
            includeRevoked
                ? keys
                : keys.filter { $0.status == "active" }
        return filtered.map { Self.apiKeyResponse($0, projectId: project.id!) }
    }

    static func createApiKey(req: Request) async throws -> ApiKeyCreateResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let body = try? req.content.decode(ApiKeyCreateRequest.self)
        let name = try body?.normalizedName()
        let rawKey = "mcp_\(UUID().uuidString.replacingOccurrences(of: "-", with: ""))"
        let hash = SHA256.hash(data: Data(rawKey.utf8))
        let hashString = hash.map { String(format: "%02x", $0) }.joined()
        let prefix = String(rawKey.prefix(12))

        let apiKey = ApiKey(
            projectId: project.id!,
            name: name,
            keyPrefix: prefix,
            keyHash: hashString,
            status: "active"
        )
        try await apiKey.save(on: req.db)
        return ApiKeyCreateResponse(id: apiKey.id!.uuidString, key: rawKey, prefix: prefix, name: name)
    }

    static func updateApiKey(req: Request) async throws -> ApiKeyResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        guard let keyId = req.parameters.get("keyId", as: UUID.self) else {
            throw Abort(.badRequest, reason: "Invalid API key id")
        }
        guard let apiKey = try await ApiKey.find(keyId, on: req.db),
              apiKey.$project.id == project.id! else {
            throw Abort(.notFound, reason: "API key not found")
        }
        guard apiKey.status == "active" else {
            throw Abort(.conflict, reason: "API key is revoked")
        }
        let body = try req.content.decode(ApiKeyPatchRequest.self)
        let name = try body.normalizedName()
        apiKey.name = name
        try await apiKey.save(on: req.db)
        return Self.apiKeyResponse(apiKey, projectId: project.id!)
    }

    /// Soft-revoke: sets `status` to `revoked`. Idempotent when already revoked.
    static func revokeApiKey(req: Request) async throws -> Response {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        guard let keyId = req.parameters.get("keyId", as: UUID.self) else {
            throw Abort(.badRequest, reason: "Invalid API key id")
        }
        guard let apiKey = try await ApiKey.find(keyId, on: req.db),
              apiKey.$project.id == project.id! else {
            throw Abort(.notFound, reason: "API key not found")
        }
        if apiKey.status != "revoked" {
            apiKey.status = "revoked"
            try await apiKey.save(on: req.db)
        }
        return Response(status: .noContent)
    }

    static func listRequestLogs(req: Request) async throws -> [RequestLogResponse] {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let limit = min(req.query[Int.self, at: "limit"] ?? 50, 200)
        let offset = req.query[Int.self, at: "offset"] ?? 0

        let logs = try await RequestLog.query(on: req.db)
            .filter(\.$project.$id == project.id!)
            .sort(\.$timestamp, .descending)
            .limit(limit)
            .offset(offset)
            .all()

        let apiKeyIds = RequestLogClientResolver.apiKeyIds(from: logs)
        let keys: [ApiKey]
        if apiKeyIds.isEmpty {
            keys = []
        } else {
            keys = try await ApiKey.query(on: req.db)
                .filter(\.$project.$id == project.id!)
                .filter(\.$id ~~ apiKeyIds)
                .all()
        }
        let keysById = Dictionary(uniqueKeysWithValues: keys.compactMap { k -> (UUID, ApiKey)? in
            guard let id = k.id else { return nil }
            return (id, k)
        })

        return logs.map { log in
            let statusInt = Int(log.status) ?? 0
            return RequestLogResponse(
                id: log.id!.uuidString,
                project_id: project.id!.uuidString,
                release_id: log.$release.id.map { $0.uuidString },
                timestamp: formatDate(log.timestamp),
                client_id: RequestLogClientResolver.displayLabel(stored: log.clientId, keysById: keysById),
                method: log.method,
                latency_ms: log.latencyMs,
                status: statusInt,
                error_code: log.errorCode,
                error_message: log.errorMessage
            )
        }
    }

    private static func validationErrorEntry(from e: [String: Any]) -> ValidationErrorEntry? {
        guard let path = e["path"] as? String else { return nil }
        let message = (e["message"] as? String) ?? ""
        let code = e["code"] as? String
        let summary = e["summary"] as? String
        let fixHint = e["fix_hint"] as? String
        let line: Int? = {
            if let n = e["line"] as? Int { return n }
            if let n = e["line"] as? NSNumber { return n.intValue }
            return nil
        }()
        return ValidationErrorEntry(
            code: code,
            path: path,
            line: line,
            summary: summary,
            fix_hint: fixHint,
            message: message
        )
    }

    /// Structured validation errors for a release (`skill_packages` / pipeline report).
    static func releaseValidation(req: Request) async throws -> ReleaseValidationResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        guard let releaseId = req.parameters.get("releaseId", as: UUID.self) else {
            throw Abort(.badRequest, reason: "Invalid release ID")
        }
        let release = try await Release.query(on: req.db)
            .filter(\.$id == releaseId)
            .filter(\.$project.$id == project.id!)
            .first()
        guard let release = release else {
            throw Abort(.notFound, reason: "Release not found")
        }
        guard let record = try await ValidationReportRecord.query(on: req.db)
            .filter(\.$release.$id == releaseId)
            .first() else {
            if release.status == "failed" {
                let trimmed = release.errorSummary?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                let message = trimmed.isEmpty
                    ? "Release failed; no structured validation report was stored."
                    : trimmed
                return ReleaseValidationResponse(is_valid: false, errors: [
                    ValidationErrorEntry(path: "release", message: message)
                ], warnings: [])
            }
            return ReleaseValidationResponse(is_valid: true, errors: [], warnings: [])
        }
        guard let data = record.reportJson.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return ReleaseValidationResponse(is_valid: false, errors: [
                ValidationErrorEntry(path: "report", message: "Could not parse validation report JSON")
            ], warnings: [])
        }
        let isValid = (obj["is_valid"] as? Bool) ?? false
        let rawErrors = (obj["errors"] as? [[String: Any]]) ?? []
        let errors: [ValidationErrorEntry] = rawErrors.compactMap { Self.validationErrorEntry(from: $0) }
        let rawWarnings = (obj["warnings"] as? [[String: Any]]) ?? []
        let warnings: [ValidationErrorEntry] = rawWarnings.compactMap { Self.validationErrorEntry(from: $0) }
        return ReleaseValidationResponse(is_valid: isValid, errors: errors, warnings: warnings)
    }

    // MARK: - Dashboard metrics

    private static let dashboardLogSampleLimit = 10_000
    private static let dashboardCapabilityUsageSampleLimit = 50_000

    private static func countRequestLogs(
        db: Database,
        projectIds: [UUID],
        since: Date?
    ) async throws -> Int {
        guard !projectIds.isEmpty else { return 0 }
        var q = RequestLog.query(on: db).filter(\.$project.$id ~~ projectIds)
        if let since {
            q = q.filter(\.$timestamp >= since)
        }
        return try await q.count()
    }

    private static func dashboardLogSample(
        db: Database,
        projectIds: [UUID],
        since: Date,
        limit: Int
    ) async throws -> [RequestLog] {
        guard !projectIds.isEmpty else { return [] }
        return try await RequestLog.query(on: db)
            .filter(\.$project.$id ~~ projectIds)
            .filter(\.$timestamp >= since)
            .sort(\.$timestamp, .descending)
            .limit(limit)
            .all()
    }

    private static func capabilityUsageLogSample(
        db: Database,
        projectIds: [UUID],
        since: Date,
        limit: Int
    ) async throws -> [RequestLog] {
        guard !projectIds.isEmpty else { return [] }
        let kinds = ["tool", "resource", "prompt"]
        return try await RequestLog.query(on: db)
            .filter(\.$project.$id ~~ projectIds)
            .filter(\.$timestamp >= since)
            .filter(\.$mcpCapabilityKind ~~ kinds)
            .sort(\.$timestamp, .descending)
            .limit(limit)
            .all()
    }

    private static func successRateAndLatency(from logs: [RequestLog]) -> (
        rate: Double?,
        avg: Double?,
        p95: Int?
    ) {
        guard !logs.isEmpty else { return (nil, nil, nil) }
        let ok = logs.filter(\.countsAsSuccessfulRequestMetric).count
        let rate = Double(ok) / Double(logs.count)
        let latencies = logs.compactMap(\.latencyMs).sorted()
        let avg: Double? =
            latencies.isEmpty
                ? nil : Double(latencies.reduce(0, +)) / Double(latencies.count)
        let p95: Int? = {
            guard !latencies.isEmpty else { return nil }
            let idx = min(latencies.count - 1, Int(floor(Double(latencies.count - 1) * 0.95)))
            return latencies[idx]
        }()
        return (rate, avg, p95)
    }

    private static func methodBreakdown(from logs: [RequestLog]) -> [DashboardMethodCount] {
        var tallies: [String: Int] = [:]
        for log in logs {
            tallies[log.method, default: 0] += 1
        }
        return tallies
            .map { DashboardMethodCount(method: $0.key, count: $0.value) }
            .sorted { lhs, rhs in
                if lhs.count != rhs.count { return lhs.count > rhs.count }
                return lhs.method < rhs.method
            }
    }

    private static func topProjectsByTraffic(
        logs: [RequestLog],
        projects: [Project],
        limit: Int
    ) -> [DashboardProjectTraffic] {
        var byProject: [UUID: Int] = [:]
        for log in logs {
            let pid = log.$project.id
            byProject[pid, default: 0] += 1
        }
        let names = Dictionary(uniqueKeysWithValues: projects.map { ($0.id!, $0.name) })
        return byProject
            .sorted { $0.value > $1.value }
            .prefix(limit)
            .compactMap { pair -> DashboardProjectTraffic? in
                guard let name = names[pair.key] else { return nil }
                return DashboardProjectTraffic(
                    project_id: pair.key.uuidString,
                    project_name: name,
                    request_count: pair.value
                )
            }
    }

    private static func capabilityCountsForActiveRelease(
        project: Project,
        db: Database
    ) async throws -> (tools: Int, resources: Int, prompts: Int) {
        guard let rid = project.activeReleaseId else { return (0, 0, 0) }
        let ids = try await MCPCatalogService.readyCompiledSkillIds(releaseId: rid, db: db)
        guard !ids.isEmpty else { return (0, 0, 0) }
        let tools = try await CapabilityDef.query(on: db)
            .filter(\.$compiledSkill.$id ~~ ids)
            .filter(\.$type == "tool")
            .count()
        let resources = try await CapabilityDef.query(on: db)
            .filter(\.$compiledSkill.$id ~~ ids)
            .filter(\.$type == "resource")
            .count()
        let prompts = try await CapabilityDef.query(on: db)
            .filter(\.$compiledSkill.$id ~~ ids)
            .filter(\.$type == "prompt")
            .count()
        return (tools, resources, prompts)
    }

    private static func sumCapabilityCounts(projects: [Project], db: Database) async throws -> (
        tools: Int,
        resources: Int,
        prompts: Int
    ) {
        var tools = 0, resources = 0, prompts = 0
        for p in projects {
            let c = try await capabilityCountsForActiveRelease(project: p, db: db)
            tools += c.tools
            resources += c.resources
            prompts += c.prompts
        }
        return (tools, resources, prompts)
    }

    /// Account-wide MCP traffic and catalog totals (for root dashboard).
    static func accountDashboardSummary(req: Request) async throws -> AccountDashboardSummaryResponse {
        let account = try requireAccount(req)
        let projects = try await Project.query(on: req.db)
            .filter(\.$account.$id == account.id!)
            .all()
        let ids = projects.map { $0.id! }
        let now = Date()
        let since24h = now.addingTimeInterval(-86400)
        let since7d = now.addingTimeInterval(-7 * 86400)

        let totalAllTime = try await countRequestLogs(db: req.db, projectIds: ids, since: nil)
        let requests24h = try await countRequestLogs(db: req.db, projectIds: ids, since: since24h)
        let requests7d = try await countRequestLogs(db: req.db, projectIds: ids, since: since7d)
        let sample = try await dashboardLogSample(
            db: req.db,
            projectIds: ids,
            since: since7d,
            limit: Self.dashboardLogSampleLimit
        )
        let (rate, avg, p95) = successRateAndLatency(from: sample)
        let withActive = projects.filter { $0.activeReleaseId != nil }.count
        let caps = try await sumCapabilityCounts(projects: projects, db: req.db)
        let methods = methodBreakdown(from: sample)
        let top = topProjectsByTraffic(logs: sample, projects: projects, limit: 8)

        return AccountDashboardSummaryResponse(
            total_requests: totalAllTime,
            requests_last_24h: requests24h,
            requests_last_7d: requests7d,
            success_rate_last_7d: rate,
            metrics_sample_size_last_7d: sample.count,
            avg_latency_ms_last_7d: avg,
            p95_latency_ms_last_7d: p95,
            projects_total: projects.count,
            projects_with_active_release: withActive,
            active_tools_total: caps.tools,
            active_resources_total: caps.resources,
            active_prompts_total: caps.prompts,
            method_breakdown_last_7d: methods,
            top_projects_last_7d: top
        )
    }

    /// Per-project MCP traffic and active catalog counts (for project overview).
    static func projectDashboardSummary(req: Request) async throws -> ProjectDashboardSummaryResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        guard let pid = project.id else {
            throw Abort(.internalServerError, reason: "Invalid project")
        }
        let ids = [pid]
        let now = Date()
        let since24h = now.addingTimeInterval(-86400)
        let since7d = now.addingTimeInterval(-7 * 86400)

        let totalAllTime = try await countRequestLogs(db: req.db, projectIds: ids, since: nil)
        let requests24h = try await countRequestLogs(db: req.db, projectIds: ids, since: since24h)
        let requests7d = try await countRequestLogs(db: req.db, projectIds: ids, since: since7d)
        let sample = try await dashboardLogSample(
            db: req.db,
            projectIds: ids,
            since: since7d,
            limit: Self.dashboardLogSampleLimit
        )
        let (rate, avg, p95) = successRateAndLatency(from: sample)
        let caps = try await capabilityCountsForActiveRelease(project: project, db: req.db)
        let methods = methodBreakdown(from: sample)
        let capLogs = try await capabilityUsageLogSample(
            db: req.db,
            projectIds: ids,
            since: since7d,
            limit: Self.dashboardCapabilityUsageSampleLimit
        )
        let capabilityUsage = CapabilityUsageAggregation.breakdown(from: capLogs)

        var activeSha: String?
        var activeStatus: String?
        if let rid = project.activeReleaseId,
           let rel = try await Release.find(rid, on: req.db) {
            activeSha = rel.commitSha
            activeStatus = rel.status
        }

        return ProjectDashboardSummaryResponse(
            project_id: pid.uuidString,
            total_requests: totalAllTime,
            requests_last_24h: requests24h,
            requests_last_7d: requests7d,
            success_rate_last_7d: rate,
            metrics_sample_size_last_7d: sample.count,
            avg_latency_ms_last_7d: avg,
            p95_latency_ms_last_7d: p95,
            method_breakdown_last_7d: methods,
            active_release_id: project.activeReleaseId?.uuidString,
            active_commit_sha: activeSha,
            active_release_status: activeStatus,
            active_tools: caps.tools,
            active_resources: caps.resources,
            active_prompts: caps.prompts,
            capability_usage_last_7d: capabilityUsage
        )
    }

    // MARK: - Dashboard timeseries

    static func accountDashboardTimeseries(req: Request) async throws -> AccountDashboardTimeseriesResponse {
        let account = try requireAccount(req)
        let rangeKey = try DashboardTimeseriesService.normalizeRangeKey(req.query[String.self, at: "range"])
        let projects = try await Project.query(on: req.db)
            .filter(\.$account.$id == account.id!)
            .all()
        let ids = projects.map(\.id!)
        let firstLog: RequestLog? =
            ids.isEmpty
                ? nil
                : try await RequestLog.query(on: req.db)
                .filter(\.$project.$id ~~ ids)
                .sort(\.$timestamp, .ascending)
                .first()
        let earliest = firstLog?.timestamp
        let now = Date()
        let buckets = try DashboardTimeseriesService.buildBucketDefs(
            rangeKey: rangeKey,
            now: now,
            earliestLog: earliest
        )
        let series = try await DashboardTimeseriesService.aggregate(
            db: req.db,
            projectIds: ids,
            buckets: buckets,
            rangeEndInclusive: now
        )
        let rangeStart = buckets.first?.start ?? now
        return AccountDashboardTimeseriesResponse(
            range_key: rangeKey,
            range_start: formatDate(rangeStart),
            range_end: formatDate(now),
            buckets: series
        )
    }

    static func projectDashboardTimeseries(req: Request) async throws -> ProjectDashboardTimeseriesResponse {
        let account = try requireAccount(req)
        let project = try await requireProject(req, accountId: account.id!)
        let rangeKey = try DashboardTimeseriesService.normalizeRangeKey(req.query[String.self, at: "range"])
        guard let pid = project.id else {
            throw Abort(.internalServerError, reason: "Invalid project")
        }
        let firstLog = try await RequestLog.query(on: req.db)
            .filter(\.$project.$id == pid)
            .sort(\.$timestamp, .ascending)
            .first()
        let now = Date()
        let buckets = try DashboardTimeseriesService.buildBucketDefs(
            rangeKey: rangeKey,
            now: now,
            earliestLog: firstLog?.timestamp
        )
        let series = try await DashboardTimeseriesService.aggregate(
            db: req.db,
            projectIds: [pid],
            buckets: buckets,
            rangeEndInclusive: now
        )
        let rangeStart = buckets.first?.start ?? now
        return ProjectDashboardTimeseriesResponse(
            project_id: pid.uuidString,
            range_key: rangeKey,
            range_start: formatDate(rangeStart),
            range_end: formatDate(now),
            buckets: series
        )
    }
}

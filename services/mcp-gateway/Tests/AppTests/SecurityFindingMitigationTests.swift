import Crypto
import Fluent
import Foundation
import NIOCore
import Testing
import Vapor
import VaporTesting
@testable import App

@Suite("Security finding mitigations", .serialized)
struct SecurityFindingMitigationTests {
    @Test("Accounts can create more projects than the former paid limit")
    func unlimitedProjectCreation() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "USE_MEMORY_SESSIONS": "1",
            "FREE_PROJECT_LIMIT": "1",
            "PRO_PROJECT_LIMIT": "5",
            "FRONTEND_URL": "https://app.example.com",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            let account = Account(githubId: 910_010, login: "full-access")
            try await account.save(on: app.db)
            app.post("test-create-project") { req async throws -> ProjectResponse in
                req.storage[AccountKey.self] = account
                return try await ProjectController.create(req: req)
            }
            for index in 0..<7 {
                try await app.testing().test(
                    .POST, "/test-create-project",
                    body: ByteBuffer(string: "{\"name\":\"Project \(index)\",\"slug\":\"project-\(index)\"}"),
                    beforeRequest: { req in
                        req.headers.contentType = .json
                        req.headers.replaceOrAdd(name: .origin, value: "https://app.example.com")
                    },
                    afterResponse: { res in
                        #expect(res.status == .ok)
                    }
                )
            }
            #expect(try await Project.query(on: app.db).count() == 7)
            #expect(try await AuthController.computeNeedsProjectSelection(account: account, db: app.db) == false)
        }
    }

    @Test("Payment bypass flags grant no admin access and cannot be managed")
    func paymentBypassIsInert() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "FRONTEND_URL": "https://app.example.com",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            let legacy = Account(githubId: 910_012, login: "legacy-bypass")
            legacy.paywallBypass = true
            try await legacy.save(on: app.db)
            let admin = Account(githubId: 910_013, login: "admin-access")
            admin.isAdmin = true
            try await admin.save(on: app.db)
            app.get("test-admin-directory") { req async throws -> [AdminPrivilegedAccountRow] in
                try await AdminController.listPrivilegedAccounts(req: req)
            }
            app.post("test-admin-flags") { req async throws -> Response in
                try await AdminController.updateFlags(req: req)
            }
            try await app.testing().test(.GET, "/test-admin-directory", afterResponse: { res in
                let rows = try res.content.decode([AdminPrivilegedAccountRow].self)
                #expect(rows.count == 1)
                #expect(rows.first?.account_id == admin.id!.uuidString)
                #expect(rows.first?.paywall_bypass == false)
            })
            try await app.testing().test(.POST, "/test-admin-flags",
                body: ByteBuffer(string: "{\"account_id\":\"\(legacy.id!.uuidString)\",\"paywall_bypass\":false}"),
                beforeRequest: { req in
                    req.headers.contentType = .json
                    req.headers.replaceOrAdd(name: .origin, value: "https://app.example.com")
                }, afterResponse: { res in
                    #expect(res.status == .badRequest)
                })
            let unchanged = try #require(try await Account.find(legacy.id, on: app.db))
            #expect(unchanged.paywallBypass == true)
            #expect(unchanged.isAdmin == false)
        }
    }

    @Test("Payment endpoints are unavailable")
    func paymentEndpointsAreRemoved() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "FRONTEND_URL": "https://app.example.com",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            for path in ["/billing/checkout-session", "/billing/portal-session", "/webhooks/stripe"] {
                try await app.testing().test(.POST, path, beforeRequest: { req in
                    req.headers.replaceOrAdd(name: .origin, value: "https://app.example.com")
                }, afterResponse: { res in
                    #expect(res.status == .notFound)
                })
            }
        }
    }

    @Test("Payment removal migration restores existing suspended projects")
    func migrationRestoresLegacyProjects() async throws {
        try await withHardeningApp(appEnv: "prod", env: ["DATABASE_URL": nil, "SUPABASE_DB_URL": nil]) { app in
            let account = Account(githubId: 910_011, login: "legacy-access")
            account.subscriptionStatus = "canceled"
            try await account.save(on: app.db)
            let project = Project(accountId: account.id!, name: "Legacy", slug: "legacy", subdomain: "legacy-access")
            project.suspendedAt = Date()
            try await project.save(on: app.db)
            try await RemoveProjectPaymentSuspensions().prepare(on: app.db)
            let restored = try #require(try await Project.find(project.id, on: app.db))
            #expect(restored.suspendedAt == nil)
            let restoredAccount = try #require(try await Account.find(account.id, on: app.db))
            #expect(restoredAccount.subscriptionStatus == "canceled")
        }
    }

    @Test("Canceled accounts keep every project active")
    func canceledAccountsKeepEveryProjectActive() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "USE_SQLITE": "1",
            "USE_MEMORY_SESSIONS": "1",
            "SAAS_MCP_BASE_DOMAIN": "mcp.test",
            "STRIPE_WEBHOOK_SECRET": "test_stripe_secret",
            "FREE_PROJECT_LIMIT": "1",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            let account = Account(githubId: 910_001, login: "downgrade-user", email: "downgrade@example.com")
            account.stripeCustomerId = "cus_downgrade"
            account.stripeSubscriptionId = "sub_downgrade"
            account.subscriptionStatus = "canceled"
            try await account.save(on: app.db)

            let first = Project(accountId: account.id!, name: "First", slug: "first", subdomain: "first")
            let second = Project(accountId: account.id!, name: "Second", slug: "second", subdomain: "second")
            try await first.save(on: app.db)
            try await second.save(on: app.db)

            try await ProjectEntitlementReconciler.reconcileProjects(for: account, db: app.db)

            let updatedFirst = try #require(try await Project.find(first.id, on: app.db))
            let updatedSecond = try #require(try await Project.find(second.id, on: app.db))
            #expect(updatedFirst.suspendedAt == nil)
            #expect(updatedSecond.suspendedAt == nil)
        }
    }

    @Test("Accounts without a subscription keep every project active")
    func accountsWithoutSubscriptionKeepEveryProjectActive() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "USE_SQLITE": "1",
            "USE_MEMORY_SESSIONS": "1",
            "SAAS_MCP_BASE_DOMAIN": "mcp.test",
            "STRIPE_WEBHOOK_SECRET": "test_stripe_secret",
            "FREE_PROJECT_LIMIT": "1",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            let account = Account(githubId: 910_002, login: "delete-user", email: "delete@example.com")
            account.stripeCustomerId = "cus_delete"
            account.stripeSubscriptionId = nil
            account.subscriptionStatus = "none"
            try await account.save(on: app.db)

            let first = Project(accountId: account.id!, name: "First", slug: "first", subdomain: "deletefirst")
            let second = Project(accountId: account.id!, name: "Second", slug: "second", subdomain: "deletesecond")
            try await first.save(on: app.db)
            try await second.save(on: app.db)

            try await ProjectEntitlementReconciler.reconcileProjects(for: account, db: app.db)

            let updatedFirst = try #require(try await Project.find(first.id, on: app.db))
            let updatedSecond = try #require(try await Project.find(second.id, on: app.db))
            #expect(updatedFirst.suspendedAt == nil)
            #expect(updatedSecond.suspendedAt == nil)
        }
    }

    @Test("Legacy payment suspensions are cleared without a subscription")
    func legacyPaymentSuspensionsAreCleared() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "USE_SQLITE": "1",
            "USE_MEMORY_SESSIONS": "1",
            "SAAS_MCP_BASE_DOMAIN": "mcp.test",
            "STRIPE_WEBHOOK_SECRET": "test_stripe_secret",
            "FREE_PROJECT_LIMIT": "1",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            let account = Account(githubId: 910_003, login: "reactivate-user", email: "reactivate@example.com")
            account.stripeCustomerId = "cus_reactivate"
            account.stripeSubscriptionId = "sub_reactivate"
            account.subscriptionStatus = "canceled"
            try await account.save(on: app.db)

            let first = Project(accountId: account.id!, name: "First", slug: "first", subdomain: "reactfirst")
            let second = Project(accountId: account.id!, name: "Second", slug: "second", subdomain: "reactsecond")
            first.suspendedAt = Date()
            second.suspendedAt = Date()
            try await first.save(on: app.db)
            try await second.save(on: app.db)

            try await ProjectEntitlementReconciler.reconcileProjects(for: account, db: app.db)

            let updatedFirst = try #require(try await Project.find(first.id, on: app.db))
            let updatedSecond = try #require(try await Project.find(second.id, on: app.db))
            #expect(updatedFirst.suspendedAt == nil)
            #expect(updatedSecond.suspendedAt == nil)
        }
    }

    @Test("Verified custom domain routes without a subscription")
    func verifiedCustomDomainRoutesWithoutSubscription() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "USE_SQLITE": "1",
            "USE_MEMORY_SESSIONS": "1",
            "MCP_OAUTH_ENABLED": "0",
            "SAAS_MCP_BASE_DOMAIN": "mcp.test",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            let account = Account(githubId: 910_004, login: "custom-free", email: "custom-free@example.com")
            account.subscriptionStatus = "canceled"
            try await account.save(on: app.db)
            let project = Project(
                accountId: account.id!,
                name: "Custom Free",
                slug: "custom-free",
                subdomain: "customfree",
                customDomain: "mcp.free.example",
                customDomainVerifiedAt: Date()
            )
            try await project.save(on: app.db)

            try await app.testing().test(
                .GET,
                "/",
                beforeRequest: { req in
                    req.headers.replaceOrAdd(name: .host, value: "mcp.free.example")
                },
                afterResponse: { res in
                    #expect(res.status == .ok)
                    #expect(res.body.string == "MyContextProtocol")
                }
            )
        }
    }

    @Test("Verified custom domain routes for Pro account")
    func verifiedCustomDomainRoutesForProAccount() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "USE_SQLITE": "1",
            "USE_MEMORY_SESSIONS": "1",
            "MCP_OAUTH_ENABLED": "0",
            "SAAS_MCP_BASE_DOMAIN": "mcp.test",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            let account = Account(githubId: 910_005, login: "custom-pro", email: "custom-pro@example.com")
            account.subscriptionStatus = "active"
            try await account.save(on: app.db)
            let project = Project(
                accountId: account.id!,
                name: "Custom Pro",
                slug: "custom-pro",
                subdomain: "custompro",
                customDomain: "mcp.pro.example",
                customDomainVerifiedAt: Date()
            )
            try await project.save(on: app.db)

            try await app.testing().test(
                .GET,
                "/",
                beforeRequest: { req in
                    req.headers.replaceOrAdd(name: .host, value: "mcp.pro.example")
                },
                afterResponse: { res in
                    #expect(res.status == .ok)
                    #expect(res.body.string == "MyContextProtocol")
                }
            )
        }
    }

    @Test("Railway domain config validates scoped IDs, token, port, and HTTPS API URL")
    func railwayDomainConfigValidation() async throws {
        await TestProcessEnvGate.run {
            let (apply, restore) = hardeningTemporaryEnv([
                "RAILWAY_PROJECT_TOKEN": "railway_secret",
                "RAILWAY_PROJECT_ID": "3647f696-1766-459a-ac3f-0482e5a1f26c",
                "RAILWAY_ENVIRONMENT_ID": "9b29a079-8db3-47d5-81e5-6a20d747ad1f",
                "RAILWAY_SERVICE_ID": "5c1a6101-9c11-49fa-b384-7a71fee049c8",
                "RAILWAY_DOMAIN_API_URL": "https://backboard.railway.com/graphql/v2/",
                "RAILWAY_DOMAIN_TARGET_PORT": "8080",
            ])
            apply()
            defer { restore() }
            let config = RailwayDomainService.Config.fromEnvironment()
            #expect(config?.apiURL == "https://backboard.railway.com/graphql/v2")
            #expect(config?.targetPort == 8080)
            #expect(config?.authentication == .projectToken("railway_secret"))
        }

        await TestProcessEnvGate.run {
            let (apply, restore) = hardeningTemporaryEnv([
                "RAILWAY_PROJECT_TOKEN": "railway_secret",
                "RAILWAY_PROJECT_ID": "not-a-uuid",
                "RAILWAY_ENVIRONMENT_ID": "9b29a079-8db3-47d5-81e5-6a20d747ad1f",
                "RAILWAY_SERVICE_ID": "5c1a6101-9c11-49fa-b384-7a71fee049c8",
            ])
            apply()
            defer { restore() }
            #expect(RailwayDomainService.Config.fromEnvironment() == nil)
        }

        await TestProcessEnvGate.run {
            let (apply, restore) = hardeningTemporaryEnv([
                "RAILWAY_PROJECT_TOKEN": "railway_secret",
                "RAILWAY_PROJECT_ID": "3647f696-1766-459a-ac3f-0482e5a1f26c",
                "RAILWAY_ENVIRONMENT_ID": "9b29a079-8db3-47d5-81e5-6a20d747ad1f",
                "RAILWAY_SERVICE_ID": "5c1a6101-9c11-49fa-b384-7a71fee049c8",
                "RAILWAY_DOMAIN_API_URL": "http://backboard.railway.com/graphql/v2",
            ])
            apply()
            defer { restore() }
            #expect(RailwayDomainService.Config.fromEnvironment() == nil)
        }

        await TestProcessEnvGate.run {
            let (apply, restore) = hardeningTemporaryEnv([
                "RAILWAY_PROJECT_TOKEN": "railway_secret",
                "RAILWAY_PROJECT_ID": "3647f696-1766-459a-ac3f-0482e5a1f26c",
                "RAILWAY_ENVIRONMENT_ID": "9b29a079-8db3-47d5-81e5-6a20d747ad1f",
                "RAILWAY_SERVICE_ID": "5c1a6101-9c11-49fa-b384-7a71fee049c8",
                "RAILWAY_DOMAIN_TARGET_PORT": "70000",
            ])
            apply()
            defer { restore() }
            #expect(RailwayDomainService.Config.fromEnvironment() == nil)
        }
    }

    @Test("Railway domain HTTP errors redact response bodies")
    func railwayDomainHTTPErrorDescriptionRedactsBodies() {
        let error = RailwayDomainError.http(status: .badRequest, body: "secret-token-in-body")
        #expect(String(describing: error) == "HTTP 400")
        #expect(!String(describing: error).contains("secret-token-in-body"))
    }

    @Test("Internal TLS ask requires secret and allocated project subdomain")
    func internalTlsAskRequiresSecretAndAllocatedProjectSubdomain() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "USE_SQLITE": "1",
            "USE_MEMORY_SESSIONS": "1",
            "SAAS_MCP_BASE_DOMAIN": "mcp.test",
            "INTERNAL_TLS_ASK_SECRET": "tls-ask-secret",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            try await app.testing().test(
                .GET,
                "/internal/custom-domain/verify-for-tls?domain=unknown.mcp.test",
                afterResponse: { res in
                    #expect(res.status == .unauthorized)
                }
            )

            try await app.testing().test(
                .GET,
                "/internal/custom-domain/verify-for-tls?domain=unknown.mcp.test",
                beforeRequest: { req in
                    req.headers.replaceOrAdd(name: "X-Internal-TLS-Ask-Secret", value: "tls-ask-secret")
                },
                afterResponse: { res in
                    #expect(res.status == .unprocessableEntity)
                }
            )

            let account = Account(githubId: 910_006, login: "tls-ask", email: "tls-ask@example.com")
            try await account.save(on: app.db)
            let project = Project(accountId: account.id!, name: "TLS Ask", slug: "tls-ask", subdomain: "known")
            try await project.save(on: app.db)

            try await app.testing().test(
                .GET,
                "/internal/custom-domain/verify-for-tls?domain=known.mcp.test",
                beforeRequest: { req in
                    req.headers.replaceOrAdd(name: "X-Internal-TLS-Ask-Secret", value: "tls-ask-secret")
                },
                afterResponse: { res in
                    #expect(res.status == .ok)
                }
            )

            project.suspendedAt = Date()
            try await project.save(on: app.db)
            try await app.testing().test(
                .GET,
                "/internal/custom-domain/verify-for-tls?domain=known.mcp.test",
                beforeRequest: { req in
                    req.headers.replaceOrAdd(name: "X-Internal-TLS-Ask-Secret", value: "tls-ask-secret")
                },
                afterResponse: { res in
                    #expect(res.status == .ok)
                }
            )
        }
    }

    @Test("GitHub webhook dispatch binds to hook id before signature verification")
    func githubWebhookDispatchBindsToHookIdBeforeSignatureVerification() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "USE_SQLITE": "1",
            "USE_MEMORY_SESSIONS": "1",
            "SAAS_MCP_BASE_DOMAIN": "mcp.test",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            let account = Account(githubId: 910_007, login: "hook-bind", email: "hook-bind@example.com")
            try await account.save(on: app.db)
            let firstProject = Project(accountId: account.id!, name: "Hook First", slug: "hook-first", subdomain: "hookfirst")
            let secondProject = Project(accountId: account.id!, name: "Hook Second", slug: "hook-second", subdomain: "hooksecond")
            try await firstProject.save(on: app.db)
            try await secondProject.save(on: app.db)

            let first = RepoConnection(
                projectId: firstProject.id!,
                provider: "github",
                repoOwner: "Stygian-Tech",
                repoName: "shared-repo",
                defaultBranch: "main",
                webhookId: "hook-one",
                webhookSecret: "secret-one"
            )
            let second = RepoConnection(
                projectId: secondProject.id!,
                provider: "github",
                repoOwner: "Stygian-Tech",
                repoName: "shared-repo",
                defaultBranch: "main",
                webhookId: "hook-two",
                webhookSecret: "secret-two"
            )
            try await first.save(on: app.db)
            try await second.save(on: app.db)

            let payload = #"{"ref":"refs/heads/feature","repository":{"full_name":"Stygian-Tech/shared-repo","default_branch":"main"}}"#
            try await app.testing().test(
                .POST,
                "/webhooks/github",
                body: ByteBuffer(string: payload),
                beforeRequest: { req in
                    req.headers.replaceOrAdd(name: "X-GitHub-Hook-ID", value: "hook-two")
                    req.headers.replaceOrAdd(name: "X-Hub-Signature-256", value: githubSignatureHeader(payload: payload, secret: "secret-two"))
                    req.headers.replaceOrAdd(name: .contentType, value: "application/json")
                },
                afterResponse: { res in
                    #expect(res.status == .ok, "github webhook status=\(res.status) body=\(res.body.string)")
                    #expect(res.body.string.contains(#""skipped":"not_default_branch""#))
                }
            )
        }
    }

    @Test("MCP resource subscriptions enforce per-subscriber caps")
    func mcpResourceSubscriptionsEnforcePerSubscriberCaps() {
        let subscriptions = McpResourceSubscriptions()
        let subscriber = UUID()
        for i in 0 ..< McpResourceSubscriptions.maxSubscriptionsPerSubscriber {
            #expect(subscriptions.subscribe(subscriberId: subscriber, uri: "resource://\(i)"))
        }
        #expect(subscriptions.subscribe(subscriberId: subscriber, uri: "resource://0"))
        #expect(!subscriptions.subscribe(subscriberId: subscriber, uri: "resource://overflow"))
        #expect(!subscriptions.subscribe(subscriberId: UUID(), uri: String(repeating: "a", count: McpResourceSubscriptions.maxUriLength + 1)))
    }

    @Test("RepoFetcher validates GitHub path segments")
    func repoFetcherValidatesGitHubPathSegments() {
        #expect(RepoFetcher.isValidGitHubOwnerOrRepo("Stygian-Tech"))
        #expect(RepoFetcher.isValidGitHubOwnerOrRepo("repo.name"))
        #expect(!RepoFetcher.isValidGitHubOwnerOrRepo("../owner"))
        #expect(!RepoFetcher.isValidGitHubOwnerOrRepo("owner/repo"))
        #expect(!RepoFetcher.isValidGitHubOwnerOrRepo(String(repeating: "a", count: 101)))

        #expect(RepoFetcher.isValidGitHubRef("feature/harden-oauth"))
        #expect(RepoFetcher.isValidGitHubRef("release-2026.06"))
        #expect(!RepoFetcher.isValidGitHubRef("../main"))
        #expect(!RepoFetcher.isValidGitHubRef("main?token=x"))
        #expect(!RepoFetcher.isValidGitHubRef(String(repeating: "a", count: 256)))
        #expect(RepoFetcher.pathSegmentEscape("feature/harden-oauth") == "feature%2Fharden-oauth")
    }

    @Test("RepoFetcher rejects archive symlink escapes and excessive entries")
    func repoFetcherRejectsArchiveAbuse() throws {
        let root = FileManager.default.temporaryDirectory
            .appendingPathComponent("repo-archive-\(UUID().uuidString)")
        let outside = FileManager.default.temporaryDirectory
            .appendingPathComponent("repo-outside-\(UUID().uuidString)")
        defer {
            try? FileManager.default.removeItem(at: root)
            try? FileManager.default.removeItem(at: outside)
        }
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try "secret".write(to: outside, atomically: true, encoding: .utf8)
        try FileManager.default.createSymbolicLink(
            at: root.appendingPathComponent("escape"),
            withDestinationURL: outside
        )

        do {
            try RepoFetcher.validateExtractedArchive(root: root)
            Issue.record("Expected archive escape to be rejected")
        } catch RepoFetcherError.extractEscaped {
        } catch {
            Issue.record("Unexpected archive validation error: \(error)")
        }

        try FileManager.default.removeItem(at: root)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let (apply, restore) = hardeningTemporaryEnv(["REPO_EXTRACT_MAX_ENTRIES": "1"])
        apply()
        defer { restore() }
        for i in 0 ... 100 {
            try "x".write(to: root.appendingPathComponent("file-\(i).txt"), atomically: true, encoding: .utf8)
        }
        do {
            try RepoFetcher.validateExtractedArchive(root: root)
            Issue.record("Expected excessive archive entries to be rejected")
        } catch RepoFetcherError.extractedArchiveTooManyEntries {
        } catch {
            Issue.record("Unexpected archive validation error: \(error)")
        }
    }

    @Test("OAuth handoff tokens are single use")
    func oauthHandoffTokensAreSingleUse() async throws {
        try await withHardeningApp(appEnv: "prod", env: [
            "USE_SQLITE": "1",
            "USE_MEMORY_SESSIONS": "1",
            "SAAS_MCP_BASE_DOMAIN": "mcp.test",
            "DATABASE_URL": nil,
            "SUPABASE_DB_URL": nil,
        ]) { app in
            let account = Account(githubId: 910_008, login: "handoff-single-use", email: "handoff-single-use@example.com")
            try await account.save(on: app.db)
            let token = try await OAuthHandoffService.issue(accountId: account.id!, on: app.db)

            let consumed = try await OAuthHandoffService.consume(token, on: app.db)
            #expect(consumed == account.id)

            let replay = try await OAuthHandoffService.consume(token, on: app.db)
            #expect(replay == nil)
        }
    }
}

private func withHardeningApp(
    appEnv: String,
    env: [String: String?],
    _ run: @Sendable @escaping (Application) async throws -> Void
) async throws {
    try await TestProcessEnvGate.run {
        let prevEnv = AppEnvironment._testOverrideAppEnv
        // The retention scheduler prunes once at boot; its deletes race this suite's writes and
        // lock the shared in-memory SQLite database.
        var env = env
        env["DISABLE_SKILL_USAGE_RETENTION_SCHEDULER"] = "1"
        let (apply, restore) = hardeningTemporaryEnv(env)
        AppEnvironment._testOverrideAppEnv = appEnv
        apply()
        defer {
            restore()
            AppEnvironment._testOverrideAppEnv = prevEnv
        }

        let app = try await Application.make(.testing)
        try await configure(app)
        do {
            try await run(app)
        } catch {
            try await app.asyncShutdown()
            throw error
        }
        try await app.asyncShutdown()
    }
}

private func githubSignatureHeader(payload: String, secret: String) -> String {
    let key = SymmetricKey(data: Data(secret.utf8))
    let mac = HMAC<SHA256>.authenticationCode(for: Data(payload.utf8), using: key)
    let hex = mac.map { String(format: "%02x", $0) }.joined()
    return "sha256=\(hex)"
}

private func hardeningTemporaryEnv(_ overrides: [String: String?]) -> (() -> Void, () -> Void) {
    var saved: [String: String?] = [:]
    for (key, _) in overrides {
        saved[key] = ProcessInfo.processInfo.environment[key]
    }
    let apply: () -> Void = {
        for (key, val) in overrides {
            if let v = val {
                setenv(key, v, 1)
            } else {
                unsetenv(key)
            }
        }
    }
    let restore: () -> Void = {
        for (key, val) in saved {
            if let v = val {
                setenv(key, v, 1)
            } else {
                unsetenv(key)
            }
        }
    }
    return (apply, restore)
}

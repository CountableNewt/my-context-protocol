import Fluent
import Vapor

private func userResponse(for account: Account, suggestedGithubAppInstall: Bool = false, needsProjectSelection: Bool = false) -> UserResponse {
    UserResponse(
        id: account.id!.uuidString,
        email: account.email,
        login: account.login,
        avatar_url: account.avatarUrl,
        plan: account.hasProEntitlements ? "pro" : "free",
        is_admin: account.isAdmin,
        internal_pro_bypass: InternalProBypass.matches(login: account.login, githubId: account.githubId),
        can_manage_subscription: account.hasStripeCustomerRecord,
        suggested_github_app_install: suggestedGithubAppInstall,
        app_env: AppEnvironment.appEnvString,
        non_production_bypasses: AppEnvironment.nonProductionBypassesActive,
        needs_project_selection: needsProjectSelection
    )
}

/// Marks account as admin when `INTERNAL_ADMIN_GITHUB_*` env lists match (idempotent).
private func syncEnvAdminBootstrap(account: Account, db: Database) async throws {
    guard InternalAdminBootstrap.matches(login: account.login, githubId: account.githubId) else {
        return
    }
    guard !account.isAdmin else { return }
    account.isAdmin = true
    account.adminGrantedAt = Date()
    try await account.save(on: db)
}

struct AuthController {
    /// Space-separated GitHub OAuth scopes (authorize URL). Keep in sync with Notion “GitHub OAuth scopes & GitHub App permissions” (spec child page).
    /// - `read:user` / `user:email`: profile for login.
    /// - `repo`: private repos, contents, and **repository webhooks** (REST `POST /repos/{owner}/{repo}/hooks`).
    /// - `read:org`: org membership / listing org-owned repos (still subject to org SAML SSO on GitHub’s side).
    private static let githubOAuthScope = "read:user user:email repo read:org"

    /// When OAuth state is missing or invalid and we cannot recover `return_to`, redirect here or fail.
    private static func oauthFailureResponse(req: Request, message: String, log: String? = nil) throws -> Response {
        if let log = log {
            req.logger.warning("\(log)")
        }
        if let url = AppFrontendURL.loginErrorURL(code: message) {
            return req.redirect(to: url, redirectType: .normal)
        }
        throw Abort(.badRequest, reason: message)
    }

    static func githubInitiate(req: Request) async throws -> Response {
        let prompt = req.query[String.self, at: "prompt"]
        if let prompt, prompt != "select_account" {
            throw Abort(.badRequest, reason: "Unsupported GitHub sign-in prompt")
        }
        let returnTo: String
        if let q = req.query[String.self, at: "return_to"], !q.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            returnTo = try AppFrontendURL.validateReturnTo(q, for: req)
        } else if let d = AppFrontendURL.defaultReturnToURL() {
            returnTo = d
        } else {
            throw Abort(
                .badRequest,
                reason: "return_to is required, or set FRONTEND_URL or CORS_ORIGIN for a default redirect"
            )
        }

        return try githubAuthorizeResponse(req: req, returnTo: returnTo, prompt: prompt)
    }

    /// A separate entry point keeps browser redirects restricted to frontend origins.
    /// The CLI binds this numeric loopback address before opening the authorization URL.
    static func cliGithubInitiate(req: Request) async throws -> Response {
        let prompt = req.query[String.self, at: "prompt"]
        if let prompt, prompt != "select_account" {
            throw Abort(.badRequest, reason: "Unsupported GitHub sign-in prompt")
        }
        guard let raw = req.query[String.self, at: "return_to"] else {
            throw Abort(.badRequest, reason: "return_to is required")
        }
        let returnTo = try validateCliReturnTo(raw)
        return try githubAuthorizeResponse(req: req, returnTo: returnTo, prompt: prompt)
    }

    static func validateCliReturnTo(_ raw: String) throws -> String {
        let invalid = Abort(.badRequest, reason: "CLI return_to must be http://127.0.0.1:PORT/callback with a single random state parameter")
        guard raw.utf8.count <= 512,
              let url = URLComponents(string: raw),
              url.scheme == "http", url.host == "127.0.0.1",
              url.user == nil, url.password == nil, url.fragment == nil,
              let port = url.port, (1 ... 65535).contains(port),
              url.percentEncodedPath == "/callback",
              let query = url.queryItems, query.count == 1,
              query[0].name == "state", let nonce = query[0].value,
              (43 ... 128).contains(nonce.utf8.count),
              nonce.utf8.allSatisfy({ byte in
                  (65 ... 90).contains(byte) || (97 ... 122).contains(byte)
                      || (48 ... 57).contains(byte) || byte == 45 || byte == 95
              }),
              // Require the canonical spelling: no encoded host/path/query tricks or controls.
              raw == "http://127.0.0.1:\(port)/callback?state=\(nonce)" else {
            throw invalid
        }
        return raw
    }

    private static func githubAuthorizeResponse(req: Request, returnTo: String, prompt: String?) throws -> Response {
        guard let clientId = Environment.get("GITHUB_CLIENT_ID"), !clientId.isEmpty else {
            throw Abort(.internalServerError, reason: "GITHUB_CLIENT_ID not configured")
        }
        let redirectUri = try GitHubOAuthLoginConfig.redirectURI(logger: req.logger)
        let state: String
        do {
            state = try SignedOAuthState.signGitHubOAuth(returnTo: returnTo)
        } catch SignedOAuthState.StateError.keyNotConfigured {
            throw Abort(.internalServerError, reason: "ENCRYPTION_KEY must be configured (32-byte base64) for OAuth state signing")
        } catch {
            throw Abort(.internalServerError, reason: "Failed to build OAuth state")
        }

        var components = URLComponents(string: "https://github.com/login/oauth/authorize")!
        components.queryItems = [
            URLQueryItem(name: "client_id", value: clientId),
            URLQueryItem(name: "redirect_uri", value: redirectUri),
            URLQueryItem(name: "state", value: state),
            URLQueryItem(name: "scope", value: githubOAuthScope),
        ]
        if let prompt {
            components.queryItems?.append(URLQueryItem(name: "prompt", value: prompt))
        }
        guard let url = components.url else {
            throw Abort(.internalServerError, reason: "Invalid OAuth URL")
        }
        return req.redirect(to: url.absoluteString, redirectType: .normal)
    }

    static func githubCallback(req: Request) async throws -> Response {
        guard let clientId = Environment.get("GITHUB_CLIENT_ID"), !clientId.isEmpty,
              let clientSecret = Environment.get("GITHUB_CLIENT_SECRET"), !clientSecret.isEmpty else {
            throw Abort(.internalServerError, reason: "GitHub OAuth not configured")
        }
        let redirectUri = try GitHubOAuthLoginConfig.redirectURI(logger: req.logger)

        let stateParam = req.query[String.self, at: "state"]
        let returnTo: String
        if let s = stateParam, !s.isEmpty {
            do {
                returnTo = try SignedOAuthState.verifyGitHubOAuth(state: s)
            } catch {
                return try oauthFailureResponse(
                    req: req,
                    message: "oauth_invalid_state",
                    log: "GitHub OAuth state verify failed: \(error)"
                )
            }
        } else {
            return try oauthFailureResponse(
                req: req,
                message: "oauth_missing_state",
                log: "GitHub OAuth state query parameter missing"
            )
        }

        let code = req.query[String.self, at: "code"]

        guard let code = code, !code.isEmpty else {
            return req.redirect(to: returnTo + (returnTo.contains("?") ? "&" : "?") + "error=missing_code", redirectType: .normal)
        }

        struct TokenRequest: Content {
            let client_id: String
            let client_secret: String
            let code: String
            let redirect_uri: String
        }
        struct TokenResponse: Content {
            let access_token: String?
            let error: String?
            let error_description: String?
        }

        let tokenReq = TokenRequest(
            client_id: clientId,
            client_secret: clientSecret,
            code: code,
            redirect_uri: redirectUri
        )
        let tokenResp = try await req.client.post("https://github.com/login/oauth/access_token") { clientReq in
            try clientReq.content.encode(tokenReq)
            clientReq.headers.contentType = .json
            clientReq.headers.add(name: "Accept", value: "application/json")
            clientReq.headers.add(name: "User-Agent", value: "MyContextProtocol-OAuth")
        }

        // Read raw body and decode manually to avoid 415 when GitHub returns HTML error pages
        guard let body = tokenResp.body else {
            return req.redirect(to: returnTo + (returnTo.contains("?") ? "&" : "?") + "error=token_exchange_failed", redirectType: .normal)
        }
        let tokenBody: TokenResponse
        do {
            tokenBody = try JSONDecoder().decode(TokenResponse.self, from: Data(buffer: body))
        } catch {
            return req.redirect(to: returnTo + (returnTo.contains("?") ? "&" : "?") + "error=token_decode_failed", redirectType: .normal)
        }
        guard let accessToken = tokenBody.access_token, !accessToken.isEmpty else {
            let err = tokenBody.error_description ?? tokenBody.error ?? "Token exchange failed"
            return req.redirect(to: returnTo + (returnTo.contains("?") ? "&" : "?") + "error=\(err.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? err)", redirectType: .normal)
        }

        struct GitHubUser: Content {
            let id: Int
            let login: String?
            let avatar_url: String?
            let email: String?
        }

        let userResp = try await req.client.get("https://api.github.com/user") { clientReq in
            clientReq.headers.bearerAuthorization = BearerAuthorization(token: accessToken)
            clientReq.headers.add(name: "Accept", value: "application/vnd.github.v3+json")
            clientReq.headers.add(name: "User-Agent", value: "MyContextProtocol-OAuth")
        }

        let userBodyData: Data? = userResp.body.map { Data(buffer: $0) }
        guard let data = userBodyData, !data.isEmpty else {
            req.logger.warning("GitHub user API: empty body, status=\(userResp.status.code)")
            return req.redirect(to: returnTo + (returnTo.contains("?") ? "&" : "?") + "error=user_fetch_failed", redirectType: .normal)
        }

        guard (200 ..< 300).contains(userResp.status.code) else {
            struct GitHubError: Decodable { let message: String? }
            let msg = (try? JSONDecoder().decode(GitHubError.self, from: data))?.message ?? String(data: data, encoding: .utf8).map { String($0.prefix(100)) } ?? "unknown"
            req.logger.warning("GitHub user API: status=\(userResp.status.code), body=\(msg)")
            return req.redirect(to: returnTo + (returnTo.contains("?") ? "&" : "?") + "error=user_fetch_failed", redirectType: .normal)
        }

        let ghUser: GitHubUser
        do {
            ghUser = try JSONDecoder().decode(GitHubUser.self, from: data)
        } catch {
            req.logger.warning("GitHub user API: decode failed - \(error), body=\(String(data: data, encoding: .utf8).map { String($0.prefix(200)) } ?? "?")")
            return req.redirect(to: returnTo + (returnTo.contains("?") ? "&" : "?") + "error=user_fetch_failed", redirectType: .normal)
        }

        let account: Account
        if let existing = try await Account.query(on: req.db).filter(\.$githubId == Int64(ghUser.id)).first() {
            existing.login = ghUser.login ?? existing.login
            existing.avatarUrl = ghUser.avatar_url
            existing.email = ghUser.email ?? existing.email
            if let encrypted = try? TokenEncryption.encrypt(accessToken) {
                existing.githubTokenEncrypted = encrypted
            }
            try await existing.save(on: req.db)
            account = existing
        } else {
            let newAccount = Account(
                githubId: Int64(ghUser.id),
                login: ghUser.login ?? "user\(ghUser.id)",
                avatarUrl: ghUser.avatar_url,
                email: ghUser.email
            )
            if let encrypted = try? TokenEncryption.encrypt(accessToken) {
                newAccount.githubTokenEncrypted = encrypted
            }
            try await newAccount.save(on: req.db)
            account = newAccount
        }

        try await syncEnvAdminBootstrap(account: account, db: req.db)

        req.session.data["accountId"] = account.id?.uuidString
        // Token-based handoff: 302 redirect with one-time token. Using 302 (not HTML meta refresh)
        // ensures a single navigation so the frontend middleware sees auth_token exactly once.
        let handoffToken = try await OAuthHandoffService.issue(accountId: account.id!, on: req.db)
        let successUrl = returnTo + (returnTo.contains("?") ? "&" : "?") + "auth_token=" + handoffToken
        return req.redirect(to: successUrl, redirectType: .normal)
    }

    static func me(req: Request) async throws -> UserResponse {
        do {
            guard let accountIdString = req.session.data["accountId"],
                  let accountId = UUID(uuidString: accountIdString) else {
                throw Abort(.unauthorized, reason: "Not authenticated")
            }
            guard let account = try await Account.find(accountId, on: req.db) else {
                req.session.destroy()
                throw Abort(.unauthorized, reason: "Invalid session")
            }
            try await syncEnvAdminBootstrap(account: account, db: req.db)
            try await syncStripeStatusIfStale(account: account, req: req)
            let suggest = try await Self.suggestedGithubAppMe(account: account, db: req.db)
            let needsProjectSelection = try await Self.computeNeedsProjectSelection(account: account, db: req.db)
            return userResponse(for: account, suggestedGithubAppInstall: suggest, needsProjectSelection: needsProjectSelection)
        } catch let abort as AbortError {
            throw abort
        } catch {
            req.logger.error("auth/me infrastructure error: \(error)")
            throw Abort(.serviceUnavailable, reason: "Auth service temporarily unavailable")
        }
    }

    /// Re-fetches Stripe subscription status when the cached value is older than 5 minutes.
    /// Silently swallows errors so a Stripe outage never breaks `/auth/me`.
    private static func syncStripeStatusIfStale(account: Account, req: Request) async throws {
        guard account.stripeSubscriptionId != nil else { return }
        let staleCutoff: TimeInterval = 300  // 5 minutes
        if let checked = account.stripeStatusCheckedAt, Date().timeIntervalSince(checked) < staleCutoff {
            return
        }
        do {
            try await StripeSubscriptionSync.syncAccount(
                account: account,
                client: req.client,
                db: req.db,
                logger: req.logger
            )
        } catch {
            req.logger.warning("stripe_sync on /auth/me failed (non-fatal): \(error)")
        }
    }

    /// True when the account is on free tier and has more non-suspended projects than the free limit.
    static func computeNeedsProjectSelection(account: Account, db: Database) async throws -> Bool {
        guard !account.hasProEntitlements else { return false }
        let freeLimit = Int(Environment.get("FREE_PROJECT_LIMIT") ?? "") ?? 1
        let activeCount = try await Project.query(on: db)
            .filter(\.$account.$id == account.id!)
            .filter(\.$suspendedAt == .null)
            .count()
        return activeCount > freeLimit
    }

    /// Repositories the session user can access on GitHub (uses stored OAuth token).
    static func listGithubRepos(req: Request) async throws -> [GithubRepoListItem] {
        guard let account = req.storage[AccountKey.self], let account = account else {
            throw Abort(.unauthorized, reason: "Not authenticated")
        }
        guard let encrypted = account.githubTokenEncrypted,
              let token = try? TokenEncryption.decrypt(encrypted), !token.isEmpty else {
            throw Abort(.badRequest, reason: "No GitHub token. Re-authorize with repo scope.")
        }
        return try await GitHubRepositoriesService.listUserRepositories(
            token: token,
            client: req.client,
            logger: req.logger
        )
    }

    /// Exchange one-time auth token for session.
    /// - If `redirect` query param is present: sets session, redirects to that URL (avoids CORS).
    /// - Else: returns JSON (for fetch with credentials: include).
    static func confirm(req: Request) async throws -> Response {
        guard let token = req.query[String.self, at: "token"], !token.isEmpty else {
            throw Abort(.badRequest, reason: "Missing token")
        }
        guard let accountUUID = try await OAuthHandoffService.consume(token, on: req.db) else {
            throw Abort(.unauthorized, reason: "Invalid or expired token")
        }
        let accountId = accountUUID.uuidString
        guard let account = try await Account.find(accountUUID, on: req.db) else {
            throw Abort(.unauthorized, reason: "Account not found")
        }
        try await syncEnvAdminBootstrap(account: account, db: req.db)
        req.session.data["accountId"] = accountId

        if let redirectRaw = req.query[String.self, at: "redirect"], !redirectRaw.isEmpty {
            let path = try AppFrontendURL.validateRelativeBrowserPath(redirectRaw, label: "redirect")
            guard let base = AppFrontendURL.normalizedBase() else {
                throw Abort(.internalServerError, reason: "FRONTEND_URL or CORS_ORIGIN must be set for redirect")
            }
            return req.redirect(to: base + path, redirectType: .normal)
        }

        let suggest = try await Self.suggestedGithubAppMe(account: account, db: req.db)
        let needsProjectSelection = try await Self.computeNeedsProjectSelection(account: account, db: req.db)
        let response = try await userResponse(for: account, suggestedGithubAppInstall: suggest, needsProjectSelection: needsProjectSelection).encodeResponse(for: req)
        // Native clients need the server's configured frontend origin for cookie-authenticated writes.
        if let origin = AppFrontendURL.allowedOriginBases().first {
            response.headers.replaceOrAdd(name: "X-Context-Frontend-Origin", value: origin)
        }
        response.headers.replaceOrAdd(name: .cacheControl, value: "no-store")
        return response
    }

    /// Pro users with GitHub repos missing App installation, when slug + webhook base URL are configured.
    private static func suggestedGithubAppMe(account: Account, db: Database) async throws -> Bool {
        guard account.hasProEntitlements else { return false }
        guard let slug = Environment.get("GITHUB_APP_SLUG"), !slug.isEmpty else { return false }
        guard let base = Environment.get("WEBHOOK_BASE_URL"), !base.isEmpty else { return false }
        guard let aid = account.id else { return false }
        let projects = try await Project.query(on: db).filter(\.$account.$id == aid).all()
        for project in projects {
            let conns = try await project.$repoConnections.get(on: db)
            if conns.contains(where: { $0.provider == "github" && $0.githubInstallationId == nil }) {
                return true
            }
        }
        return false
    }

    static func logout(req: Request) async throws -> Response {
        req.session.destroy()
        return Response(status: .noContent)
    }
}

struct UserResponse: Content {
    let id: String
    let email: String?
    let login: String?
    let avatar_url: String?
    let plan: String
    /// Platform admin (DB flag and/or env bootstrap). Always exposed for nav gating.
    let is_admin: Bool
    /// True when this account matches `INTERNAL_PRO_GITHUB_*` env allowlists.
    let internal_pro_bypass: Bool
    /// True when a Stripe Customer exists (Customer Portal / paid subscription management).
    let can_manage_subscription: Bool
    /// Hint for Pro: install the GitHub App so webhooks can use installation tokens.
    let suggested_github_app_install: Bool
    /// Backend `APP_ENV`: `local`, `dev`, or `prod`.
    let app_env: String
    /// True when local-only Pro/rate-limit bypasses are active (`APP_ENV=local` and `strictProGating` is off).
    let non_production_bypasses: Bool
    /// True when account is on free tier but has more active (non-suspended) projects than the free limit.
    let needs_project_selection: Bool

    enum CodingKeys: String, CodingKey {
        case id, email, login, plan
        case avatar_url = "avatar_url"
        case is_admin = "is_admin"
        case internal_pro_bypass = "internal_pro_bypass"
        case can_manage_subscription = "can_manage_subscription"
        case suggested_github_app_install = "suggested_github_app_install"
        case app_env = "app_env"
        case non_production_bypasses = "non_production_bypasses"
        case needs_project_selection = "needs_project_selection"
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encodeIfPresent(email, forKey: .email)
        try c.encodeIfPresent(login, forKey: .login)
        try c.encodeIfPresent(avatar_url, forKey: .avatar_url)
        try c.encode(plan, forKey: .plan)
        try c.encode(is_admin, forKey: .is_admin)
        try c.encode(can_manage_subscription, forKey: .can_manage_subscription)
        try c.encode(suggested_github_app_install, forKey: .suggested_github_app_install)
        try c.encode(app_env, forKey: .app_env)
        try c.encode(needs_project_selection, forKey: .needs_project_selection)
        if AppEnvironment.exposeUserDebugFields {
            try c.encode(internal_pro_bypass, forKey: .internal_pro_bypass)
            try c.encode(non_production_bypasses, forKey: .non_production_bypasses)
        }
    }

    init(
        id: String,
        email: String?,
        login: String?,
        avatar_url: String?,
        plan: String,
        is_admin: Bool,
        internal_pro_bypass: Bool,
        can_manage_subscription: Bool,
        suggested_github_app_install: Bool,
        app_env: String,
        non_production_bypasses: Bool,
        needs_project_selection: Bool = false
    ) {
        self.id = id
        self.email = email
        self.login = login
        self.avatar_url = avatar_url
        self.plan = plan
        self.is_admin = is_admin
        self.internal_pro_bypass = internal_pro_bypass
        self.can_manage_subscription = can_manage_subscription
        self.suggested_github_app_install = suggested_github_app_install
        self.app_env = app_env
        self.non_production_bypasses = non_production_bypasses
        self.needs_project_selection = needs_project_selection
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        email = try c.decodeIfPresent(String.self, forKey: .email)
        login = try c.decodeIfPresent(String.self, forKey: .login)
        avatar_url = try c.decodeIfPresent(String.self, forKey: .avatar_url)
        plan = try c.decode(String.self, forKey: .plan)
        is_admin = try c.decodeIfPresent(Bool.self, forKey: .is_admin) ?? false
        internal_pro_bypass = try c.decodeIfPresent(Bool.self, forKey: .internal_pro_bypass) ?? false
        can_manage_subscription = try c.decodeIfPresent(Bool.self, forKey: .can_manage_subscription) ?? false
        suggested_github_app_install = try c.decodeIfPresent(Bool.self, forKey: .suggested_github_app_install) ?? false
        app_env = try c.decodeIfPresent(String.self, forKey: .app_env) ?? "prod"
        non_production_bypasses = try c.decodeIfPresent(Bool.self, forKey: .non_production_bypasses) ?? false
        needs_project_selection = try c.decodeIfPresent(Bool.self, forKey: .needs_project_selection) ?? false
    }
}

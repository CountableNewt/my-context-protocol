import Foundation
import Testing
import Vapor
import VaporTesting
@testable import App

@Suite("CLI authentication")
struct CliAuthenticationTests {
    private static let nonce = String(repeating: "a", count: 43)

    @Test("CLI callbacks require a canonical numeric loopback origin and strong nonce")
    func validateCallbacks() throws {
        for port in [1, 49152, 65535] {
            let callback = "http://127.0.0.1:\(port)/callback?state=\(Self.nonce)"
            #expect(try AuthController.validateCliReturnTo(callback) == callback)
        }
        let invalid = [
            "https://127.0.0.1:1234/callback?state=\(Self.nonce)",
            "http://localhost:1234/callback?state=\(Self.nonce)",
            "http://[::1]:1234/callback?state=\(Self.nonce)",
            "http://127.0.0.2:1234/callback?state=\(Self.nonce)",
            "http://127.0.0.1.evil.test:1234/callback?state=\(Self.nonce)",
            "http://127.0.0.1:0/callback?state=\(Self.nonce)",
            "http://127.0.0.1:65536/callback?state=\(Self.nonce)",
            "http://127.0.0.1/callback?state=\(Self.nonce)",
            "http://user@127.0.0.1:1234/callback?state=\(Self.nonce)",
            "http://127.0.0.1:1234/callback?state=\(Self.nonce)#fragment",
            "http://127.0.0.1:1234/other?state=\(Self.nonce)",
            "http://127.0.0.1:1234/%63allback?state=\(Self.nonce)",
            "http://127.0.0.1:1234/callback?state=short",
            "http://127.0.0.1:1234/callback?state=\(String(repeating: "a", count: 129))",
            "http://127.0.0.1:1234/callback?state=\(Self.nonce)&state=\(Self.nonce)",
            "http://127.0.0.1:1234/callback?state=\(Self.nonce)&redirect=https://evil.test",
            "http://127.0.0.1:1234/callback?state=\(Self.nonce)%0A",
            "http://127.0.0.1:1234/callback",
            "http://127.0.0.1:1234/callback?state=\(Self.nonce)\n",
        ]
        for callback in invalid {
            #expect(throws: Abort.self) { try AuthController.validateCliReturnTo(callback) }
        }
    }

    @Test("CLI GitHub login signs only validated loopback callbacks and preserves browser restrictions")
    func githubInitiation() async throws {
        try await TestProcessEnvGate.run {
            let overrides = [
                "GITHUB_CLIENT_ID": "cli-auth-test",
                "GITHUB_CLIENT_SECRET": "cli-auth-test-secret",
                "DISABLE_SKILL_USAGE_RETENTION_SCHEDULER": "1",
                "USE_MEMORY_SESSIONS": "1",
                "GITHUB_OAUTH_REDIRECT_URI": "https://api.example.test/auth/github/callback",
                "FRONTEND_URL": "https://app.example.test",
                "CORS_ORIGIN": "https://app.example.test",
                "ENCRYPTION_KEY": Data(repeating: 1, count: 32).base64EncodedString(),
            ]
            let original = Dictionary(uniqueKeysWithValues: overrides.keys.map {
                ($0, ProcessInfo.processInfo.environment[$0])
            })
            for (key, value) in overrides { setenv(key, value, 1) }
            defer {
                for key in overrides.keys {
                    if let value = original[key] ?? nil { setenv(key, value, 1) }
                    else { unsetenv(key) }
                }
            }
            let app = try await Application.make(.testing)
            try await configure(app)
            do {
                let callback = "http://127.0.0.1:49152/callback?state=\(Self.nonce)"
                var query = URLComponents()
                query.queryItems = [URLQueryItem(name: "return_to", value: callback)]
                let suffix = "?" + (query.percentEncodedQuery ?? "")
                try await app.testing().test(.GET, "/auth/cli/github" + suffix + "&prompt=select_account", afterResponse: { response in
                    #expect(response.status == .seeOther)
                    let location = try #require(response.headers.first(name: .location))
                    let url = try #require(URLComponents(string: location))
                    #expect(url.scheme == "https")
                    #expect(url.host == "github.com")
                    let items = url.queryItems ?? []
                    #expect(items.first(where: { $0.name == "prompt" })?.value == "select_account")
                    #expect(items.first(where: { $0.name == "redirect_uri" })?.value == "https://api.example.test/auth/github/callback")
                    let state = try #require(items.first(where: { $0.name == "state" })?.value)
                    #expect(try SignedOAuthState.verifyGitHubOAuth(state: state) == callback)
                })
                let account = Account(githubId: 928_001, login: "cli-login", email: "cli@example.test")
                try await account.save(on: app.db)
                let token = try await OAuthHandoffService.issue(accountId: account.id!, on: app.db)
                try await app.testing().test(.GET, "/auth/confirm?token=" + token, afterResponse: { response in
                    #expect(response.status == .ok)
                    #expect(response.headers.first(name: "X-Context-Frontend-Origin") == "https://app.example.test")
                    #expect(response.headers.first(name: .cacheControl) == "no-store")
                    #expect(response.headers.first(name: .setCookie)?.contains("vapor-session=") == true)
                })
                try await app.testing().test(.GET, "/auth/confirm?token=" + token, afterResponse: { response in
                    #expect(response.status == .unauthorized)
                    #expect(response.headers.first(name: "X-Context-Frontend-Origin") == nil)
                })
                // Exercise the real callback without contacting GitHub: a missing code must
                // return its error to the signed loopback URL, preserving the CLI nonce.
                var callbackQuery = URLComponents()
                callbackQuery.queryItems = [URLQueryItem(name: "state", value: try SignedOAuthState.signGitHubOAuth(returnTo: callback))]
                try await app.testing().test(.GET, "/auth/github/callback?" + (callbackQuery.percentEncodedQuery ?? ""), afterResponse: { response in
                    #expect(response.status == .seeOther)
                    #expect(response.headers.first(name: .location) == callback + "&error=missing_code")
                })
                for path in ["/auth/cli/github", "/auth/cli/github?return_to=https://evil.test", "/auth/cli/github" + suffix + "&prompt=consent", "/auth/github" + suffix] {
                    try await app.testing().test(.GET, path, afterResponse: { response in
                        #expect(response.status == .badRequest)
                        #expect(response.headers.first(name: .location) == nil)
                    })
                }
            } catch {
                try await app.asyncShutdown()
                throw error
            }
            try await app.asyncShutdown()
        }
    }
}

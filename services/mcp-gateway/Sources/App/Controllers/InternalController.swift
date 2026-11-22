import Fluent
import Vapor

/// Endpoints called by internal infrastructure — not exposed to end users.
/// Currently used by Caddy's `on_demand_tls` ask hook to gate TLS certificate issuance.
struct InternalController {
    /// GET /internal/custom-domain/verify-for-tls?domain=mcp.example.com
    ///
    /// Returns 200 if Caddy should provision a TLS certificate for `domain`, 422 otherwise.
    /// Two classes of domain are allowed:
    ///   1. Allocated project subdomains of `SAAS_MCP_BASE_DOMAIN`
    ///   2. A verified custom domain stored in the `projects` table
    ///
    /// Caddy normally calls this from localhost before completing a TLS handshake. Configure
    /// `INTERNAL_TLS_ASK_SECRET` if this route can be reached by anything else.
    static func verifyForTls(req: Request) async throws -> Response {
        if let expected = Environment.get("INTERNAL_TLS_ASK_SECRET")?
            .trimmingCharacters(in: .whitespacesAndNewlines), !expected.isEmpty {
            let actual = req.headers.first(name: "X-Internal-TLS-Ask-Secret")?
                .trimmingCharacters(in: .whitespacesAndNewlines)
            guard actual == expected else {
                return Response(status: .unauthorized)
            }
        }
        guard let domain = req.query[String.self, at: "domain"],
              !domain.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return Response(status: .unprocessableEntity)
        }
        guard let host = McpUrlBuilder.canonicalCustomDomainHost(domain) else {
            return Response(status: .unprocessableEntity)
        }

        // Allow allocated project subdomains of the MCP base domain.
        if let rawBase = Environment.get("SAAS_MCP_BASE_DOMAIN")?
            .trimmingCharacters(in: .whitespacesAndNewlines), !rawBase.isEmpty {
            let base = McpUrlBuilder.normalizedBaseDomain(rawBase)
            if host.hasSuffix("." + base) {
                let prefix = String(host.dropLast(base.count + 1))
                guard !prefix.isEmpty,
                      try await Project.query(on: req.db).filter(\.$subdomain == prefix).first() != nil else {
                    return Response(status: .unprocessableEntity)
                }
                return Response(status: .ok)
            }
        }

        // Allow verified custom domains — same filter pattern as TenantHostMiddleware.
        if let project = try await Project.query(on: req.db).filter(\.$customDomain == host).first(),
           project.customDomainVerifiedAt != nil {
            return Response(status: .ok)
        }

        req.logger.warning("verify-for-tls rejected domain=\(host)")
        return Response(status: .unprocessableEntity)
    }
}

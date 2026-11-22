import Foundation
import Vapor

/// Builds the public MCP HTTP endpoint URL (`{scheme}://{host}{path}`) from env + project.
enum McpUrlBuilder {
    /// Full URL for `POST /mcp`, preferring the verified custom domain.
    static func publicMcpUrl(for project: Project) -> String? {
        if let host = canonicalCustomDomainHost(project) {
            return build(host: host)
        }
        guard let sub = project.subdomain?.trimmingCharacters(in: .whitespacesAndNewlines), !sub.isEmpty,
              let baseRaw = Environment.get("SAAS_MCP_BASE_DOMAIN"),
              !baseRaw.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return nil
        }
        let base = normalizedBaseDomain(baseRaw)
        return build(host: "\(sub).\(base)")
    }

    static func canonicalCustomDomainHost(_ raw: String?) -> String? {
        guard var d = raw?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased(),
              !d.isEmpty else { return nil }
        while d.hasSuffix(".") { d.removeLast() }
        guard !d.isEmpty else { return nil }
        return d
    }

    static func canonicalCustomDomainHost(_ project: Project) -> String? {
        guard project.customDomainVerifiedAt != nil,
              let d = canonicalCustomDomainHost(project.customDomain) else { return nil }
        return d
    }

    static func normalizedBaseDomain(_ raw: String) -> String {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if s.hasPrefix("https://") { s = String(s.dropFirst(8)) }
        else if s.hasPrefix("http://") { s = String(s.dropFirst(7)) }
        while s.hasSuffix("/") { s.removeLast() }
        return s
    }

    private static func normalizedScheme() -> String {
        let raw = Environment.get("SAAS_MCP_URL_SCHEME")?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "https"
        if raw.isEmpty { return "https" }
        var s = raw.lowercased()
        if s.hasSuffix("://") { s = String(s.dropLast(3)) }
        if s.hasSuffix(":") { s.removeLast() }
        return s.isEmpty ? "https" : s
    }

    private static func normalizedPath() -> String {
        let parts = McpRoutePath.pathComponents()
        return "/" + parts.joined(separator: "/")
    }

    private static func build(host: String) -> String {
        "\(normalizedScheme())://\(host)\(normalizedPath())"
    }

    /// Origin for the tenant MCP host (no path), e.g. `https://sub.example.dev` or `https://custom.domain`.
    static func tenantOrigin(for project: Project) -> String? {
        if let host = canonicalCustomDomainHost(project) {
            return "\(normalizedScheme())://\(host)"
        }
        guard let sub = project.subdomain?.trimmingCharacters(in: .whitespacesAndNewlines), !sub.isEmpty,
              let baseRaw = Environment.get("SAAS_MCP_BASE_DOMAIN"),
              !baseRaw.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return nil
        }
        let base = normalizedBaseDomain(baseRaw)
        return "\(normalizedScheme())://\(sub).\(base)"
    }
}

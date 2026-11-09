import FluentSQLiteDriver
import Foundation
import SQLKit
import Vapor

enum DatabaseDriver: String, Sendable {
    case sqlite, postgres
}

enum SQLiteBootstrapError: Error, Equatable, LocalizedError {
    case unsupportedDriver(String)
    case deployedPathRequired
    case invalidPath
    case missingDeployedDatabase
    case writeAheadLoggingUnavailable

    var errorDescription: String? {
        switch self {
        case .unsupportedDriver(let value): "Unsupported DATABASE_DRIVER '\(value)'; use sqlite or postgres."
        case .deployedPathRequired: "SQLite in APP_ENV=dev or prod requires an absolute SQLITE_PATH on a persistent volume."
        case .invalidPath: "SQLITE_PATH must name a regular database file, not an in-memory database, URI, or directory."
        case .missingDeployedDatabase: "Deployed SQLite database is missing. Mount the migrated database at SQLITE_PATH; use SQLITE_ALLOW_CREATE=1 only when intentionally initializing an empty database."
        case .writeAheadLoggingUnavailable: "SQLite could not enable WAL; use a local persistent filesystem that supports SQLite locking."
        }
    }
}

enum SQLiteBootstrap {
    /// Existing Postgres deployments keep their database until an operator explicitly selects SQLite.
    static func driver(environment: [String: String] = ProcessInfo.processInfo.environment) throws -> DatabaseDriver {
        let explicit = environment["DATABASE_DRIVER"]?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() ?? ""
        if !explicit.isEmpty {
            guard let driver = DatabaseDriver(rawValue: explicit) else { throw SQLiteBootstrapError.unsupportedDriver(explicit) }
            return driver
        }
        if ["1", "true", "yes"].contains(environment["USE_SQLITE"]?.lowercased() ?? "") { return .sqlite }
        let postgresKeys = ["DATABASE_URL", "SUPABASE_DB_URL", "DATABASE_HOST", "DATABASE_USERNAME", "DATABASE_PASSWORD", "DATABASE_NAME", "DATABASE_PORT"]
        return postgresKeys.contains { !(environment[$0]?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true) }
            ? .postgres : .sqlite
    }

    static func path(raw: String?, deployKind: DeployAppEnv, workingDirectory: String) throws -> String {
        let configured = raw?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if deployKind != .local, !configured.hasPrefix("/") { throw SQLiteBootstrapError.deployedPathRequired }
        let path = configured.isEmpty ? "db.sqlite" : configured
        guard path != ":memory:", !path.hasPrefix("file:"), !path.hasSuffix("/"), !path.contains("\0") else {
            throw SQLiteBootstrapError.invalidPath
        }
        return URL(fileURLWithPath: path, relativeTo: URL(fileURLWithPath: workingDirectory, isDirectory: true)).standardizedFileURL.path
    }

    static func install(on app: Application, path: String, deployKind: DeployAppEnv = .local, allowCreate: Bool = false) async throws {
        var isDirectory: ObjCBool = false
        let exists = FileManager.default.fileExists(atPath: path, isDirectory: &isDirectory)
        if exists, isDirectory.boolValue {
            throw SQLiteBootstrapError.invalidPath
        }
        if !exists, deployKind != .local, !allowCreate { throw SQLiteBootstrapError.missingDeployedDatabase }
        let parent = URL(fileURLWithPath: path).deletingLastPathComponent()
        try FileManager.default.createDirectory(at: parent, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        // Foreign keys are enabled by SQLiteKit for every pooled connection, including reconnects.
        let configuration = SQLiteConfiguration(storage: .file(path: path), enableForeignKeys: true)
        app.databases.use(.sqlite(configuration, sqlLogLevel: DevLoggingConfig.sqliteSqlLogLevel), as: .sqlite)
        guard let sql = app.db as? any SQLDatabase,
              let row = try await sql.raw("PRAGMA journal_mode=WAL").first(),
              try row.decode(column: "journal_mode", as: String.self).lowercased() == "wal" else {
            throw SQLiteBootstrapError.writeAheadLoggingUnavailable
        }
        // WAL is persistent; keep SQLite's FULL synchronous default. SQLiteNIO supplies its own busy
        // handler on every connection. A one-off busy_timeout PRAGMA would affect only one pool member.
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path)
        app.logger.info("Using SQLite WAL database at \(path)")
    }
}

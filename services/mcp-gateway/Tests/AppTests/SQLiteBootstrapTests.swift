import Fluent
import Foundation
import SQLKit
import Testing
import Vapor
@testable import App

@Suite("SQLite database bootstrap", .serialized)
struct SQLiteBootstrapTests {
    @Test func driverSelectionPreservesExistingDeployments() throws {
        #expect(try SQLiteBootstrap.driver(environment: [:]) == .sqlite)
        #expect(try SQLiteBootstrap.driver(environment: ["DATABASE_URL": "postgres://existing/db"]) == .postgres)
        #expect(try SQLiteBootstrap.driver(environment: ["DATABASE_HOST": "postgres.railway.internal"]) == .postgres)
        #expect(try SQLiteBootstrap.driver(environment: ["DATABASE_PASSWORD": "partial-config"]) == .postgres)
        #expect(try SQLiteBootstrap.driver(environment: ["USE_SQLITE": "1", "DATABASE_URL": "postgres://existing/db"]) == .sqlite)
        #expect(try SQLiteBootstrap.driver(environment: ["DATABASE_DRIVER": "postgres", "USE_SQLITE": "1"]) == .postgres)
        #expect(try SQLiteBootstrap.driver(environment: ["DATABASE_DRIVER": " sqlite ", "DATABASE_HOST": "old-db"]) == .sqlite)
        #expect(throws: SQLiteBootstrapError.unsupportedDriver("sqltie")) {
            try SQLiteBootstrap.driver(environment: ["DATABASE_DRIVER": "sqltie"])
        }
    }

    @Test func deployedPathsRequireExplicitPersistentLocation() throws {
        #expect(try SQLiteBootstrap.path(raw: nil, deployKind: .local, workingDirectory: "/tmp/local/") == "/tmp/local/db.sqlite")
        #expect(try SQLiteBootstrap.path(raw: "data/app.sqlite", deployKind: .local, workingDirectory: "/tmp/local/") == "/tmp/local/data/app.sqlite")
        #expect(try SQLiteBootstrap.path(raw: "/data/app.sqlite", deployKind: .prod, workingDirectory: "/app/") == "/data/app.sqlite")
        for kind in [DeployAppEnv.dev, .prod] {
            for path in [nil, "", "db.sqlite"] as [String?] {
                #expect(throws: SQLiteBootstrapError.deployedPathRequired) {
                    try SQLiteBootstrap.path(raw: path, deployKind: kind, workingDirectory: "/app/")
                }
            }
        }
        for path in [":memory:", "file:memory?mode=memory", "/data/"] {
            #expect(throws: SQLiteBootstrapError.invalidPath) {
                try SQLiteBootstrap.path(raw: path, deployKind: .local, workingDirectory: "/app/")
            }
        }
    }

    @Test func deployedMissingFileFailsBeforeCreatingDirectories() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("sqlite-missing-\(UUID())")
        defer { try? FileManager.default.removeItem(at: root) }
        let app = try await Application.make(.testing)
        do {
            await #expect(throws: SQLiteBootstrapError.missingDeployedDatabase) {
                try await SQLiteBootstrap.install(on: app, path: root.appendingPathComponent("db.sqlite").path, deployKind: .prod)
            }
            #expect(!FileManager.default.fileExists(atPath: root.path))
            try await SQLiteBootstrap.install(on: app, path: root.appendingPathComponent("db.sqlite").path,
                deployKind: .prod, allowCreate: true)
            #expect(FileManager.default.fileExists(atPath: root.appendingPathComponent("db.sqlite").path))
            try await app.asyncShutdown()
        } catch {
            try await app.asyncShutdown()
            throw error
        }
    }

    @Test("Full schema, sessions, UUIDs and relationships survive a file database restart")
    func fileDatabaseSurvivesRestart() async throws {
        try await TestProcessEnvGate.run {
            let root = FileManager.default.temporaryDirectory.appendingPathComponent("sqlite-durable-\(UUID())")
            let path = root.appendingPathComponent("db.sqlite").path
            defer { try? FileManager.default.removeItem(at: root) }
            let overrides = [
                "APP_ENV": "local", "DATABASE_DRIVER": "sqlite", "SQLITE_PATH": path, "SQLITE_ALLOW_CREATE": "0",
                "USE_MEMORY_SESSIONS": "0", "LOG_LEVEL": "critical",
                "DISABLE_ADMIN_ANALYTICS_ROLLUP_SCHEDULER": "1", "DISABLE_STRIPE_RECONCILIATION_SCHEDULER": "1",
                "DISABLE_SKILL_USAGE_RETENTION_SCHEDULER": "1", "SEED_LOCAL_FIXTURES": "0",
            ]
            let saved = Dictionary(uniqueKeysWithValues: overrides.keys.map { ($0, ProcessInfo.processInfo.environment[$0]) })
            for (key, value) in overrides { setenv(key, value, 1) }
            defer {
                for key in overrides.keys {
                    if let value = saved[key] ?? nil { setenv(key, value, 1) } else { unsetenv(key) }
                }
            }
            let accountId = UUID()
            let compiledId = UUID()
            let canonicalJSON = #"{"id":"durable-skill","instructions":"Keep Unicode: café 🦉","policy":{"enabled":true}}"#
            let first = try await Application.make(.development)
            do {
                try await configure(first)
                let account = Account(id: accountId, githubId: 123_456_789, login: "sqlite-durability")
                try await account.save(on: first.db)
                let project = Project(accountId: accountId, name: "Durable", slug: "durable", subdomain: "durable-sqlite")
                try await project.save(on: first.db)
                let release = Release(projectId: project.id!, commitSha: "sqlite-durable", status: "ready")
                try await release.save(on: first.db)
                let package = SkillPackage(releaseId: release.id!, path: "durable/SKILL.md", name: "durable")
                try await package.save(on: first.db)
                let compiled = CompiledSkill(id: compiledId, releaseId: release.id!, skillPackageId: package.id!,
                    path: "durable/SKILL.md", name: "durable", skillBody: "# Durable\nKeep café 🦉", exposureType: "tool",
                    riskLevel: "low", repoSpecific: false, status: "ready")
                compiled.canonicalJson = canonicalJSON
                try await compiled.save(on: first.db)
                try await AppSessionRecord(sessionKey: "durability-session", payload: "{\"account_id\":\"\(accountId)\"}").save(on: first.db)
                for suffix in ["", "-wal", "-shm"] {
                    let attributes = try FileManager.default.attributesOfItem(atPath: path + suffix)
                    let permissions = try #require(attributes[.posixPermissions] as? NSNumber)
                    #expect(permissions.intValue & 0o777 == 0o600)
                }
                try await first.asyncShutdown()
            } catch {
                try await first.asyncShutdown()
                throw error
            }
            // Reopening through the deployed bootstrap also proves migration history is recognized.
            setenv("APP_ENV", "prod", 1)
            let second = try await Application.make(.development)
            do {
                try await configure(second)
                let account = try #require(try await Account.find(accountId, on: second.db))
                #expect(account.login == "sqlite-durability")
                #expect(account.createdAt != nil)
                let compiled = try #require(try await CompiledSkill.find(compiledId, on: second.db))
                #expect(compiled.canonicalJson == canonicalJSON)
                #expect(compiled.skillBody == "# Durable\nKeep café 🦉")
                #expect(!compiled.repoSpecific)
                #expect(try await Project.query(on: second.db).filter(\.$account.$id == accountId).count() == 1)
                #expect(try await AppSessionRecord.query(on: second.db).filter(\.$sessionKey == "durability-session").count() == 1)
                let sql = try #require(second.db as? any SQLDatabase)
                let journal = try #require(try await sql.raw("PRAGMA journal_mode").first())
                #expect(try journal.decode(column: "journal_mode", as: String.self) == "wal")
                let foreignKeys = try #require(try await sql.raw("PRAGMA foreign_keys").first())
                #expect(try foreignKeys.decode(column: "foreign_keys", as: Int.self) == 1)
                #expect(try await sql.raw("PRAGMA foreign_key_check").all().isEmpty)
                try await account.delete(on: second.db)
                #expect(try await Project.query(on: second.db).filter(\.$account.$id == accountId).count() == 0)
                try await second.asyncShutdown()
            } catch {
                try await second.asyncShutdown()
                throw error
            }
        }
    }

    @Test("Deployed boot rejects empty files and unrelated SQLite databases before migrations")
    func deployedRejectsUninitializedFiles() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("sqlite-uninitialized-\(UUID())")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        for content in [Data(), Data("unrelated text file".utf8)] {
            let path = root.appendingPathComponent("not-a-database.sqlite").path
            try content.write(to: URL(fileURLWithPath: path))
            let app = try await Application.make(.testing)
            do {
                await #expect(throws: SQLiteBootstrapError.uninitializedDeployedDatabase) {
                    try await SQLiteBootstrap.install(on: app, path: path, deployKind: .prod)
                }
                #expect(try Data(contentsOf: URL(fileURLWithPath: path)) == content)
                try await app.asyncShutdown()
            } catch {
                try await app.asyncShutdown()
                throw error
            }
        }
        let path = root.appendingPathComponent("unrelated.sqlite").path
        let creator = try await Application.make(.testing)
        do {
            try await SQLiteBootstrap.install(on: creator, path: path)
            let sql = try #require(creator.db as? any SQLDatabase)
            try await sql.raw("CREATE TABLE unrelated (id INTEGER PRIMARY KEY)").run()
            try await creator.asyncShutdown()
        } catch {
            try await creator.asyncShutdown()
            throw error
        }
        let deployed = try await Application.make(.testing)
        do {
            await #expect(throws: SQLiteBootstrapError.uninitializedDeployedDatabase) {
                try await SQLiteBootstrap.install(on: deployed, path: path, deployKind: .prod)
            }
            let sql = try #require(deployed.db as? any SQLDatabase)
            #expect(try await sql.raw("SELECT name FROM sqlite_master WHERE name = '_fluent_migrations'").all().isEmpty)
            try await sql.raw("CREATE TABLE accounts (id TEXT)").run()
            try await sql.raw("CREATE TABLE projects (id TEXT)").run()
            try await sql.raw("CREATE TABLE _fluent_migrations (name TEXT)").run()
            try await sql.raw("INSERT INTO _fluent_migrations (name) VALUES ('OtherApp.CreateAccounts')").run()
            try await deployed.asyncShutdown()
        } catch {
            try await deployed.asyncShutdown()
            throw error
        }
        let foreignHistory = try await Application.make(.testing)
        do {
            await #expect(throws: SQLiteBootstrapError.uninitializedDeployedDatabase) {
                try await SQLiteBootstrap.install(on: foreignHistory, path: path, deployKind: .prod)
            }
            try await foreignHistory.asyncShutdown()
        } catch {
            try await foreignHistory.asyncShutdown()
            throw error
        }
    }
}

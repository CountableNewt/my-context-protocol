import Fluent

/// Keep historical billing columns while restoring access to every existing project.
struct RemoveProjectPaymentSuspensions: AsyncMigration {
    func prepare(on database: Database) async throws {
        try await Project.query(on: database)
            .filter(\.$suspendedAt != .null)
            .set(\.$suspendedAt, to: nil)
            .update()
    }

    func revert(on database: Database) async throws {
        // A rollback must not suspend projects whose owners now rely on full access.
    }
}

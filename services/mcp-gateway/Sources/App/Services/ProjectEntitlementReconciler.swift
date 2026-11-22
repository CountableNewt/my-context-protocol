import Fluent
import Logging

/// Clears legacy payment-related project suspensions without changing account data.
enum ProjectEntitlementReconciler {
    static func reconcileProjects(for account: Account, db: Database, logger: Logger? = nil) async throws {
        try await Project.query(on: db)
            .filter(\.$account.$id == account.id!)
            .filter(\.$suspendedAt != .null)
            .set(\.$suspendedAt, to: nil)
            .update()
    }
}

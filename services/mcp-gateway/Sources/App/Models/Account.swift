import Fluent
import Vapor

final class Account: Model, Content {
    static let schema = "accounts"

    @ID(key: .id)
    var id: UUID?

    @Field(key: "github_id")
    var githubId: Int64

    @Field(key: "login")
    var login: String

    @OptionalField(key: "avatar_url")
    var avatarUrl: String?

    @OptionalField(key: "email")
    var email: String?

    @OptionalField(key: "github_token_encrypted")
    var githubTokenEncrypted: String?

    /// GitHub App installation id for this GitHub user (persists across sessions; see install callback).
    @OptionalField(key: "github_app_installation_id")
    var githubAppInstallationId: Int64?

    @OptionalField(key: "stripe_customer_id")
    var stripeCustomerId: String?

    @OptionalField(key: "stripe_subscription_id")
    var stripeSubscriptionId: String?

    /// Stripe subscription lifecycle: none, active, trialing, past_due, canceled, unpaid, incomplete, etc.
    @OptionalField(key: "subscription_status")
    var subscriptionStatus: String?

    /// Last time subscription status was confirmed with the Stripe API (for staleness checks).
    @OptionalField(key: "stripe_status_checked_at")
    var stripeStatusCheckedAt: Date?

    @Timestamp(key: "created_at", on: .create)
    var createdAt: Date?

    /// Platform admin (aggregate metrics + grant/revoke). Also via `INTERNAL_ADMIN_GITHUB_*` env bootstrap.
    @Field(key: "is_admin")
    var isAdmin: Bool

    /// Paywall / Pro feature bypass without Stripe or internal Pro env list.
    @Field(key: "paywall_bypass")
    var paywallBypass: Bool

    /// Set when `is_admin` becomes true; cleared when revoked.
    @OptionalField(key: "admin_granted_at")
    var adminGrantedAt: Date?

    /// Set when `paywall_bypass` becomes true; cleared when revoked.
    @OptionalField(key: "paywall_bypass_granted_at")
    var paywallBypassGrantedAt: Date?

    @Children(for: \.$account)
    var projects: [Project]

    init() {
        self.isAdmin = false
        self.paywallBypass = false
    }

    init(
        id: UUID? = nil,
        githubId: Int64,
        login: String,
        avatarUrl: String? = nil,
        email: String? = nil
    ) {
        self.id = id
        self.githubId = githubId
        self.login = login
        self.avatarUrl = avatarUrl
        self.email = email
        self.isAdmin = false
        self.paywallBypass = false
    }
}

extension Account: @unchecked Sendable {}

extension Account {
    /// Stripe subscription in good standing.
    var hasActiveProSubscription: Bool {
        let s = subscriptionStatus ?? "none"
        return s == "active" || s == "trialing"
    }

    /// Compatibility accessor for older integrations; every account has full access.
    var hasProEntitlements: Bool { true }

    var hasStripeCustomerRecord: Bool {
        guard let id = stripeCustomerId else { return false }
        return !id.isEmpty
    }
}

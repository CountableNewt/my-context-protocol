-- Generated from all Swift Fluent migrations on 2026-11-22; no account data.
-- Baseline for new Go/Turso databases. Never apply over existing application tables.

CREATE TABLE "_fluent_migrations" ("id" UUID PRIMARY KEY, "name" TEXT NOT NULL, "batch" INTEGER NOT NULL, "created_at" REAL, "updated_at" REAL, CONSTRAINT "uq:_fluent_migrations.name" UNIQUE ("name"));

CREATE TABLE "accounts" ("id" UUID PRIMARY KEY, "github_id" INTEGER NOT NULL, "login" TEXT NOT NULL, "avatar_url" TEXT, "email" TEXT, "created_at" REAL, "github_token_encrypted" TEXT, "stripe_customer_id" TEXT, "stripe_subscription_id" TEXT, "subscription_status" TEXT, "github_app_installation_id" INTEGER, "is_admin" INTEGER NOT NULL DEFAULT FALSE, "paywall_bypass" INTEGER NOT NULL DEFAULT FALSE, "admin_granted_at" REAL, "paywall_bypass_granted_at" REAL, "stripe_status_checked_at" REAL, CONSTRAINT "uq:accounts.github_id" UNIQUE ("github_id"));

CREATE TABLE "admin_analytics_hourly" ("id" UUID PRIMARY KEY, "hour_start" REAL NOT NULL, "request_count" INTEGER NOT NULL, "success_count" INTEGER NOT NULL, "latency_sum_ms" INTEGER NOT NULL, "latency_count" INTEGER NOT NULL, "updated_at" REAL, CONSTRAINT "uq:admin_analytics_hourly.hour_start" UNIQUE ("hour_start"));

CREATE TABLE "api_keys" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "key_prefix" TEXT NOT NULL, "key_hash" TEXT NOT NULL, "status" TEXT NOT NULL, "created_at" REAL, "last_used_at" REAL, "name" TEXT);

CREATE TABLE "app_sessions" ("id" UUID PRIMARY KEY, "session_key" TEXT NOT NULL, "payload" TEXT NOT NULL, "updated_at" REAL, CONSTRAINT "uq:app_sessions.session_key" UNIQUE ("session_key"));

CREATE TABLE "auth_configs" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "mode" TEXT NOT NULL, "settings_json" TEXT, "created_at" REAL);

CREATE TABLE "capability_defs" ("id" UUID PRIMARY KEY, "compiled_skill_id" UUID NOT NULL REFERENCES "compiled_skills" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "capability_name" TEXT NOT NULL, "type" TEXT NOT NULL, "schema_json" TEXT, "side_effect_level" TEXT NOT NULL, "created_at" REAL);

CREATE TABLE "compiled_skills" ("id" UUID PRIMARY KEY, "release_id" UUID NOT NULL REFERENCES "releases" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "skill_package_id" UUID NOT NULL REFERENCES "skill_packages" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "path" TEXT NOT NULL, "name" TEXT NOT NULL, "summary" TEXT, "exposure_type" TEXT NOT NULL, "risk_level" TEXT NOT NULL, "repo_specific" INTEGER NOT NULL, "status" TEXT NOT NULL, "created_at" REAL, "skill_body" TEXT, "body_diff_unified" TEXT, "body_diff_prior_release_id" UUID REFERENCES "releases" ("id") ON DELETE SET NULL ON UPDATE NO ACTION, "yaml_frontmatter_present" INTEGER NOT NULL DEFAULT TRUE, "canonical_schema_version" INTEGER NOT NULL DEFAULT 1, "skill_id" TEXT, "kind" TEXT, "scope" TEXT, "activation_mode" TEXT, "enforcement" TEXT, "priority" INTEGER, "version" TEXT, "source_checksum" TEXT, "canonical_json" TEXT, "clarification_json" TEXT, "clarification_required" INTEGER NOT NULL DEFAULT TRUE, "source_policy_json" TEXT, "source_policy_base_checksum" TEXT, "source_policy_stale" INTEGER NOT NULL DEFAULT FALSE);

CREATE TABLE "github_app_install_intents" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "account_id" UUID NOT NULL REFERENCES "accounts" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "return_to" TEXT, "owner" TEXT, "repo" TEXT, "expires_at" REAL NOT NULL);

CREATE TABLE "mcp_oauth_access_tokens" ("id" UUID PRIMARY KEY, "token_hash" TEXT NOT NULL, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "mcp_oauth_client_id" UUID NOT NULL REFERENCES "mcp_oauth_clients" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "account_id" UUID REFERENCES "accounts" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "subject_type" TEXT NOT NULL, "scope" TEXT NOT NULL, "expires_at" REAL NOT NULL, "revoked_at" REAL, "created_at" REAL, CONSTRAINT "uq:mcp_oauth_access_tokens.token_hash" UNIQUE ("token_hash"));

CREATE TABLE "mcp_oauth_authorization_codes" ("id" UUID PRIMARY KEY, "code_hash" TEXT NOT NULL, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "mcp_oauth_client_id" UUID NOT NULL REFERENCES "mcp_oauth_clients" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "account_id" UUID NOT NULL REFERENCES "accounts" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "redirect_uri" TEXT NOT NULL, "scope" TEXT NOT NULL, "code_challenge" TEXT NOT NULL, "code_challenge_method" TEXT NOT NULL, "expires_at" REAL NOT NULL, "consumed_at" REAL, "created_at" REAL, CONSTRAINT "uq:mcp_oauth_authorization_codes.code_hash" UNIQUE ("code_hash"));

CREATE TABLE "mcp_oauth_clients" ("id" UUID PRIMARY KEY, "public_client_id" TEXT NOT NULL, "client_secret_hash" TEXT, "is_confidential" INTEGER NOT NULL, "redirect_uris_json" TEXT NOT NULL, "allowed_grants" TEXT NOT NULL, "status" TEXT NOT NULL, "created_at" REAL, "project_id" UUID REFERENCES "projects" ("id") ON DELETE SET NULL ON UPDATE NO ACTION, CONSTRAINT "uq:mcp_oauth_clients.public_client_id" UNIQUE ("public_client_id"));

CREATE TABLE "mcp_oauth_pending_authorizations" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "mcp_oauth_client_id" UUID NOT NULL REFERENCES "mcp_oauth_clients" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "redirect_uri" TEXT NOT NULL, "state" TEXT NOT NULL, "scope" TEXT NOT NULL, "code_challenge" TEXT NOT NULL, "code_challenge_method" TEXT NOT NULL, "expires_at" REAL NOT NULL, "created_at" REAL);

CREATE TABLE "oauth_handoff_tokens" ("id" UUID PRIMARY KEY, "token" TEXT NOT NULL, "account_id" UUID NOT NULL REFERENCES "accounts" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "expires_at" REAL NOT NULL, "created_at" REAL, CONSTRAINT "uq:oauth_handoff_tokens.token" UNIQUE ("token"));

CREATE TABLE "project_runtime_settings" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "telemetry_enabled" INTEGER NOT NULL DEFAULT FALSE, "telemetry_retention_days" INTEGER NOT NULL DEFAULT 30, "semantic_enabled" INTEGER NOT NULL DEFAULT FALSE, "embedding_provider" TEXT, "embedding_model" TEXT, "feedback_issue_creation_enabled" INTEGER NOT NULL DEFAULT FALSE, "provider_preferences_json" TEXT, "updated_at" REAL, CONSTRAINT "uq:project_runtime_settings.project_id" UNIQUE ("project_id"));

CREATE TABLE "projects" ("id" UUID PRIMARY KEY, "account_id" UUID NOT NULL REFERENCES "accounts" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "name" TEXT NOT NULL, "slug" TEXT NOT NULL, "subdomain" TEXT, "active_release_id" UUID, "created_at" REAL, "custom_domain" TEXT, "custom_domain_verified_at" REAL, "custom_domain_verification_token" TEXT, "mcp_catalog_markdown_override" TEXT, "suspended_at" REAL, CONSTRAINT "uq_projects_account_slug" UNIQUE ("account_id", "slug"), CONSTRAINT "uq_projects_subdomain" UNIQUE ("subdomain"), CONSTRAINT "uq_projects_custom_domain" UNIQUE ("custom_domain"));

CREATE TABLE "releases" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "commit_sha" TEXT NOT NULL, "status" TEXT NOT NULL, "created_at" REAL, "error_summary" TEXT, "skill_body_changes_count" INTEGER NOT NULL DEFAULT 0);

CREATE TABLE "repo_connections" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "provider" TEXT NOT NULL, "repo_owner" TEXT NOT NULL, "repo_name" TEXT NOT NULL, "default_branch" TEXT NOT NULL, "auth_type" TEXT NOT NULL, "token_ref" TEXT, "webhook_id" TEXT, "created_at" REAL, "webhook_secret" TEXT, "token_encrypted" TEXT, "github_installation_id" INTEGER);

CREATE TABLE "request_logs" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "release_id" UUID REFERENCES "releases" ("id") ON DELETE SET NULL ON UPDATE NO ACTION, "timestamp" REAL, "client_id" TEXT, "method" TEXT NOT NULL, "latency_ms" INTEGER, "status" TEXT NOT NULL, "error_code" TEXT, "error_message" TEXT, "mcp_capability_kind" TEXT, "mcp_capability_key" TEXT);

CREATE TABLE "routing_rules" ("id" UUID PRIMARY KEY, "compiled_skill_id" UUID NOT NULL REFERENCES "compiled_skills" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "use_when_json" TEXT, "avoid_when_json" TEXT, "created_at" REAL, "failure_modes_json" TEXT, "invoke_first" INTEGER);

CREATE TABLE "skill_assignments" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "repo_connection_id" UUID REFERENCES "repo_connections" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "skill_id" TEXT NOT NULL, "scope" TEXT NOT NULL, "activation_mode" TEXT NOT NULL, "required" INTEGER NOT NULL, "priority" INTEGER NOT NULL, "created_at" REAL, "target_type" TEXT NOT NULL DEFAULT 'legacy_unscoped', "target_id" TEXT NOT NULL DEFAULT 'legacy_unscoped', CONSTRAINT "uq:skill_assignments.project_id+skill_assignments.repo_connection_id+skill_assignments.skill_id+skill_assignments.scope" UNIQUE ("project_id", "repo_connection_id", "skill_id", "scope"));

CREATE TABLE "skill_embeddings" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "skill_id" TEXT NOT NULL, "source_checksum" TEXT NOT NULL, "provider" TEXT NOT NULL, "model" TEXT NOT NULL, "vector_json" TEXT NOT NULL, "created_at" REAL, CONSTRAINT "uq:skill_embeddings.project_id+skill_embeddings.skill_id+skill_embeddings.provider+skill_embeddings.model+skill_embeddings.source_checksum" UNIQUE ("project_id", "skill_id", "provider", "model", "source_checksum"));

CREATE TABLE "skill_feedback" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "skill_id" TEXT NOT NULL, "skill_version" TEXT NOT NULL, "source_path" TEXT NOT NULL, "source_revision" TEXT, "category" TEXT NOT NULL, "summary" TEXT NOT NULL, "evidence" TEXT, "suggested_change" TEXT, "issue_draft_json" TEXT NOT NULL, "external_issue_url" TEXT, "created_at" REAL);

CREATE TABLE "skill_package_files" ("id" UUID PRIMARY KEY, "skill_package_id" UUID NOT NULL REFERENCES "skill_packages" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "path" TEXT NOT NULL, "content" BLOB NOT NULL, "content_type" TEXT, "byte_count" INTEGER NOT NULL, "checksum" TEXT NOT NULL, "created_at" REAL, CONSTRAINT "uq:skill_package_files.skill_package_id+skill_package_files.path" UNIQUE ("skill_package_id", "path"));

CREATE TABLE "skill_packages" ("id" UUID PRIMARY KEY, "release_id" UUID NOT NULL REFERENCES "releases" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "path" TEXT NOT NULL, "name" TEXT NOT NULL, "description" TEXT, "hash" TEXT, "validation_status" TEXT NOT NULL, "created_at" REAL);

CREATE TABLE "skill_runtime_events" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "trace_id" UUID NOT NULL, "event_type" TEXT NOT NULL, "skill_id" TEXT, "reason_code" TEXT, "score" DOUBLE, "request_hash" TEXT, "detail_json" TEXT NOT NULL, "created_at" REAL, "release_id" UUID, "skill_version" TEXT, "source_checksum" TEXT, "client_identity" TEXT, "source" TEXT);

CREATE TABLE "skill_runtime_overrides" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "repo_connection_id" UUID REFERENCES "repo_connections" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "skill_id" TEXT NOT NULL, "scope" TEXT NOT NULL, "metadata_json" TEXT NOT NULL, "source_checksum" TEXT, "writeback_pr_url" TEXT, "created_at" REAL, "updated_at" REAL, "base_checksum" TEXT, "is_stale" INTEGER NOT NULL DEFAULT FALSE, CONSTRAINT "uq:skill_runtime_overrides.project_id+skill_runtime_overrides.repo_connection_id+skill_runtime_overrides.skill_id+skill_runtime_overrides.scope" UNIQUE ("project_id", "repo_connection_id", "skill_id", "scope"));

CREATE TABLE "skill_usage_reports" ("id" UUID PRIMARY KEY, "project_id" UUID NOT NULL REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "client_identity" TEXT NOT NULL, "report_id" TEXT NOT NULL, "payload_hash" TEXT NOT NULL, "recorded_count" INTEGER NOT NULL, "created_at" REAL, CONSTRAINT "uq:skill_usage_reports.project_id+skill_usage_reports.client_identity+skill_usage_reports.report_id" UNIQUE ("project_id", "client_identity", "report_id"));

CREATE TABLE "tools_index" ("id" UUID PRIMARY KEY, "skill_package_id" UUID NOT NULL REFERENCES "skill_packages" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "tool_name" TEXT NOT NULL, "schema_json" TEXT, "handler_type" TEXT NOT NULL, "created_at" REAL);

CREATE TABLE "validation_reports" ("id" UUID PRIMARY KEY, "release_id" UUID NOT NULL REFERENCES "releases" ("id") ON DELETE CASCADE ON UPDATE NO ACTION, "report_json" TEXT NOT NULL, "created_at" REAL);

CREATE INDEX idx_api_keys_hash
ON api_keys (key_hash);

CREATE INDEX idx_compiled_skills_release_status
ON compiled_skills (release_id, status);

CREATE INDEX idx_request_logs_project_ts
ON request_logs (project_id, timestamp DESC);

CREATE INDEX skill_usage_project_time ON skill_runtime_events (project_id, created_at, skill_id);

CREATE INDEX skill_usage_trace_client ON skill_runtime_events (project_id, trace_id, client_identity);

CREATE UNIQUE INDEX uq_skill_assignments_target
ON skill_assignments(project_id, skill_id, scope, target_type, target_id);

CREATE UNIQUE INDEX uq_skill_overrides_project_fallback
ON skill_runtime_overrides(project_id, skill_id, scope)
WHERE repo_connection_id IS NULL;

CREATE UNIQUE INDEX uq_skill_overrides_repository
ON skill_runtime_overrides(project_id, repo_connection_id, skill_id, scope)
WHERE repo_connection_id IS NOT NULL;

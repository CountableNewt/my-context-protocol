# Vercel domain provider credentials

The provider always targets the fixed `VERCEL_PROJECT_ID` and explicit
`VERCEL_TEAM_ID`. Choose either `VERCEL_TOKEN` or all three integration variables;
mixed or partial credentials fail startup. Credentials stay server-side.

For a narrower credential than a personal/team token, create a connectable account
integration in Vercel's Integrations Console, keep it unlisted/community, and ask
for Project Read/Write and Domain Read/Write, plus the mandatory Integration
Configuration Read permission. Install it on the intended Hobby
team and select **only MyContextProtocol**, rather than all projects. Verify that
selection in Integrations → Manage → Manage Access.

This still grants substantial authority: project write includes project settings
and deletion; domain write includes account-level DNS, certificates, domain
deletion and registrar operations. Project selection is not documented as limiting
all account-level domain APIs. Approve these permissions explicitly before
creating/installing the integration. Do not describe it as domain-only access.
See [Vercel permissions](https://vercel.com/docs/integrations/install-an-integration/manage-integrations-reference)
and [scope definitions](https://vercel.com/docs/integrations/create-integration/vercel-api-integrations).

A temporary localhost callback utility can receive the installation code and
exchange it at `POST https://api.vercel.com/v2/oauth/access_token`, using the
registered redirect URI and integration credentials. No product login route is
required. Keep the returned installation ID and team ID; never log the code,
client secret or access token. Vercel explicitly supports localhost for testing
in its [redirect URL documentation](https://vercel.com/docs/integrations/create-integration/submit-integration).

Set `VERCEL_INTEGRATION_CLIENT_ID`, `VERCEL_INTEGRATION_CLIENT_SECRET` and
`VERCEL_INTEGRATION_INSTALLATION_ID` as production server credentials. Leave
`VERCEL_TOKEN` unset. New integrations issue only short-lived tokens. The provider
mints a token with HTTP Basic client authentication and form fields
`grant_type=client_credentials` and `installation_id`, then caches it until one
minute before expiry. Responses must match the configured team and installation.
Authentication errors fail closed; domain mutations are never automatically
replayed. See [short-lived tokens](https://vercel.com/docs/integrations/create-integration/short-lived-access-tokens).

Keep these credentials out of ordinary branch previews: project access spans the
single project's deployments and permission to mutate its live domains. A separate
deployment OIDC token is not a substitute for this Vercel REST API authentication.

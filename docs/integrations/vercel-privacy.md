# MyContextProtocol Vercel integration privacy notice

Effective November 22, 2026.

This notice covers the MyContextProtocol integration that connects a Vercel
installation to MyContextProtocol custom-domain management. It describes this
integration's data use, rather than the privacy practices of every other
MyContextProtocol feature or of Vercel itself.

## Data and purpose

The integration uses the Vercel installation, team and selected project
identifiers; its server-side client credentials and short-lived access tokens;
and custom-domain hostnames, ownership-verification records, DNS routing
recommendations and verification results. MyContextProtocol associates domain
configuration with the corresponding application project.

This information is used to authenticate requests to Vercel, add and inspect
project domains, verify ownership and routing, and show the required DNS records
and status to the project owner. Ownership checks also query public DNS TXT
records. The integration does not sell this information or use it for advertising.

The installation requests Project Read/Write and Domain Read/Write permissions.
These permissions grant broader authority than the domain operations implemented
by this integration. Selecting a project must not be understood as a guarantee
that every account-level domain permission is restricted to that project.

## Credentials and storage

Persistent production integration credentials must be configured as encrypted,
server-side Vercel environment variables, outside the public repository and
ordinary branch-preview configuration. The domain provider reads these
credentials from its environment; it does not store them in the application
database or return them to the dashboard.

The provider exchanges integration credentials for short-lived Vercel API access
tokens. It caches them in process memory until shortly before expiry and requests
replacement tokens when needed.
It checks the returned installation and team identifiers against its configured
values. Authentication failures are returned as errors; domain mutations are
not automatically replayed after an authentication failure.

Domain-to-project mappings, ownership challenges and verification state are
stored in the MyContextProtocol application database, using Turso in the Vercel
deployment. Vercel receives the credentials and domain information needed for
its authentication and domain APIs. Hosting, database and DNS providers process
the corresponding requests under their own applicable policies. This notice
does not promise a particular backup-retention or deletion period.

## Revoking access and requesting removal

You can manage or uninstall the integration through your Vercel account's
integration settings to revoke its installation authorization. Token revocation
and expiry are enforced by Vercel. The MyContextProtocol deployment's integration
credentials can also be removed to stop future token requests.

Uninstalling does not itself delete existing Vercel domain assignments, public
DNS records, or the domain configuration already saved in MyContextProtocol.
Those resources require separate removal. For questions or requests concerning
integration data or saved configuration, contact
[sam@theclementes.com](mailto:sam@theclementes.com).

## Software license

The integration implementation is part of the MyContextProtocol repository and
is available under its existing [MIT License](../../LICENSE), including the
license's warranty disclaimer. This notice does not introduce a separate
software license or replace Vercel's own terms.

# Migrate a project's repository provider

Open the project, select **Repo**, then choose **Migrate to GitHub** or
**Migrate to ATProto · Tangled**.

## Use an existing repository

Select a destination containing the project's Git history and choose its branch.
Link the destination provider in Account settings and grant repository access if
needed. Confirm the destination, then submit the migration.

## Create and copy a repository

Choose **Create and copy repository**. Open the destination importer, complete the
import there, then return and refresh the repository list before selecting it.

- [Tangled importer](https://tangled.org/repo/migrate) supports importing GitHub
  repositories, including private repositories after authorization.
- [GitHub Importer](https://github.com/new/import) can import reachable Git sources.
  Private Tangled authentication may require using a local Git copy instead.
- For a local copy, create an empty destination repository first. Use each
  provider's authenticated clone URLs, clone the source with `git clone --bare`,
  and push `refs/heads/*:refs/heads/*` and `refs/tags/*:refs/tags/*` to the
  destination. Transfer Git LFS objects separately when applicable. Avoid pushing
  provider-specific refs. Keep the source until the destination is verified.

The application guides the import and switches the project connection; the
provider or local Git performs the file transfer. Issues, pull requests and forge
settings require their own migration.

## What stays with the project

The project ID, MCP URL, API keys, settings and releases are retained. The current
release remains active. Use **Sync Now** after migration to publish destination
changes. Historical recovery uses the exact original commit, skill path and
checksum, so the destination must retain the project's Git history.

The destination's repository and branch access are verified before replacing the
source. Failed verification retains the source connection. Migration requires
the source provider, repository and branch to still match the reviewed values.
Finish running syncs and pending sync publication before migrating.

Changing repository providers does not authorize new public ATProto publication;
publication consent continues through the separate ATProto records review flow.

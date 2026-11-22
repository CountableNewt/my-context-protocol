# Railway compute retirement evidence — 2026-11-22

After the approved Vercel/Turso cutover and post-cutover backup verification, all six
MyContextProtocol Railway workloads were removed from compute. Verified at
**2026-11-22 22:18 UTC**: every deployment is `REMOVED`, `deploymentStopped=true`,
and every instance is exited/removed. A project-wide query found zero active,
queued, building, deploying, sleeping or approval-pending deployments.

| Environment | Workload | Retired deployment |
|---|---|---|
| Development | Web | `0c0e82a2-9bb2-4f41-8536-f7d1b51dc50c` |
| Development | Gateway | `5d095680-46ee-4ce6-8f74-12b67df5a87d` |
| Development | Postgres | `4c7e11d1-269d-4b5f-9c36-48ed59323052` |
| Production | Web | `04799afc-bc49-4826-90f9-dabb931708c7` |
| Production | Gateway maintenance | `fad15d64-72fc-4738-badc-ad4484c555a4` |
| Production | Postgres | `383a61f6-e12b-439f-82cc-5201ffe01751` |

Only deployment retirement and Postgres automatic image-update policy changes were
performed. Both Postgres image auto-updates are disabled, committed without deploying.
GitHub auto-deploys were already disabled. Project, environments, service definitions,
domains, variables, volumes and backup configuration were not deleted.

The two volume definitions and all four environment-specific volume instances remain
nondeleted, with their original service associations and mounts:

| Environment | Volume | Instance | Mount |
|---|---|---|---|
| Development | Postgres | `7fe37abc-fd8f-465d-b5c2-1231be97d708` | `/var/lib/postgresql/data` |
| Development | Gateway | `d453048e-1889-43fb-ab55-c06f5dfb13b2` | `/data` |
| Production | Postgres | `506bb06d-d4da-418f-b339-cb99905b7ba7` | `/var/lib/postgresql/data` |
| Production | Gateway | `dc45b43b-cd47-4810-9d43-a5c8f2c0da06` | `/data` |

All **25 existing volume backups** were independently rechecked after final teardown:
Development Postgres 1, Development Gateway 11, Production Postgres 2 and Production
Gateway 11. Existing backup IDs and schedules remain unchanged. Both Gateway volumes
retain daily, weekly and monthly schedules; Postgres had no scheduled backups.
Backups retain their existing expiry policies, and retained storage can still incur
Railway charges. Historical deployment images remain subject to plan retention.

The long-running removal API response timed out while Railway continued processing.
No mutation was blindly replayed: all six exact IDs and instance states were reconciled
until terminal removal, then project-wide active/queued compute and retained backups
were independently checked.

Do not restart the old Railway Gateway against its stale SQLite source after Vercel
writes. Any rollback requires pausing destination writers and a verified reverse
transfer. The original production Swift deployment had already been removed during
the controlled maintenance transition; its source volume and final private backups
were retained.

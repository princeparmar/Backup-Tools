# Teams — supported coverage matrix

Canonical names:

| UI service | Job method | Vault bucket |
|------------|------------|--------------|
| `teams` | `outlook_teams` | `outlook-teams` |

Organization / app-only only: **one tenant-level job** (`resource_type=tenant`). Personal/self Microsoft mode does not run Teams backup.

## What the Teams vault stores

**Backed up (Teams bucket)**

- Team and channel metadata (`_team.json`, `_channel.json`)
- Channel messages and replies (standard, private, and same-tenant hosted shared channels)
- Chat messages (per-user delta / history / sweep)
- Hosted content bytes referenced by messages (inline images, etc.)
- Team/channel config (members, apps, tags, tabs) when consented
- Online meeting **metadata** (calendar event, attendance reports when available)
- Call records / PSTN / direct-routing **metadata** when consented
- File **references** with OD/SP backup status (`storx_backup.files`, `_files_coverage.json`)

**Metadata only (not media in Teams vault)**

- Meeting transcript listings (`transcripts` on `_meeting.json`)
- Meeting recording listings (`recordings` on `_meeting.json`)
- Q&A conversation messages when `TEAMS_QA_ENABLED=true` (default **off**)

Recording/transcript **media** is not downloaded into the Teams bucket. File bytes are owned by **OneDrive / SharePoint** jobs. Teams links coverage only when a resolvable drive item exists and that owner backup’s state is known. Meeting metadata alone never means `backed_up`.

**Not collected**

- Cross-tenant shared-channel messages (reported in `shared_channel_message_coverage`, not silently skipped)
- Planner, Whiteboard, Loop, Lists, Wiki, bots as first-class collectors
- Rich restore (original author, original timestamp, reactions, full hosted fidelity)

## Channel coverage (`channel_coverage` on `_team.json`)

Per membership class (`standard`, `private`, `shared`):

| Status | Meaning |
|--------|---------|
| `not_verified` | Sync for that class has not completed a successful window yet |
| `verified` | A complete, successful sync window finished with no open gaps for that class |
| `incomplete` | Sync ran but the window/round is still open or closed with gaps |
| `failed` | Sync failed (retryable/stream error) |
| `inaccessible` | Permanent block (permission, policy, not found, source deleted) |

Shared channels also expose per-channel rows in `shared_channel_message_coverage` (`listed_per_channel`, `stored_under_host_team`, `not_collected`).

## Restore limitations

Channel and chat message restore is **repost-as-new** only: the restored message is posted as the restoring identity at restore time. Original author, original timestamps, message IDs, reactions, and hosted-content fidelity are **not** guaranteed. Document and surface this in UI copy.

Meeting recordings may appear under OneDrive/SharePoint restore, not under the Teams vault.

## Legacy per-team jobs — inventory and deletion safety

**Inventory (read-only)**

```sql
-- Active or paused outlook_teams jobs that are NOT tenant-scoped
SELECT id, user_id, tenant_id, resource_type, resource_id, status, sync_type
FROM cron_job_listings
WHERE method = 'outlook_teams'
  AND resource_type <> 'tenant'
  AND status IN ('active', 'paused', 'waiting', 'waiting_merge');
```

Also count merge candidates via `ListTeamJobsForMerge` for each tenant job.

**Do not delete `runOutlookTeamsTeamJob` / legacy merge paths until all of the following are true**

1. Zero (or explicitly agreed-zero) active/paused non-tenant `outlook_teams` jobs in production  
2. No pending merge / `waiting_merge` jobs that still depend on per-team job rows  
3. Restore path tests green for current tenant-prefix object keys  
4. Confirmed no production restore runbooks or ops scripts depend on legacy key layouts or per-team job IDs  

Zero active jobs alone is **not** sufficient evidence.

## Related

- File bytes: [onedrive-sharepoint-coverage.md](onedrive-sharepoint-coverage.md)
- Direct Graph download of transcript/recording media into the Teams vault requires a **separately approved** feasibility review (out of current scope).

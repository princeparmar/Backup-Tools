# Phase F — Production readiness audit (My files + SharePoint workspace)

Audit date: 2026-10-28  
Scope: My files OneDrive + personal Shared with me + SharePoint **site workspace** (libraries F0, lists P1, permissions/versions P2, pages P3; P4 unsupported until spikes).  
Statuses: `implemented` | `partial` | `missing` | `unverified`

## OneDrive (`outlook_onedrive`)

| Area | Requirement | Status | Notes |
|------|-------------|--------|-------|
| Backup completeness | Pagination | **implemented** | `DriveDeltaGuard` + page loop in `readDeltaApply` |
| | Throttling `429` | **partial** | `graphHTTPDoWithRetry` exists; full backoff matrix unverified in CI |
| | Transient errors | **partial** | Retry helper; not all error classes classified |
| | Expired tokens | **partial** | Tenant resolve refreshes; mid-run expiry unverified |
| | Interrupted jobs | **partial** | Heartbeat cancel; page checkpoint now avoids cursor skip (B2) |
| Data integrity | Size/checksum | **partial** | Size on meta; no separate checksum verify after upload |
| | Incomplete/corrupt detect | **missing** | No post-put hash compare |
| Deletions | Safe after failed scan | **implemented** | Sweep only after successful baseline; B2 tests |
| Restore safety | Conflicts / parents / retries | **partial** | Conflict rename; deleted parents / partial restore needs more tests |
| Large files | Streaming / timeouts | **partial** | Content stream + streaming upload path; large E2E unverified |
| Job operations | Cancel / resume / stuck | **partial** | Heartbeat + cron; stuck recovery unverified |
| Access/security | Token / isolation | **implemented** | Per-job tenant prefix; app vs delegated client |
| Observability | Counters / samples | **partial** | `input_data.onedrive_coverage` (B3); UI surfacing minimal |
| Testing | Personal + org matrix | **partial** | Unit/paging tests; live tenant E2E manual |
| Operations | Rate limits / retention | **unverified** | No dedicated ops dashboards in this repo |

## SharePoint (`outlook_sharepoint`) — site workspace

| Requirement | Status | Notes |
|-------------|--------|-------|
| Site / library discovery | **implemented** | Multi-drive list; incomplete listing fail-closed for inventory flag |
| Multiple libraries per site | **implemented** | Per-library cursors; default at site prefix; others under `lib/{driveId}/` |
| Preserve-all vault | **implemented** | No library-level BIN/purge on missing inventory (unit tested) |
| Coverage manifest | **implemented** | `input_data.sharepoint_coverage` with statuses + inventory flags + run window |
| Library vs list classify | **implemented** | `documentLibrary` → drive; other templates → list collector |
| Non-library lists | **implemented** | Defs/items/attachments; preserve-only; list cursors in task memory |
| Permissions snapshot | **partial** | Caller-visible only; `inventory_complete=false`; identity map fail-closed |
| Versions | **partial** | `discovered_only` until version content restore proven |
| Pages / news | **implemented** | Modern pages API + Site Pages list fallback; restore helper |
| Recycle / OneNote / workflows | **missing** | P4 spikes — see [onedrive-phase-d-spikes.md](./onedrive-phase-d-spikes.md) |
| Site-specific access failures | **partial** | Non-fatal per library; messaging via coverage + warning prefix |
| Restore to correct library | **implemented** | Requires `drive-id` meta; no silent Documents fallback |
| Non-atomic snapshot docs | **implemented** | Coverage + product docs state crash-consistent per type |
| Live multi-library smoke | **unverified** | Checklist: [sharepoint-multi-library-smoke.md](./sharepoint-multi-library-smoke.md) |

### Shared OneDrive engine audit (OD + SP)

Shared `oneDriveRun` / delta apply is used for SP libraries. F0 did **not** add new vault delete paths for SharePoint workspace types. Legacy OD per-file BIN on Graph delete may still run inside the shared engine for drive items — out of scope to change unless product decides otherwise.

## Supported / not-supported (publish)

See [onedrive-sharepoint-coverage.md](./onedrive-sharepoint-coverage.md).  
Extras / P4: [onedrive-phase-d-spikes.md](./onedrive-phase-d-spikes.md).

## Gate before calling scope “complete”

- [x] Coverage matrix published (workspace language)  
- [x] Phase D / P4 defaults documented (`unsupported` until collect+restore)  
- [x] Preserve-all + incomplete inventory unit tests  
- [x] Checkpoint + no failed-baseline sweep tested (OD)  
- [ ] Live personal + org backup/restore smoke on a real tenant  
- [ ] Graph `429` / interrupted-run drill  
- [ ] SharePoint multi-library restore smoke (checklist)

Until live checks are done, describe SharePoint as **coded workspace F0–P3 with honest coverage**, not as live-validated against every Phase F row. Phase F SharePoint live rows remain **unverified**.

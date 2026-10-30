# OneDrive / SharePoint — supported coverage matrix

Canonical names (do not invent a fourth):

| UI service | Job method | Vault bucket |
|------------|------------|--------------|
| `onedrive` | `outlook_onedrive` | `outlook-onedrive` |
| `sharepoint` | `outlook_sharepoint` | `outlook-sharepoint` |

## Product language

SharePoint jobs are a **site workspace** backup roadmap. Implemented slices must pass **collect → store → restore** before status `complete`. Discovery alone is `discovered_only` / `unsupported`.

**Preserve-all vault rule:** SharePoint workspace backup **never deletes, purges, or BIN-moves StorX objects** for library/list/page inventory gaps. Sync is add/update. (Legacy OneDrive drive-engine BIN for per-file Graph deletes may still exist in the shared engine; this plan does not expand library-level vault removal.)

**Non-atomic:** a site run is crash-consistent per content-type checkpoint, not a single point-in-time snapshot of the whole site. See `input_data.sharepoint_coverage` for `started_at` / `finished_at` and per-type windows.

---

## OneDrive (`outlook_onedrive`) — My files + personal Shared with me

**Supported**

- Personal (delegated `/me/drive`) and organization (app-only `/users/{mailbox}/drive`)
- Files and folders under the user’s My files drive
- **Personal Shared with me** (delegated `/me` only)
- Initial baseline + incremental Graph delta; Shared with me re-listed each run
- Unchanged skip via cTag / eTag
- Deletes from My files mirrored into product **BIN** (StorX) — OneDrive product path
- Restore of My files tree + legacy + BIN keys; SWM restores as owned copies into My files

**Not supported (Phase D)**

- Org/app-only Shared with me, shortcuts, permissions, versions, OneNote packages, M365 recycle sync, drive webhooks

Coverage: `input_data.onedrive_coverage`.

---

## SharePoint (`outlook_sharepoint`) — site workspace

### Implemented (F0 + collectors)

| Content type | Status | Notes |
|--------------|--------|-------|
| Document libraries (files/folders) | **Supported** | Multi-library delta; independent cursors; restore via `drive-id` meta; fail-safe if drive id missing |
| Library vs list classification | **Supported** | `documentLibrary` → drive only; `webPageLibrary` / Site Pages → pages only; other templates → list collector (no double-store) |
| Non-library lists / items / attachments | **Supported (P1)** | Definitions + items + attachments; preserve-only; lookups stored in fields JSON |
| Site + drive root permissions snapshot | **Partial (P2)** | Caller-visible only; `inventory_complete=false` always |
| File versions | **discovered_only (P2)** | API available; full version content restore not gated complete |
| Site pages / news | **Supported when Graph allows (P3)** | Modern `sitePage` + canvas when API works; fallback via Site Pages list |
| Site config (Graph subset) | **Supported (P3)** | `displayName` / description / webUrl snapshot at `{siteKey}/_site_config.json` — not full SP admin settings |
| Coverage manifest | **Supported** | `input_data.sharepoint_coverage` |

### Explicitly unsupported / P4

| Content type | Status |
|--------------|--------|
| Complete ACL restore with identity mapping | Partial collect; restore fail-closed helpers exist — full product restore UX TBD |
| MS recycle bin sync | Unsupported (P4 spike) |
| OneNote / packages | Unsupported (P4) |
| Workflows / Power Automate / SPFx / forms | Unsupported (P4) |
| Hub / subsite auto-crawl | Selecting a site does **not** imply children |

### Coverage statuses

`complete` | `partial` | `failed` | `unsupported` | `discovered_only`

Site `run_status`: `in_progress` | `complete` | `partial` | `failed`.

Site is **`partial` / job warning** only for real gaps in **document libraries, lists, or pages** (or incomplete library inventory). **Permissions** stay type-status `partial` and **versions** stay `discovered_only` without forcing a site-wide warning on every successful run.

### Inventory completeness

- Libraries: all drive pages succeeded and default drive present ⇒ `list_complete`.
- Permissions: **never** complete from Graph alone.
- Failed listing: keep known libraries in coverage; **never** purge vault.

---

## Product BIN vs Microsoft recycle bin

| | Product BIN (OneDrive engine) | M365 recycle bin |
|--|-------------------------------|------------------|
| What | Optional StorX layout for drive-item deletes | Tenant trash |
| SharePoint workspace plan | **Does not** BIN-remove whole libraries missing from inventory | Not synced (P4) |

Live smoke: [sharepoint-multi-library-smoke.md](./sharepoint-multi-library-smoke.md).

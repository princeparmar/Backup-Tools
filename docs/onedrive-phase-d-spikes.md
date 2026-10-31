# Phase D — Graph feasibility spikes (OneDrive extras + SharePoint P4)

**Rule:** do not mark a category backed up until backup **and** restore are tested.  
**Default until spike sign-off:** remain **out of scope** / `unsupported` / `discovered_only` (document in [onedrive-sharepoint-coverage.md](./onedrive-sharepoint-coverage.md)).

## OneDrive spike status (2026-10-28)

| Feature | Graph surface (research) | Scopes (typical) | Personal vs org notes | Decision |
|---------|--------------------------|------------------|----------------------|----------|
| Shared with me | `GET /me/drive/sharedWithMe`; content via `remoteItem` drive/item | `Files.Read.All` | **Personal /me:** implemented (backup + restore into My files). Org/app-only: still limited — verify per tenant | **Personal: supported.** Org/app-only: **out of scope** |
| Shortcuts / `remoteItem` | DriveItem `remoteItem` / `sharepointIds` **inside My files** | Same as files | Shortcuts may have no downloadable content on the host drive | **Out of scope** — stub meta vs follow-target TBD (distinct from Shared with me) |
| Permissions | `GET …/items/{id}/permissions` | `Files.Read.All` (+ Sites for SP) | Restore of ACLs is a separate hard problem | OD: **out of scope**. SP: partial collect (P2), identity fail-closed |
| Versions | `GET …/items/{id}/versions` + content | `Files.Read.All` | Storage multiplier; restore previous version needs product UX | OD: **out of scope**. SP: `discovered_only` until content restore proven |
| Packages / OneNote | `package` facet; OneNote export APIs | Often separate OneNote scopes | Today: skip + count in `onedrive_coverage.packages_skipped` | **Unsupported in UI** (explicit); no content backup |
| M365 recycle bin | Drive / site recycleBin APIs | Privileged | Product BIN already retains deleted-from-drive copies after successful delta | **Product BIN = retention**; no M365 recycle sync |
| Graph webhooks | Subscriptions on drive root | Change notifications + lifecycle | Complements schedule; not required for My files correctness | **Out of scope** for this phase |

## SharePoint P4 spikes (workspace) — 2026-10-28

Ship only after **collect → store → restore** on a test tenant. Until then coverage rows stay `unsupported` / `discovered_only`.

| Feature | Graph surface | Collect notes | Restore notes | Decision |
|---------|---------------|---------------|---------------|----------|
| Site / drive recycle bin | `GET /sites/{id}/recycleBin/items`, drive `recycleBin` | Privileged; pagination; second-stage bin | Restore vs product vault already holding deleted copies | **Spike required.** Prefer preserve-all vault over M365 recycle sync. Stay `unsupported` until proven. |
| OneNote / packages on site | `package` facet; OneNote section APIs; notebook drive items | Often separate scopes; large opaque packages | Openable notebook restore is hard | **Spike required.** Stay `unsupported` / packages skipped. |
| Workflows / Power Automate / forms / SPFx | Limited Graph; often SharePoint REST / Power Platform | Definitions may export as JSON; apps not portable | Re-binding connections/owners rarely automatic | **Spike required.** Stay `unsupported`. Manifest may set `discovered_only` if inventory APIs later find instances. |
| Hub sites / subsite crawl | Site hub navigation APIs | Selecting a site must **not** auto-backup children | N/A | **Out of scope** unless product adds explicit multi-site jobs |

### P4 spike checklist (per feature)

1. Document Graph/REST endpoints + required app roles  
2. Prototype collect into `{siteKey}/…` vault keys (preserve-only)  
3. Prototype restore on empty target site  
4. Record failure modes (permissions, size, non-portable apps)  
5. Only then flip coverage status and add CI/smoke  

Live smoke for libraries: [sharepoint-multi-library-smoke.md](./sharepoint-multi-library-smoke.md).

## Recommended order after any spike is approved

1. Org/app-only Shared with me (if product needs it; personal SWM already shipped)  
2. Shortcuts inside My files (product decision + restore story)  
3. SharePoint file version **content** restore (promote from `discovered_only`)  
4. SharePoint ACL restore UX (identity mapping already fail-closed)  
5. Packages / OneNote (OD + SP)  
6. Recycle-bin parity (only if product vault retention is insufficient)  
7. Workflows / forms / apps (likely never full fidelity)

## Implementation gate

For each approved feature open a dedicated PR that includes:

- Collector + storage key design  
- Restore path  
- Feature flag  
- Personal + org / site tests  
- Update to coverage matrix (supported → yes)

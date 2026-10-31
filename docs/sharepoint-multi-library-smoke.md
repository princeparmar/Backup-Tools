# SharePoint multi-library live smoke checklist

Manual gate (not CI). Use a test tenant site with the **default Documents library** plus **≥2 custom document libraries**, nested folders, and at least one non-library list.

## Preconditions

- Org app-only token with Sites.Read.All / Files.Read.All (and write scopes for restore).
- Job `outlook_sharepoint` with `site_id` + default `drive_id`.
- Note: backup is **not** an atomic whole-site snapshot; record job start/end from `input_data.sharepoint_coverage`.

## Steps

1. **Baseline** — Run job once. Confirm files land under site prefix (default) and `…/lib/{driveId}/` for custom libs. Confirm `sharepoint_coverage.list_complete=true` and per-library `status=complete`.
2. **Incremental** — Re-run unchanged. Confirm cTag skips (no re-download).
3. **Mutations** — Add/rename/change/delete files in each library. Re-run. Confirm vault has new/updated content. **Preserve-all:** deleted Microsoft files must still exist in StorX (no vault purge for missing libraries).
4. **Partial failure** — Deny access to one custom library (or break that drive). Confirm other libraries still sync; coverage shows failed library; site `run_status=partial`; warning message set.
5. **Incomplete inventory** — Simulate listing failure if possible. Confirm known libraries still sync; `list_complete=false`; **no** vault purge of “missing” libs.
6. **Restore** — Restore a file from default lib and from a custom lib. Confirm destination `drive_id` matches source (never silent fallback to Documents).
7. **Lists (P1)** — Confirm non-library list items under `{siteKey}/lists/…`; document libraries not double-stored as list items.
8. **Mid-run restart** — Cancel during sync; re-run. Confirm cursors did not skip unapplied work; coverage not marked whole-site complete while `in_progress`.

## Pass criteria

- [ ] Multi-library backup + restore to correct drives  
- [ ] Honest `sharepoint_coverage` (partial failures visible)  
- [ ] No false vault purge on incomplete/missing library inventory  
- [ ] Phase F SharePoint rows updated with date + tenant evidence  

Until this checklist is executed on a live tenant, Phase F SharePoint live validation remains **unverified**.

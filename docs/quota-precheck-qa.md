# Quota pre-check QA matrix

Manual / automated checks for backup storage + download bandwidth pre-check.

## Allow / block per service

| Case | Expected |
|------|----------|
| Drive estimate > remaining | Job blocked before upload; `failure_code=STORAGE_QUOTA`; Contacts/Calendar can still start if their buffers fit |
| Gmail sampled estimate fits | Backup starts |
| Contacts 1–50 MiB buffer fits | Backup starts |
| Calendar 5–20 MiB buffer fits | Backup starts |

## Mid-run

| Case | Expected |
|------|----------|
| Pre-check passed, later `storage limit exceeded` | Job fails/pauses with `STORAGE_QUOTA`; uploaded objects kept; Redis **not** cleared |
| Next schedule | Pre-check uses new remaining (likely blocks until upgrade/delete+tally) |

## Gmail sampling

| Case | Expected |
|------|----------|
| Large mailbox | Pre-check finishes within ~12s timeout; no full mailbox scan |

## UX

| Trigger | Expected |
|---------|----------|
| UI Backup Now blocked | Storage upgrade popup (required vs available) |
| Scheduled / background fail | Job log + `failure_code` only — **no** popup |
| Open failed job with `STORAGE_QUOTA` / `BANDWIDTH_QUOTA` | CTA → billing upgrade |
| Restore/export over bandwidth | `BANDWIDTH_QUOTA` + MailExport / restore failure |

## Satellite

| Check | Expected |
|-------|----------|
| `POST /api/v0/internal/project-usage-limits` | Same Redis-backed used/limit as hard check |
| `POST /api/v0/google-backup/quota-check` | Thin popup helper; no Redis writes |
| Hard path `checkUploadLimits` | Unchanged |

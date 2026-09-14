# Gmail all-mail backup — optimized plan

**Status:** ready to implement  
**Repos:** Backup-Tools (write) + CyberLS Gmail vault UI (read/filter)  
**No changes:** GatewayMT, Satellite S3 config  

### Hard rule — backup = ALL mail (no filter)

Backup-Tools cron must sync **every** message in the mailbox. No `CATEGORY_PERSONAL`, no other label filter, no Gmail `q=` query that excludes sections.

| Layer | Label filter? |
|---|---|
| **Backup cron** | **None** — empty label on `messages.list`; upload all |
| **Object key** | Stores label ids with `^` for browse |
| **CyberLS sidebar** | Filters display only (Inbox / More / Labels / All Mail) |

---

## 0. Architecture (do not confuse)

| System | Role |
|---|---|
| **Backup-Tools** | Fetch Gmail → uplink upload to bucket `gmail`. Dedup in Postgres `synced_objects`. |
| **Satellite** | Grants + encrypted object store. BT is a client only. |
| **GatewayMT** | S3 ListObjectsV2 / GetObject for CyberLS. Unchanged. |
| **CyberLS** (`storx-google`) | Lists via GatewayMT; **app filters** Inbox/Sent/… tabs. |

```text
WRITE:  Gmail API → Backup-Tools → uplink → bucket "gmail"
LIST:   CyberLS → GatewayMT ListObjectsV2 → same bucket
FILTER: CyberLS GmailContent (after list) — not S3 Prefix
```

**Mistakes removed from older drafts**

- No BT “section list API”
- No GatewayMT / Satellite changes for labels
- No `.ref` / `_by_label` sidecars
- No duplicate objects under `INBOX/` + `STARRED/`
- No hashing/truncating labels out of the key
- No assuming `Prefix=email/INBOX/` finds multi-label keys
- No S3 object tags (ListObjects cannot filter by tags; encryption makes tags useless)
- Separator is `^` on **label IDs** (not display names); sanitize `^` and `/` in ids before join

---

## 1. Current baseline

| Piece | Today |
|---|---|
| `GmailObjectKey` (`apps/google/gmail.go`) | `{email}/{yyyy}/{mm}/{dd}/{from} - {subject} - {messageId}.gmail` |
| Cron (`crons/gmail_processror.go`) | Lists `CATEGORY_PERSONAL` only; skips other labels; page size 500 |
| Dedup | `IsGmailMessageSynced` — exact key or any key under email ending `- {id}.gmail` |
| Title | `GenerateTitleFromGmailMessage` already strips `/` |
| CyberLS list | `listVaultObjects` + `UserSnapshotBrowserPanel` — flat list `Prefix=email/` |
| CyberLS tab filter | `GmailContent.tsx` uses `key.includes("/inbox/")` — **wrong for `^` keys**; must replace |

---

## 2. Target object key (BT write only)

```text
{email}/{labelsSegment}/{yyyy}/{mm}/{dd}/{from} - {subject} - {messageId}.gmail
```

Example:

```text
user@x.com/IMPORTANT^INBOX^STARRED/2026/03/10/Alice - Hello - abc123.gmail
```

- Constant: `gmailLabelsKeySeparator = "^"`
- Parse: `strings.Split(segment, "^")`
- Single constructor: only `GmailObjectKey(email, msg)` builds keys

### `labelsSegment` rules

1. Start from `msg.LabelIds`
2. Drop `UNREAD`, `CHAT` (keep in JSON body for restore)
3. Keep: `INBOX`, `SENT`, `SPAM`, `TRASH`, `STARRED`, `DRAFT`, `IMPORTANT`, `SNOOZED`, `SCHEDULED`, `CATEGORY_*`, `Label_*`
4. Sanitize each id: `/` and `^` → `_`
5. Dedupe → sort → join with `^`
6. If none left → `_`

S3 treats `IMPORTANT^INBOX^STARRED` as **one** path component. `^` has no S3 meaning.

---

## 3. S3 semantics (locked)

| Prefix | Result |
|---|---|
| `user@x.com/INBOX/` | No match for multi-label keys |
| `user@x.com/` | Returns all mail — then app filters |
| `user@x.com/IMPORTANT^INBOX^STARRED/` | Exact combo folder only |

One object can appear in multiple UI tabs (Inbox + Starred) without duplicating storage.

---

## 4. Optimized implementation order

### Phase A — Backup-Tools helpers + tests (first)

**File:** `apps/google/gmail.go` (+ `_test.go`)

| Func | Purpose |
|---|---|
| `GmailBackupLabelsForKey` | filter / sanitize / dedupe / sort |
| `GmailLabelsSegment` | join with `^` |
| `GmailObjectKey` | full path (updated) |
| `ParseGmailObjectKey` | email, labels, messageId, legacy flag |
| `ObjectKeyHasGmailLabel` | exact token match (FE contract) |
| `FindExistingGmailKeyByMessageID` | scan synced map by `- {id}.gmail` under email |

Update `IsGmailMessageSynced` to use find-by-id (true if any key exists for id).

**Tests (table-driven):** sort / drop UNREAD / empty → `_` / exact token / sanitize `^` / legacy vs new parse / find-by-id / stable key for same labels different API order.

### Phase B — Cron all-mail + key migration (**no backup filter**)

**File:** `crons/gmail_processror.go`

- List with **empty** label = **all mail** (Inbox, Sent, Spam, Trash, Drafts, Starred, user labels, …)
- **Remove** `CATEGORY_PERSONAL` list arg and any `labelIds` skip — do not filter at backup time
- Page size **100** (quota); bounded backoff on 429 / `rateLimitExceeded` / 5xx
- Persist next page token **only after** page finishes successfully

Sidebar / Inbox / Labels filters exist **only in CyberLS UI** after list — never in the cron.

**Per message:**

```text
expectedKey = GmailObjectKey(...)
existingKey = FindExistingGmailKeyByMessageID(...)

no existing     → upload expectedKey, add synced_objects
same key        → overwrite upload (body/draft may change)
different key   → upload NEW → update synced_objects → DeleteObject OLD
                  (never delete old before new upload succeeds)
```

### Phase C — Align other writers (same PR if touched)

If still on flat keys, use same `GmailObjectKey`:

- `handler/gmail_handlers.go`
- `tasks/scheduled_gmail_processor.go`

### Phase D — CyberLS Gmail vault UI (required)

**File:** `storx-google/components/ui/user-detail/browser/GmailContent.tsx`

Replace `key.includes(\`/${tabLower}/\`)` with `^` token match on the labels segment.

**Sidebar layout (mirror Gmail web):**

| Section | Items | Filter |
|---|---|---|
| Primary | Inbox, Starred, Snoozed, Sent, Drafts | Exact `INBOX` / `STARRED` / `SNOOZED` / `SENT` / `DRAFT` |
| More / Less (dropdown) | Important, Scheduled, All Mail, Spam, Bin | Exact system tokens; **All Mail** = no filter (show all) |
| Labels | User labels from vault keys (`Label_*`) | Exact `Label_<n>`; new labels appear after backup |

Legacy keys without labels segment → **All Mail** only.

Listing path unchanged: `ListObjectsV2(Prefix = "user@x.com/")` then filter in UI.

Display names for user labels: v1 may show `Label_12`; optional later id→name map.

Optional shared helper: `objectKeyHasGmailLabel(key, labelId)` mirroring BT.

### Phase E — Restore (minimal)

- Already key-agnostic download + Import
- Skip `.file_placeholder`
- Dedupe restore-all by JSON `message.Id` if legacy overlap
- Body `labelIds` = Gmail truth (includes UNREAD)

---

## 5. What we do **not** do

| Item | Why |
|---|---|
| GatewayMT changes | Generic S3 list already returns keys |
| S3 tags / metadata filter | ListObjects cannot filter by tags |
| Duplicate per-label objects | Waste + sync hell |
| Label display-name map (`users.labels.list`) | Optional later; v1 Labels section can show `Label_<n>` |
| BT browse/list API for sections | CyberLS owns sidebar + filter |

---

## 6. Acceptance checklist

- [x] Cron backs up **all** mail — no label filter at backup time
- [x] New mail keys contain sorted `^`-joined label ids
- [x] Label change → new key uploaded → old deleted only after success
- [x] Legacy keys still deduped by message id
- [x] CyberLS Inbox / Starred match `^` tokens (not `/inbox/` substrings)
- [x] Sidebar: primary + More/Less system labels + dynamic user Labels; All Mail shows all
- [x] New `Label_*` appear under Labels after backup
- [x] Same object appears in Inbox and Starred without two uploads
- [x] GatewayMT / Satellite untouched
- [x] Unit tests for helpers pass

---

## 7. File touch list (optimized)

| Repo | File | Change |
|---|---|---|
| Backup-Tools | `apps/google/gmail.go` | Key + helpers |
| Backup-Tools | `apps/google/gmail_*_test.go` | Table tests |
| Backup-Tools | `crons/gmail_processror.go` | All-mail + migrate keys |
| Backup-Tools | `handler/gmail_handlers.go` | Align key if still flat |
| Backup-Tools | `tasks/scheduled_gmail_processor.go` | Align key if still flat |
| CyberLS | `.../GmailContent.tsx` | `^` token filter + Gmail-like sidebar (primary / More / Labels) |
| — | gateway-mt / satellite | **None** |

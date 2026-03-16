# Corporate Gmail – Full Flow (Code)

End-to-end flow of how corporate Gmail backup works across the codebase.

---

## 1. Routes (router/server.go)

| Method | Route | Handler | Purpose |
|--------|--------|--------|--------|
| POST | `/auth/google/connect` | `HandleGoogleConnect` | UI sends OAuth code → backend returns encrypted token |
| GET | `/google/gmail/corporate/domain-users` | `HandleGmailCorporateDomainUsers` | List domain users + email counts (admin); needs JWT + Bearer token |
| POST | `/auto-sync/job/:method` (e.g. gmail) | `HandleAutomaticSyncCreate` | Create one or many backup jobs (body: `emails[]`, header: Bearer token) |
| POST | `/auto-sync/task/:job_id` | `HandleAutomaticSyncCreateTask` | Create one task for a job (one-time run) |

`/google/*` uses `JWTMiddleware`. `/auth/google/connect` and `/auto-sync/*` use their own auth (Bearer token or JWT as per existing app).

---

## 2. Step 1 – Connect (Get encrypted token)

**UI:** User signs in with Google OAuth, gets **authorization code**.

**Request:** `POST /auth/google/connect`  
Body: `{ "code": "<oauth_code>" }`

**Handler:** `handler/gmail_handlers.go` → `HandleGoogleConnect`

1. **apps/google/google-auth.go**  
   - `ExchangeCodeForTokenWithAdminScope(code)`  
   - Uses Gmail readonly + userinfo.email + `admin.directory.user.readonly`.  
   - Returns `access_token`, `refresh_token`.

2. **apps/google/google-auth.go**  
   - `GetGoogleAccountDetailsFromAccessToken(accessToken)`  
   - Gets `email` for the connected account.

3. **handler/gmail_handlers.go**  
   - Builds `googleTokenPayload{ AccessToken, RefreshToken, Email }`.  
   - `encryptGoogleTokenPayload(payload)`  
   - AES-256-GCM with key from env `GOOGLE_TOKEN_SECRET` (SHA256 hashed).  
   - Prepends `gts1.` and base64-encodes.

**Response:** `{ "token": "gts1.<base64>" }`  
Only the backend can decrypt this; UI stores the string and sends it in the next steps.

---

## 3. Step 2 – Domain users (optional, for admin UI)

**UI:** Sends the encrypted token so backend can show account type and, for admins, the list of domain users with counts.

**Request:** `GET /google/gmail/corporate/domain-users`  
Header: `Authorization: Bearer gts1.<encrypted_token>`  
(JWT also required by `JWTMiddleware` for `/google` group.)

**Handler:** `handler/gmail_handlers.go` → `HandleGmailCorporateDomainUsers`

1. **Token**  
   - `getGoogleTokenFromRequest(c)` → raw token.  
   - `parseGoogleToken(token)` → decrypts or decodes to get `accessToken`, `refreshToken`.  
   - `resolveAccessToken(accessToken, refreshToken)` → valid access token (refreshes if needed).

2. **Account type**  
   - `GetGoogleAccountDetailsFromAccessToken(accessToken)` → email.  
   - `google.IsUserAdmin(ctx, accessToken, email)` → admin or not.  
   - Sets `account_type`: `personal` | `employee_workspace` | `admin_workspace`.

3. **If not admin**  
   - Returns `{ "account_type", "email" }` only.

4. **If admin**  
   - `google.ExtractDomainFromEmail(email)` → domain.  
   - `google.ListAllDomainUsers(ctx, accessToken, domain)` (Admin SDK Directory API).  
   - **apps/google/gmail.go** `GetUserMessageCount(email)` for each user (Gmail API profile).  
   - Returns `{ "account_type", "account", "users": [{ "email", "email_count" }], "count" }`.

---

## 4. Step 3 – Job create (one job per account)

**UI:** User picks schedule (daily/one_time), optionally selects accounts from domain list (or own only).  
Sends same encrypted token + list of emails.

**Request:** `POST /auto-sync/job/gmail?sync_type=daily` (or one_time)  
Header: `Authorization: Bearer gts1.<encrypted_token>`  
Body: `{ "emails": ["user@domain.com", ...] }` or `{}` for own account only.

**Handler:** `handler/autobackup.go` → `HandleAutomaticSyncCreate` (method = gmail)

1. **Auth & token**  
   - JWT → `userID` (e.g. satellite.GetUserdetails).  
   - `GetGoogleCredentialsFromRequest(c)`  
   - Reads Bearer token, **decrypts** via `decryptGoogleTokenPayload`  
   - Returns `connectedEmail`, `accessToken`, `refreshToken`.

2. **Emails to create**  
   - `normalizeGmailEmails(reqBody.Emails, connectedEmail)`  
   - Empty → `[connectedEmail]`.  
   - Dedupes and validates; result = `toCreate`.

3. **Admin/domain (if any email ≠ connected)**  
   - `validateGmailAdminDomain(ctx, toCreate, connectedEmail, accessToken)`  
   - One `IsUserAdmin` call; domain must match for all target emails.

4. **Credential (one per connection)**  
   - **repo/oauth_credential_repository.go**  
   - `GetByUserIDAndSourceAndEmail(userID, "gmail", connectedEmail)`  
   - If missing → `Create` one row (UserID, Email=connectedEmail, Source=gmail, RefreshToken).  
   - So: one row in **db (postgres.go)** table `oauth_credentials` per (userID, gmail, connectedEmail).

5. **Jobs**  
   - **handler/autobackup.go** `createJobsForEmails(...)`  
   - For each email in `toCreate`:  
     - **repo/cron_job_repository.go** `CreateCronJobForUser(userID, name=targetEmail, method, syncType, config)`.  
     - `config` = `{ "credential_id": cred.ID, "email": targetEmail, "refresh_token": refreshToken }`.  
   - **repo/cron_job_repository.go** stores job with `input_data` JSONB = that config.  
   - If `sync_type == one_time`, **repo/task_repository.go** `CreateTaskForCronJob(job.ID)` per job.

6. **Response**  
   - `respondSyncCreate(c, syncType, success, failed, nil, nil)`  
   - `success`: `[{ "email", "job_id", "task_id"? }]`  
   - `failed`: `[{ "email", "error" }]`  
   - No job payload with tokens is returned (no `data` for Gmail bulk); tokens stay in DB/input_data.

---

## 5. Step 4 – Task create (one-time; if not already created)

**Request:** `POST /auto-sync/task/:job_id`  
Body: `{}` (no backup_email; one job = one account).

**Handler:** `handler/autobackup.go` → `HandleAutomaticSyncCreateTask`  
- Loads job by `job_id` and user.  
- **repo/task_repository.go** `CreateTaskForCronJob(job.ID)`  
- Task has no `backup_email`; account is identified by job’s `input_data.email` / job name.

---

## 6. Step 5 – Cron runs (backup execution)

**Entry:** Cron picks a **task** for a Gmail **job** (e.g. **crons/jobs.go** → Gmail processor).

**Processor:** `crons/gmail_processror.go` → `Run(ProcessorInput)`

1. **Refresh token**  
   - `input.Job.InputData` (JSONB):  
     - If `credential_id` present → **repo/oauth_credential_repository.go** `GetRefreshTokenByID(credential_id)` (table `oauth_credentials`).  
     - Else `input_data["refresh_token"]` (legacy).  
   - **apps/google/google-auth.go** `AuthTokenUsingRefreshToken(refreshToken)` → current access token.

2. **Gmail client**  
   - **apps/google/gmail.go** `NewGmailClientUsingToken(accessToken)`.

3. **User & path (one job = one account)**  
   - `userID` = `input_data["email"]` or `job.Name`; else `"me"`.  
   - `pathPrefix` = same (storage path = `pathPrefix/...`).

4. **Storage & list**  
   - `handler.UploadObjectAndSync(..., pathPrefix+"/.file_placeholder", ...)`.  
   - `handler.GetSyncedObjectsWithPrefix(..., pathPrefix+"/", ...)`.

5. **Fetch messages**  
   - **apps/google/gmail.go** `GetUserMessagesWithUserID(userID, nextPageToken, label, num, filter)`.  
   - For corporate, `userID` = that account’s email (domain-wide delegation).

6. **Upload**  
   - Each message stored under `pathPrefix/...` in Gmail bucket (e.g. Satellite).

So: **credential_id** → refresh_token from **db (postgres)** → access token → Gmail API with **email** (or "me") and path = **email** (or job name).

---

## 7. Scheduled tasks (alternative execution path)

**Entry:** Scheduled task system runs a Gmail **scheduled task** (different from cron job/task).

**Processor:** `tasks/scheduled_gmail_processor.go` → `Run(ScheduledTaskProcessorInput)`

- **Token:** Only **access_token** from `input.InputData["access_token"]` (no refresh/credential_id in this path).  
- **Corporate:**  
  - `userID` = `InputData["email"]` or `"me"`.  
  - `pathPrefix` = that email or `task.LoginId`.  
- **apps/google** Gmail client and `Users.Messages.Get(userID, emailID)`; storage under `pathPrefix/...`.

So scheduled path = same **corporate logic** (userID + pathPrefix from `email`), but **token** is still the old model (access_token from task data only).

---

## 8. Data and tables

- **oauth_credentials** (repo/oauth_credential_repository.go, db/postgres.go)  
  - One row per (user_id, source=gmail, email=connected account).  
  - Stores `refresh_token`; cron resolves by `credential_id` from job `input_data`.

- **Cron jobs** (repo/cron_job_repository.go)  
  - `input_data`: `credential_id`, `email` (account to backup), `refresh_token` (redundant with credentials table).  
  - Job name = that account’s email for Gmail.

- **Cron tasks** (repo/task_repository.go)  
  - One task per job run; optional `backup_email` on model (legacy); not used in new flow.

- **Response shape**  
  - All job-create responses: `{ "message", "success": [{ "email", "job_id", "task_id"? }], "failed": [{ "email", "error" }] }`.  
  - Single-job (Outlook/DB) also gets `data` (masked) and `task`.

---

## 9. File roles (summary)

| File | Role |
|------|------|
| **router/server.go** | Registers connect, domain-users, job create, task create. |
| **handler/gmail_handlers.go** | Connect (encrypt token), domain-users (admin list + counts), token helpers (decrypt, parse, resolve). |
| **handler/autobackup.go** | Job create: get creds from Bearer, normalize emails, validate admin, get/create credential, create jobs, respond success/failed. |
| **apps/google/google-auth.go** | Exchange code (with admin scope), refresh token → access token, IsUserAdmin, GetGoogleAccountDetailsFromAccessToken. |
| **apps/google/gmail.go** | Gmail client, GetUserMessagesWithUserID(userID, ...), GetUserMessageCount, ListAllDomainUsers (or used via google-auth). |
| **repo/oauth_credential_repository.go** | Create/GetByID/GetRefreshTokenByID/GetByUserIDAndSourceAndEmail for oauth_credentials. |
| **repo/cron_job_repository.go** | CreateCronJobForUser (stores input_data with credential_id, email, refresh_token). |
| **repo/task_repository.go** | CreateTaskForCronJob (one task per job; BackupEmail on task is legacy). |
| **db/postgres.go** | PostgresDb holds OAuthCredentialRepo; Migrate includes OAuthCredentialDB. |
| **crons/gmail_processror.go** | Resolves refresh_token from credential_id or input_data; userID/path from input_data email; calls Gmail API and uploads. |
| **tasks/scheduled_gmail_processor.go** | Uses access_token from InputData; corporate userID/path from InputData["email"]; same Gmail API and path layout. |

---

## 10. Flow diagram (ASCII)

```
UI                          Backend
│
│  OAuth code (Google)
├─────────────────────────► POST /auth/google/connect
│                           • ExchangeCodeForTokenWithAdminScope
│                           • encrypt(access, refresh, email)
│  ◄──────────────────────── { "token": "gts1...." }
│
│  Bearer gts1.... 
├─────────────────────────► GET /google/gmail/corporate/domain-users
│                           • decrypt → access/refresh
│                           • resolveAccessToken
│                           • IsUserAdmin → account_type
│                           • if admin: ListAllDomainUsers + GetUserMessageCount
│  ◄──────────────────────── { account_type, users?, count? }
│
│  Bearer gts1.... + emails[]
├─────────────────────────► POST /auto-sync/job/gmail
│                           • GetGoogleCredentialsFromRequest (decrypt)
│                           • getOrCreateOAuthCredential → cred_id
│                           • createJobsForEmails(cred_id, refresh_token, emails)
│                           • each: CreateCronJobForUser(..., config with credential_id, email)
│  ◄──────────────────────── { success: [{ email, job_id }], failed }
│
│  (optional) POST /auto-sync/task/:job_id
│                           • CreateTaskForCronJob
│
│  [Cron picks task for job]
│
│                           crons/gmail_processror
│                           • refresh_token from OAuthCredentialRepo.GetRefreshTokenByID(credential_id)
│                           • access = AuthTokenUsingRefreshToken(refresh_token)
│                           • userID = input_data["email"] or "me"
│                           • GetUserMessagesWithUserID(userID, ...)
│                           • upload to pathPrefix/...
```

This is the full flow as implemented in the code.

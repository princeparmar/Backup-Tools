package repo

import (
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
)

// BackupRestoreLogEntry is one row in GET /backup-restore/logs.
type BackupRestoreLogEntry struct {
	Type          string `json:"type"`
	Subject       string `json:"subject"`
	Method        string `json:"method"`
	Message       string `json:"message"`
	MessageStatus string `json:"message_status"`
}

// BackupRestoreLogsFilter query options for ListBackupRestoreLogs.
type BackupRestoreLogsFilter struct {
	IncludeBackup  bool
	IncludeRestore bool
	Search         string
	Method         string
	MessageStatus  string
	Limit          int
	Offset         int
}

// BackupRestoreLogsResult paginated log rows.
type BackupRestoreLogsResult struct {
	Logs       []BackupRestoreLogEntry
	TotalCount int64
}

// BackupRestoreLogsRepository reads merged backup + restore activity logs.
type BackupRestoreLogsRepository struct {
	db *gorm.DB
}

// NewBackupRestoreLogsRepository creates a backup/restore logs repository.
func NewBackupRestoreLogsRepository(db *gorm.DB) *BackupRestoreLogsRepository {
	return &BackupRestoreLogsRepository{db: db}
}

const (
	backupRestoreLogsDefaultLimit = 10
	backupRestoreLogsMaxLimit     = 100
)

const restoreLogMessageStatusSQL = `CASE rj.status
	WHEN 'partial_completed' THEN 'warning'
	WHEN 'failed' THEN 'error'
	WHEN 'cancelled' THEN 'error'
	ELSE 'info'
END`

// RestoreMessageStatusFromJobStatus maps restore job status to message_status.
func RestoreMessageStatusFromJobStatus(status string) string {
	switch strings.TrimSpace(status) {
	case RestoreJobStatusPartialCompleted:
		return JobMessageStatusWarning
	case RestoreJobStatusFailed, RestoreJobStatusCancelled:
		return JobMessageStatusError
	default:
		return JobMessageStatusInfo
	}
}

// ListBackupRestoreLogs returns merged backup + restore logs for a user.
func (r *BackupRestoreLogsRepository) ListBackupRestoreLogs(userID string, filter BackupRestoreLogsFilter) (*BackupRestoreLogsResult, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if !filter.IncludeBackup && !filter.IncludeRestore {
		return &BackupRestoreLogsResult{Logs: make([]BackupRestoreLogEntry, 0)}, nil
	}

	method := strings.TrimSpace(filter.Method)
	search := strings.TrimSpace(filter.Search)
	messageStatus := strings.TrimSpace(filter.MessageStatus)

	cap := 0
	if filter.IncludeBackup {
		cap++
	}
	if filter.IncludeRestore {
		cap++
	}
	parts := make([]string, 0, cap)
	args := make([]interface{}, 0, 16)
	arg := func(v interface{}) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if filter.IncludeBackup {
		backupWhere := []string{
			"cj.user_id = " + arg(userID),
			"COALESCE(cj.placeholder, false) = false",
			"cj.deleted_at IS NULL",
			"(cj.last_run IS NOT NULL OR TRIM(COALESCE(cj.message, '')) <> '')",
		}
		if method != "" {
			backupWhere = append(backupWhere, "cj.method = "+arg(method))
		}
		if messageStatus != "" {
			backupWhere = append(backupWhere, "COALESCE(NULLIF(TRIM(cj.message_status), ''), 'info') = "+arg(messageStatus))
		}
		if search != "" {
			pat := "%" + search + "%"
			backupWhere = append(backupWhere, "(COALESCE(NULLIF(TRIM(cj.input_data->>'email'), ''), TRIM(cj.name)) ILIKE "+arg(pat)+" OR cj.message ILIKE "+arg(pat)+")")
		}

		parts = append(parts, fmt.Sprintf(`SELECT
	'backup' AS log_type,
	COALESCE(NULLIF(TRIM(cj.input_data->>'email'), ''), TRIM(cj.name)) AS subject,
	cj.method AS method,
	COALESCE(cj.message, '') AS message,
	COALESCE(NULLIF(TRIM(cj.message_status), ''), 'info') AS message_status,
	COALESCE(cj.last_run, cj.updated_at) AS sort_ts
FROM cron_job_listing_dbs cj
WHERE %s`, strings.Join(backupWhere, " AND ")))
	}

	if filter.IncludeRestore {
		restoreInnerWhere := []string{
			"rj.user_id = " + arg(userID),
			"rj.deleted_at IS NULL",
			"(TRIM(COALESCE(rj.message, '')) <> '' OR rj.status IN ('queued','running','completed','partial_completed','failed','cancelled'))",
		}
		if method != "" {
			restoreInnerWhere = append(restoreInnerWhere, "rj.method = "+arg(method))
		}
		if search != "" {
			pat := "%" + search + "%"
			restoreInnerWhere = append(restoreInnerWhere, "(rj.login_id ILIKE "+arg(pat)+" OR rj.message ILIKE "+arg(pat)+")")
		}

		restoreOuterWhere := ""
		if messageStatus != "" {
			restoreOuterWhere = "WHERE rs.message_status = " + arg(messageStatus)
		}

		parts = append(parts, fmt.Sprintf(`SELECT
	'restore' AS log_type,
	rs.subject,
	rs.method,
	rs.message,
	rs.message_status,
	rs.sort_ts
FROM (
	SELECT
		rj.login_id AS subject,
		rj.method AS method,
		COALESCE(rj.message, '') AS message,
		%s AS message_status,
		COALESCE(rj.updated_at, rj.created_at) AS sort_ts
	FROM restore_job_listing_dbs rj
	WHERE %s
) rs
%s`, restoreLogMessageStatusSQL, strings.Join(restoreInnerWhere, " AND "), restoreOuterWhere))
	}

	merged := strings.Join(parts, "\nUNION ALL\n")

	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM (%s) merged", merged)
	var total int64
	if err := r.db.Raw(countSQL, args...).Scan(&total).Error; err != nil {
		return nil, fmt.Errorf("count backup restore logs: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = backupRestoreLogsDefaultLimit
	}
	if limit > backupRestoreLogsMaxLimit {
		limit = backupRestoreLogsMaxLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	listArgs := append(append([]interface{}{}, args...), limit, offset)
	limitArg := fmt.Sprintf("$%d", len(args)+1)
	offsetArg := fmt.Sprintf("$%d", len(args)+2)

	listSQL := fmt.Sprintf(`SELECT log_type, subject, method, message, message_status
FROM (%s) merged
ORDER BY sort_ts DESC
LIMIT %s OFFSET %s`, merged, limitArg, offsetArg)

	type row struct {
		LogType       string
		Subject       string
		Method        string
		Message       string
		MessageStatus string
	}
	var rows []row
	if err := r.db.Raw(listSQL, listArgs...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list backup restore logs: %w", err)
	}

	logs := make([]BackupRestoreLogEntry, 0, len(rows))
	for i := range rows {
		logs = append(logs, BackupRestoreLogEntry{
			Type:          rows[i].LogType,
			Subject:       rows[i].Subject,
			Method:        rows[i].Method,
			Message:       rows[i].Message,
			MessageStatus: rows[i].MessageStatus,
		})
	}

	return &BackupRestoreLogsResult{
		Logs:       logs,
		TotalCount: total,
	}, nil
}

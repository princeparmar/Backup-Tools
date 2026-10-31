package googlestore

import (
	"strings"

	"github.com/StorX2-0/Backup-Tools/restore"
)

// WriteEmail returns the mailbox email used for Google write (OAuth or DWD subject).
func WriteEmail(d *restore.RestoreDeps) string {
	if d == nil {
		return ""
	}
	if s := strings.TrimSpace(d.GoogleWriteSubject); s != "" {
		return s
	}
	return strings.TrimSpace(d.LoginID)
}

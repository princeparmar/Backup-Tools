package restore

import "strings"

// GoogleWriteEmail returns the mailbox email used for Google write (OAuth or DWD subject).
func (d *RestoreDeps) GoogleWriteEmail() string {
	if d == nil {
		return ""
	}
	if s := strings.TrimSpace(d.GoogleWriteSubject); s != "" {
		return s
	}
	return strings.TrimSpace(d.LoginID)
}

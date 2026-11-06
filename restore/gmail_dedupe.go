package restore

import (
	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// DedupeGmailRestoreRows keeps one synced row per Gmail message id when legacy + labeled
// keys coexist. Prefers non-legacy (labeled) keys.
func DedupeGmailRestoreRows(rows []repo.SyncedObject) []repo.SyncedObject {
	if len(rows) <= 1 {
		return rows
	}
	type pick struct {
		row    repo.SyncedObject
		legacy bool
		msgID  string
		hasID  bool
	}
	best := map[string]pick{}
	var order []string
	passthrough := make([]repo.SyncedObject, 0)

	for _, row := range rows {
		parsed, ok := google.ParseGmailObjectKey(row.ObjectKey)
		if !ok || parsed.MessageID == "" {
			passthrough = append(passthrough, row)
			continue
		}
		id := parsed.MessageID
		cur, exists := best[id]
		if !exists {
			best[id] = pick{row: row, legacy: parsed.Legacy, msgID: id, hasID: true}
			order = append(order, id)
			continue
		}
		if cur.legacy && !parsed.Legacy {
			best[id] = pick{row: row, legacy: false, msgID: id, hasID: true}
		}
	}

	out := make([]repo.SyncedObject, 0, len(order)+len(passthrough))
	for _, id := range order {
		out = append(out, best[id].row)
	}
	out = append(out, passthrough...)
	return out
}

package restore

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const maxSelectedRestoreKeys = 200

// SelectedRestoreKeyPrefix is the vault prefix selected keys must sit under.
// Google: {email}/. Microsoft: {tenant}/{type}/{resource_id}/ from the backup job.
// Always lowercased so NormalizeSelectedRestoreKeys matches case-insensitively.
func SelectedRestoreKeyPrefix(prep *ReadinessResult, cronJob *repo.CronJobListingDB) string {
	if prep == nil {
		return ""
	}
	login := strings.TrimSpace(prep.LoginID)
	method, ok := APIServiceToMethod[APIService(strings.TrimSpace(strings.ToLower(prep.Service)))]
	prefix := ""
	if ok {
		prefix = RestoreKeyPrefix(&repo.RestoreJobListingDB{Method: method, LoginID: login}, cronJob)
	}
	if prefix == "" && login != "" {
		prefix = login + "/"
	}
	if prefix == "" {
		return ""
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return strings.ToLower(prefix)
}

// NormalizeSelectedRestoreKeys trims, de-dupes, and keeps keys under keyPrefix.
// Empty input means restore-all (every vault item). Base64 vault keys from the old
// manual body are decoded. Keys outside keyPrefix are dropped.
// keyPrefix is SelectedRestoreKeyPrefix (email/ or tenant/user/oid/).
func NormalizeSelectedRestoreKeys(keyPrefix string, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	prefix := strings.ToLower(strings.TrimSpace(keyPrefix))
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, raw := range keys {
		key := normalizeSelectedKey(raw)
		if key == "" || ShouldSkipObjectKey(key) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(key), prefix) {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
		if len(out) > maxSelectedRestoreKeys {
			return nil, fmt.Errorf("at most %d selected keys per restore job", maxSelectedRestoreKeys)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no selected keys belong to this mailbox")
	}
	return out, nil
}

func normalizeSelectedKey(raw string) string {
	key := strings.TrimSpace(raw)
	if key == "" {
		return ""
	}
	if strings.Contains(key, "/") {
		return key
	}
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return key
	}
	out := strings.TrimSpace(string(decoded))
	if !strings.Contains(out, "/") {
		return key
	}
	return out
}

// JobSelectedKeys is the optional key list on a restore-all job. Empty means all items.
func JobSelectedKeys(job *repo.RestoreJobListingDB) []string {
	if job == nil || job.SelectedKeys == nil || job.SelectedKeys.Json() == nil {
		return nil
	}
	return append([]string(nil), (*job.SelectedKeys.Json())...)
}

func selectedKeysValue(keys []string) *database.DbJson[[]string] {
	if len(keys) == 0 {
		return nil
	}
	return database.NewDbJsonFromValue(keys)
}

// SelectedKeyPage returns the next selected keys after cursor (1-based last id).
func SelectedKeyPage(keys []string, afterID uint, limit int) []repo.SyncedObject {
	if limit <= 0 {
		limit = 25
	}
	return selectedKeyRange(keys, afterID, afterID+uint(limit))
}

// SelectedKeyRange returns keys with 1-based id in (startID, endID].
func SelectedKeyRange(keys []string, startID, endID uint) []repo.SyncedObject {
	return selectedKeyRange(keys, startID, endID)
}

func selectedKeyRange(keys []string, startID, endID uint) []repo.SyncedObject {
	if endID <= startID || len(keys) == 0 {
		return nil
	}
	start := int(startID)
	end := int(endID)
	if start < 0 {
		start = 0
	}
	if end > len(keys) {
		end = len(keys)
	}
	if start >= end {
		return nil
	}
	rows := make([]repo.SyncedObject, 0, end-start)
	for i := start; i < end; i++ {
		row := repo.SyncedObject{ObjectKey: keys[i]}
		row.ID = uint(i + 1)
		rows = append(rows, row)
	}
	return rows
}

func selectedKeysDrained(job *repo.RestoreJobListingDB) bool {
	keys := JobSelectedKeys(job)
	if len(keys) == 0 {
		return false
	}
	return job != nil && job.CursorID >= uint(len(keys))
}

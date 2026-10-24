package outlook

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Mail backups are one object per message, named like Gmail backups:
//
//	{prefix}/{folderPath}/{yyyy}/{mm}/{dd}/{from} - {subject} - {conversationId} - {messageId}.outlook
//
// prefix is ResourceKeyPrefix. The object is the Graph message JSON (body, recipients and
// attachments) plus a "storx_backup" block with the folder it was in at the last sync.
const outlookMailFileExt = ".outlook"

// Long subjects and senders are cut so keys stay well below object key limits.
const (
	mailKeyFromMaxRunes    = 100
	mailKeySubjectMaxRunes = 150
)

const mailKeySep = " - "

// OutlookMailObject is a parsed mail backup key.
type OutlookMailObject struct {
	Prefix         string
	FolderPath     string
	DatePath       string
	From           string
	Subject        string
	ConversationID string
	MessageID      string
}

// OutlookMailObjectKey builds the backup key of one message.
func OutlookMailObjectKey(prefix, folderPath, receivedTime, from, subject, conversationID, messageID string) string {
	parts := []string{strings.TrimSuffix(strings.TrimSpace(prefix), "/")}
	if fp := strings.Trim(strings.TrimSpace(folderPath), "/"); fp != "" {
		parts = append(parts, fp)
	}
	parts = append(parts, objectKeyDatePath(receivedTime), OutlookMailFileName(from, subject, conversationID, messageID))
	return strings.Join(parts, "/")
}

// OutlookMailFileName is the key leaf: "{from} - {subject} - {conversationId} - {messageId}.outlook".
// The two ids never contain spaces, so they are read back from the end; the sender never
// contains " - ", so whatever follows the first separator is the subject.
func OutlookMailFileName(from, subject, conversationID, messageID string) string {
	messageID = mailKeyID(messageID)
	conversationID = mailKeyID(conversationID)
	if conversationID == "" {
		conversationID = messageID
	}
	from = strings.ReplaceAll(mailKeyText(from, "unknown", mailKeyFromMaxRunes), mailKeySep, "-")
	subject = mailKeyText(subject, "(no subject)", mailKeySubjectMaxRunes)
	return from + mailKeySep + subject + mailKeySep + conversationID + mailKeySep + messageID + outlookMailFileExt
}

func mailKeyText(s, empty string, maxRunes int) string {
	s = strings.NewReplacer("/", "_", "\\", "_").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxRunes]))
	}
	if s == "" {
		return empty
	}
	return s
}

func mailKeyID(id string) string {
	return strings.NewReplacer("/", "_", "\\", "_", " ", "").Replace(strings.TrimSpace(id))
}

// ParseOutlookMailObjectKey parses a mail backup key written by OutlookMailObjectKey.
func ParseOutlookMailObjectKey(key string) (OutlookMailObject, bool) {
	prefix, _, _, _, rest, ok := SplitResourceKey(strings.TrimSpace(key))
	if !ok || !strings.HasSuffix(rest, outlookMailFileExt) {
		return OutlookMailObject{}, false
	}
	parts := strings.Split(rest, "/")
	n := len(parts)
	if n < 4 {
		return OutlookMailObject{}, false
	}
	yyyy, mm, dd := parts[n-4], parts[n-3], parts[n-2]
	if !allDigits(yyyy, 4) || !allDigits(mm, 2) || !allDigits(dd, 2) {
		return OutlookMailObject{}, false
	}
	name := strings.TrimSuffix(parts[n-1], outlookMailFileExt)
	i := strings.LastIndex(name, mailKeySep)
	if i < 0 {
		return OutlookMailObject{}, false
	}
	messageID := name[i+len(mailKeySep):]
	name = name[:i]
	j := strings.LastIndex(name, mailKeySep)
	if j < 0 || messageID == "" {
		return OutlookMailObject{}, false
	}
	conversationID := name[j+len(mailKeySep):]
	from, subject, _ := strings.Cut(name[:j], mailKeySep)
	return OutlookMailObject{
		Prefix:         prefix,
		FolderPath:     strings.Join(parts[:n-4], "/"),
		DatePath:       yyyy + "/" + mm + "/" + dd,
		From:           from,
		Subject:        subject,
		ConversationID: conversationID,
		MessageID:      messageID,
	}, true
}

// OutlookMailLegacyKey is a key of the old two-object layout:
// {prefix}/meta/{yyyy/mm/dd}/{messageId}.json with the message at the same path under data/.
type OutlookMailLegacyKey struct {
	Prefix    string
	IsMeta    bool
	DatePath  string
	MessageID string
}

// ParseOutlookMailLegacyKey parses old meta/data mail keys.
func ParseOutlookMailLegacyKey(key string) (OutlookMailLegacyKey, bool) {
	parts := strings.Split(strings.TrimSpace(key), "/")
	n := len(parts)
	if n < 6 || (parts[n-5] != "meta" && parts[n-5] != "data") {
		return OutlookMailLegacyKey{}, false
	}
	yyyy, mm, dd := parts[n-4], parts[n-3], parts[n-2]
	if !allDigits(yyyy, 4) || !allDigits(mm, 2) || !allDigits(dd, 2) {
		return OutlookMailLegacyKey{}, false
	}
	file := parts[n-1]
	id := strings.TrimSuffix(file, ".json")
	if id == file || id == "" || strings.HasPrefix(file, ".") {
		return OutlookMailLegacyKey{}, false
	}
	return OutlookMailLegacyKey{
		Prefix:    strings.Join(parts[:n-5], "/"),
		IsMeta:    parts[n-5] == "meta",
		DatePath:  yyyy + "/" + mm + "/" + dd,
		MessageID: id,
	}, true
}

// OutlookMailLegacyDataKey returns the data object of a legacy mail key ("" if not legacy).
func OutlookMailLegacyDataKey(key string) string {
	k, ok := ParseOutlookMailLegacyKey(key)
	if !ok {
		return ""
	}
	return k.Prefix + "/data/" + k.DatePath + "/" + k.MessageID + ".json"
}

// IsOutlookMailBackupKey reports whether key is a mail backup in the current or legacy layout.
func IsOutlookMailBackupKey(key string) bool {
	if _, ok := ParseOutlookMailObjectKey(key); ok {
		return true
	}
	_, ok := ParseOutlookMailLegacyKey(key)
	return ok
}

func allDigits(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// outlookMailBackupStateField holds OutlookMailBackupState inside a mail backup object.
const outlookMailBackupStateField = "storx_backup"

// OutlookMailBackupState records where the message was at the last sync. Read, flag and
// category state are kept in the Graph fields of the same object.
type OutlookMailBackupState struct {
	FolderID           string `json:"folder_id,omitempty"`
	FolderPath         string `json:"folder_path,omitempty"`
	WellKnownFolder    string `json:"well_known_folder,omitempty"`
	RemovedFromMailbox bool   `json:"removed_from_mailbox,omitempty"`
	RemovedAt          string `json:"removed_at,omitempty"`
	BackedUpAt         string `json:"backed_up_at,omitempty"`
}

// ReadOutlookMailBackupState returns the backup state stored in a mail backup object.
func ReadOutlookMailBackupState(raw []byte) OutlookMailBackupState {
	var doc struct {
		State OutlookMailBackupState `json:"storx_backup"`
	}
	_ = json.Unmarshal(raw, &doc)
	return doc.State
}

// PatchOutlookMailBackup applies the mailbox state from delta (when msg is not nil) and the
// backup state to a stored message. An empty state.BackedUpAt keeps the stored one.
func PatchOutlookMailBackup(raw []byte, msg *OutlookMailDeltaMessage, state OutlookMailBackupState) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse mail backup: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("parse mail backup: not an object")
	}
	set := func(field string, v interface{}) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		doc[field] = b
		return nil
	}
	if msg != nil {
		flag := map[string]interface{}{}
		if cur, ok := doc["flag"]; ok {
			_ = json.Unmarshal(cur, &flag)
			if flag == nil {
				flag = map[string]interface{}{}
			}
		}
		if msg.FlagStatus != "" {
			flag["flagStatus"] = msg.FlagStatus
		}
		categories := msg.Categories
		if categories == nil {
			categories = []string{}
		}
		fields := map[string]interface{}{"isRead": msg.IsRead, "categories": categories, "flag": flag}
		if msg.Importance != "" {
			fields["importance"] = msg.Importance
		}
		if msg.InferenceClassification != "" {
			fields["inferenceClassification"] = msg.InferenceClassification
		}
		if msg.ParentFolderID != "" {
			fields["parentFolderId"] = msg.ParentFolderID
		}
		for k, v := range fields {
			if err := set(k, v); err != nil {
				return nil, err
			}
		}
	}
	if state.BackedUpAt == "" {
		state.BackedUpAt = ReadOutlookMailBackupState(raw).BackedUpAt
	}
	if err := set(outlookMailBackupStateField, state); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

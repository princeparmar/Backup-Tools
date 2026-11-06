package outlook

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Contact photo is stored inside the contact JSON under this field so restore can strip it from
// the Graph create payload and PUT it separately after the contact exists.
const contactPhotoBackupField = "storx_contact_photo"

const contactPhotoMaxBytes = 4 << 20

// ContactPhotoBackup is the photo bytes embedded in a contact backup object.
type ContactPhotoBackup struct {
	ContentType string `json:"content_type,omitempty"`
	Data        string `json:"data"` // standard base64
}

// ContactPhotoURL is GET …/contacts/{id}/photo/$value (folder-scoped when folderID set).
func ContactPhotoURL(userBase, folderID, contactID string) string {
	base := strings.TrimRight(userBase, "/")
	contactID = url.PathEscape(strings.TrimSpace(contactID))
	if folderID = strings.TrimSpace(folderID); folderID != "" {
		return base + "/contactFolders/" + url.PathEscape(folderID) + "/contacts/" + contactID + "/photo/$value"
	}
	return base + "/contacts/" + contactID + "/photo/$value"
}

// FetchContactPhoto downloads a contact photo. ok is false when the contact has no photo (404).
func FetchContactPhoto(ctx context.Context, accessToken, userBase, folderID, contactID string) (photo ContactPhotoBackup, ok bool, err error) {
	reqURL := ContactPhotoURL(userBase, folderID, contactID)
	body, status, err := graphExchange(ctx, accessToken, http.MethodGet, reqURL, nil, nil, contactPhotoMaxBytes+1)
	if err != nil {
		return ContactPhotoBackup{}, false, err
	}
	if status == http.StatusNotFound {
		return ContactPhotoBackup{}, false, nil
	}
	if status < 200 || status >= 300 {
		return ContactPhotoBackup{}, false, fmt.Errorf("contact photo http %d: %s", status, truncateForErr(body))
	}
	if len(body) == 0 || len(body) > contactPhotoMaxBytes {
		return ContactPhotoBackup{}, false, fmt.Errorf("contact photo size %d invalid", len(body))
	}
	ct := http.DetectContentType(body)
	if ct == "application/octet-stream" {
		ct = "image/jpeg"
	}
	return ContactPhotoBackup{ContentType: ct, Data: base64.StdEncoding.EncodeToString(body)}, true, nil
}

// PutContactPhoto uploads a contact photo after create. Empty data is a no-op.
func PutContactPhoto(ctx context.Context, accessToken, userBase, folderID, contactID string, photo ContactPhotoBackup) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(photo.Data))
	if err != nil {
		return fmt.Errorf("decode contact photo: %w", err)
	}
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > contactPhotoMaxBytes {
		return fmt.Errorf("contact photo too large (%d bytes)", len(raw))
	}
	ct := strings.TrimSpace(photo.ContentType)
	if ct == "" {
		ct = http.DetectContentType(raw)
	}
	extra := make(http.Header)
	extra.Set("Content-Type", ct)
	body, status, err := graphExchange(ctx, accessToken, http.MethodPut, ContactPhotoURL(userBase, folderID, contactID), bytes.NewReader(raw), extra, 1<<20)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("put contact photo http %d: %s", status, truncateForErr(body))
	}
	return nil
}

// EmbedContactPhoto adds or replaces the photo block on a Graph contact JSON document.
func EmbedContactPhoto(raw []byte, photo ContactPhotoBackup) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("parse contact for photo: %w", err)
	}
	b, err := json.Marshal(photo)
	if err != nil {
		return nil, err
	}
	doc[contactPhotoBackupField] = b
	return json.Marshal(doc)
}

// ContactPhotoChecked reports whether the vault object already recorded a photo fetch
// (embedded bytes or null when Graph returned 404). Missing field means we should retry.
func ContactPhotoChecked(raw []byte) bool {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	_, ok := doc[contactPhotoBackupField]
	return ok
}

// MarkContactPhotoChecked records that photo was looked up and none exists (Graph 404).
func MarkContactPhotoChecked(raw []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("parse contact for photo check: %w", err)
	}
	doc[contactPhotoBackupField] = json.RawMessage("null")
	return json.Marshal(doc)
}

// ReadContactPhotoBackup extracts an embedded photo from a contact backup (ok=false if none).
func ReadContactPhotoBackup(raw []byte) (ContactPhotoBackup, bool) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return ContactPhotoBackup{}, false
	}
	block, ok := doc[contactPhotoBackupField]
	if !ok || len(block) == 0 || string(block) == "null" {
		return ContactPhotoBackup{}, false
	}
	var photo ContactPhotoBackup
	if json.Unmarshal(block, &photo) != nil || strings.TrimSpace(photo.Data) == "" {
		return ContactPhotoBackup{}, false
	}
	return photo, true
}

// StripContactPhotoBackup returns contact JSON without the vault photo field (for Graph create).
func StripContactPhotoBackup(raw []byte) ([]byte, ContactPhotoBackup, bool) {
	photo, ok := ReadContactPhotoBackup(raw)
	if !ok {
		return raw, ContactPhotoBackup{}, false
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return raw, photo, true
	}
	delete(doc, contactPhotoBackupField)
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, photo, true
	}
	return out, photo, true
}

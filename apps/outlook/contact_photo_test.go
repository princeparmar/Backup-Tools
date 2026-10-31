package outlook

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestEmbedAndReadContactPhoto(t *testing.T) {
	raw := []byte(`{"id":"C1","displayName":"Ann"}`)
	photo := ContactPhotoBackup{ContentType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("png"))}
	out, err := EmbedContactPhoto(raw, photo)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ReadContactPhotoBackup(out)
	if !ok || got.ContentType != "image/png" || got.Data != photo.Data {
		t.Fatalf("photo %+v ok=%v", got, ok)
	}
	stripped, extracted, has := StripContactPhotoBackup(out)
	if !has || extracted.Data != photo.Data {
		t.Fatal("strip failed")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(stripped, &doc); err != nil {
		t.Fatal(err)
	}
	if _, still := doc[contactPhotoBackupField]; still {
		t.Fatal("photo field still present after strip")
	}
	payload, err := ContactCreatePayload(stripped)
	if err != nil {
		t.Fatal(err)
	}
	if payload["displayName"] != "Ann" {
		t.Fatalf("payload %+v", payload)
	}
}

func TestContactsDeltaURL(t *testing.T) {
	def := ContactsDeltaURL("https://graph.microsoft.com/v1.0/me", "")
	if def != "https://graph.microsoft.com/v1.0/me/contacts/delta" {
		t.Fatalf("default url %s", def)
	}
	folder := ContactsDeltaURL("https://graph.microsoft.com/v1.0/me", "F1")
	if folder != "https://graph.microsoft.com/v1.0/me/contactFolders/F1/contacts/delta" {
		t.Fatalf("folder url %s", folder)
	}
}

func TestContactPhotoURL(t *testing.T) {
	if got := ContactPhotoURL("https://g/me", "", "C1"); got != "https://g/me/contacts/C1/photo/$value" {
		t.Fatalf("default %s", got)
	}
	if got := ContactPhotoURL("https://g/me", "F1", "C1"); got != "https://g/me/contactFolders/F1/contacts/C1/photo/$value" {
		t.Fatalf("folder %s", got)
	}
}

func TestContactPhotoCheckedMarker(t *testing.T) {
	raw := []byte(`{"id":"C1"}`)
	if ContactPhotoChecked(raw) {
		t.Fatal("unchecked")
	}
	marked, err := MarkContactPhotoChecked(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !ContactPhotoChecked(marked) {
		t.Fatal("should be checked")
	}
	if _, ok := ReadContactPhotoBackup(marked); ok {
		t.Fatal("null marker must not count as a photo")
	}
}

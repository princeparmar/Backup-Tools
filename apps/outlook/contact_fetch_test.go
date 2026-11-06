package outlook

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContactCreatePayloadKeepsAnniversary(t *testing.T) {
	raw := []byte(`{
		"id":"c1","displayName":"Bhago","jobTitle":"developer",
		"emailAddresses":[{"address":"dhavalder93@gmail.com"}],
		"businessPhones":["9313405704"],"spouseName":"nothing else",
		"categories":["test"],"personalNotes":"no any fine","imAddresses":["uh"],
		"companyName":"smart","homeAddress":{"street":"surat"},
		"businessHomePage":"https://outlook.live.com/people","birthday":"2024-03-02T08:00:00Z",
		"singleValueExtendedProperties":[{"id":"SystemTime {00062004-0000-0000-C000-000000000046} Id 0x8008","value":"2022-02-05T00:00:00Z"}]
	}`)
	p, err := ContactCreatePayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"displayName", "jobTitle", "emailAddresses", "businessPhones", "spouseName",
		"categories", "personalNotes", "imAddresses", "companyName", "homeAddress",
		"businessHomePage", "birthday",
	} {
		if _, ok := p[field]; !ok {
			t.Errorf("missing %s", field)
		}
	}
	b, _ := json.Marshal(p["singleValueExtendedProperties"])
	if !strings.Contains(string(b), "0x8008") || !strings.Contains(string(b), "2022-02-05") {
		t.Fatalf("anniversary props = %s", b)
	}
}

func TestContactAnniversaryFromBackup(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"singleValueExtendedProperties": []map[string]string{
			{"id": contactAnniversaryPropID, "value": "2022-02-05T00:00:00Z"},
		},
	})
	if got := ContactAnniversaryFromBackup(raw); got != "2022-02-05T00:00:00Z" {
		t.Fatalf("got %q", got)
	}
}

func TestContactItemURL(t *testing.T) {
	if got := ContactItemURL("https://g/me", "", "C1"); got != "https://g/me/contacts/C1" {
		t.Fatalf("%s", got)
	}
	if got := ContactItemURL("https://g/me", "F1", "C1"); got != "https://g/me/contactFolders/F1/contacts/C1" {
		t.Fatalf("%s", got)
	}
}

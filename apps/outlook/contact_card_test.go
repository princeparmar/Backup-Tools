package outlook

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestContactCardFromBackupBhago(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("png"))
	raw := []byte(`{
		"id":"c1","displayName":"Bhago","jobTitle":"developer",
		"emailAddresses":[{"address":"dhavalder93@gmail.com"}],
		"businessPhones":["9313405704"],"spouseName":"nothing else",
		"categories":["test"],"personalNotes":"no any fine","imAddresses":["uh"],
		"companyName":"smart",
		"homeAddress":{"street":"surat gujrate 102 stc office","city":"surta","state":"gujrat","postalCode":"365620","countryOrRegion":"india"},
		"businessHomePage":"https://outlook.live.com/people","birthday":"2024-03-02T08:00:00Z",
		"storx_contact_photo":{"content_type":"image/png","data":"` + png + `"},
		"singleValueExtendedProperties":[{"id":"SystemTime {00062004-0000-0000-C000-000000000046} Id 0x8008","value":"2022-02-05T00:00:00Z"}]
	}`)
	card, err := ContactCardFromBackup(raw)
	if err != nil {
		t.Fatal(err)
	}
	if card.DisplayName != "Bhago" || card.JobTitle != "developer" || card.CompanyName != "smart" {
		t.Fatalf("identity %+v", card)
	}
	if card.SignificantOther != "nothing else" {
		t.Fatalf("significant other %q", card.SignificantOther)
	}
	if card.Anniversary != "2022-02-05T00:00:00Z" {
		t.Fatalf("anniversary %q", card.Anniversary)
	}
	if len(card.Categories) != 1 || card.Categories[0] != "test" {
		t.Fatalf("categories %+v", card.Categories)
	}
	if len(card.Chat) != 1 || card.Chat[0] != "uh" {
		t.Fatalf("chat %+v", card.Chat)
	}
	if card.Website == "" || card.Notes != "no any fine" || card.Birthday == "" {
		t.Fatalf("website/notes/birthday %+v", card)
	}
	if len(card.Emails) != 1 || len(card.Phones) != 1 || len(card.Addresses) != 1 {
		t.Fatalf("emails/phones/addr %+v", card)
	}
	if !card.HasPhoto || card.Photo == nil || !strings.HasPrefix(card.PhotoDataURL, "data:image/png;base64,") {
		t.Fatalf("photo %+v url=%q", card.Photo, card.PhotoDataURL)
	}
}

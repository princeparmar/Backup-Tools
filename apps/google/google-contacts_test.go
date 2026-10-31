package google

import (
	"encoding/json"
	"testing"

	people "google.golang.org/api/people/v1"
)

func TestContactsIDFromResourceName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "people resource", input: "people/c123456789", want: "c123456789"},
		{name: "bare id", input: "c123456789", want: "c123456789"},
		{name: "empty", input: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContactsIDFromResourceName(tt.input); got != tt.want {
				t.Fatalf("ContactsIDFromResourceName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContactsObjectKey(t *testing.T) {
	tests := []struct {
		name        string
		resource    string
		updatedAt   string
		displayName string
		want        string
	}{
		{
			name:        "legacy id only when name empty",
			resource:    "people/c123",
			updatedAt:   "2026-07-21T10:00:00Z",
			displayName: "",
			want:        "user@gmail.com/contacts/2026/07/21/c123.json",
		},
		{
			name:        "list name in the leaf like Gmail",
			resource:    "people/c1510373750082112799",
			updatedAt:   "2026-07-21T10:00:00Z",
			displayName: "Priya Shah",
			want:        "user@gmail.com/contacts/2026/07/21/Priya Shah - c1510373750082112799.json",
		},
		{
			name:        "slash in name is underscored",
			resource:    "people/c12345678",
			updatedAt:   "2026-07-21T10:00:00Z",
			displayName: "Sales / Desk",
			want:        "user@gmail.com/contacts/2026/07/21/Sales _ Desk - c12345678.json",
		},
		{
			name:        "people id is not used as the label",
			resource:    "people/c12345678",
			updatedAt:   "2026-07-21T10:00:00Z",
			displayName: "c12345678",
			want:        "user@gmail.com/contacts/2026/07/21/c12345678.json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContactsObjectKey("user@gmail.com", tt.resource, tt.updatedAt, tt.displayName)
			if got != tt.want {
				t.Fatalf("ContactsObjectKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseContactsObjectFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantName string
		wantID   string
	}{
		{name: "legacy id", filename: "c1510373750082112799.json", wantName: "", wantID: "c1510373750082112799"},
		{name: "named leaf", filename: "Priya Shah - c1510373750082112799.json", wantName: "Priya Shah", wantID: "c1510373750082112799"},
		{name: "name with dash", filename: "Jean-Luc Picard - c12345678.json", wantName: "Jean-Luc Picard", wantID: "c12345678"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotID := ParseContactsObjectFilename(tt.filename)
			if gotName != tt.wantName || gotID != tt.wantID {
				t.Fatalf("got (%q, %q), want (%q, %q)", gotName, gotID, tt.wantName, tt.wantID)
			}
		})
	}
}

func TestContactKeyDisplayName(t *testing.T) {
	tests := []struct {
		name string
		item FlatContact
		want string
	}{
		{name: "uses display name", item: FlatContact{Name: "Ada Lovelace"}, want: "Ada Lovelace"},
		{name: "falls back to email", item: FlatContact{Name: "", Emails: []string{"ada@example.com"}}, want: "ada@example.com"},
		{name: "skips people id", item: FlatContact{Name: "c12345678", Emails: []string{"ada@example.com"}}, want: "ada@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContactKeyDisplayName(tt.item); got != tt.want {
				t.Fatalf("ContactKeyDisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildContactsSyncedIDSet(t *testing.T) {
	tests := []struct {
		name       string
		objectKeys map[string]bool
		email      string
		wantIDs    []string
	}{
		{
			name: "dated contacts json keys",
			objectKeys: map[string]bool{
				"user@gmail.com/contacts/2026/07/21/c123.json":                 true,
				"user@gmail.com/contacts/2026/07/22/Ada Lovelace - c456.json": true,
			},
			email:   "user@gmail.com",
			wantIDs: []string{"c123", "c456"},
		},
		{
			name: "ignores undated and placeholders",
			objectKeys: map[string]bool{
				"user@gmail.com/contacts/c123.json": true,
				"user@gmail.com/.file_placeholder":  true,
			},
			email:   "user@gmail.com",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildContactsSyncedIDSet(tt.objectKeys, tt.email)
			for _, id := range tt.wantIDs {
				if _, ok := got[id]; !ok {
					t.Fatalf("expected id %q in set, got %v", id, got)
				}
			}
			if len(tt.wantIDs) == 0 && len(got) != 0 {
				t.Fatalf("expected empty set, got %v", got)
			}
		})
	}
}

func TestPageHasAnyNewContactsItems(t *testing.T) {
	synced := map[string]struct{}{"known": {}}
	tests := []struct {
		name  string
		items []FlatContact
		want  bool
	}{
		{name: "all known", items: []FlatContact{{ID: "people/known"}}, want: false},
		{name: "has new", items: []FlatContact{{ID: "people/known"}, {ID: "people/new"}}, want: true},
		{name: "empty page", items: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PageHasAnyNewContactsItems(tt.items, synced); got != tt.want {
				t.Fatalf("PageHasAnyNewContactsItems() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsContactSynced(t *testing.T) {
	syncedMap := map[string]bool{
		"user@gmail.com/contacts/c123.json":                         true,
		"user@gmail.com/contacts/2026/07/21/c999.json":              true,
		"user@gmail.com/contacts/2026/07/22/Ada Lovelace - c888.json": true,
	}
	tests := []struct {
		name         string
		resourceName string
		want         bool
	}{
		{name: "undated ignored", resourceName: "people/c123", want: false},
		{name: "dated synced", resourceName: "people/c999", want: true},
		{name: "named dated synced", resourceName: "people/c888", want: true},
		{name: "not synced", resourceName: "people/c000", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsContactSynced(syncedMap, "user@gmail.com", tt.resourceName); got != tt.want {
				t.Fatalf("IsContactSynced() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPersonFromContactsBackup_usesWritableNameFields(t *testing.T) {
	person := personFromContactsBackup(ContactsBackupObject{
		Name:   "Ada Lovelace",
		Phones: []string{"+1 555-0100"},
		Emails: []string{"ada@example.com"},
	})
	if person == nil || len(person.Names) != 1 {
		t.Fatalf("expected one name, got %+v", person)
	}
	if person.Names[0].DisplayName != "" {
		t.Fatalf("displayName must stay empty on create, got %q", person.Names[0].DisplayName)
	}
	if person.Names[0].UnstructuredName != "Ada Lovelace" {
		t.Fatalf("unstructuredName = %q, want Ada Lovelace", person.Names[0].UnstructuredName)
	}
	if len(person.PhoneNumbers) != 1 || person.PhoneNumbers[0].Value != "+1 555-0100" {
		t.Fatalf("phones = %+v", person.PhoneNumbers)
	}
	if len(person.EmailAddresses) != 1 || person.EmailAddresses[0].Value != "ada@example.com" {
		t.Fatalf("emails = %+v", person.EmailAddresses)
	}
}

func TestPersonFromContactsBackup_labeledDetails(t *testing.T) {
	tests := []struct {
		name    string
		backup  ContactsBackupObject
		email   string
		etype   string
		phone   string
		ptype   string
		title   string
		company string
		address string
		note    string
	}{
		{
			name: "google detail fields",
			backup: ContactsBackupObject{
				Name: "Aarav Mehta",
				EmailDetails: []ContactLabeledValue{
					{Value: "aarav.mehta@example.com", Type: "Other"},
				},
				PhoneDetails: []ContactLabeledValue{
					{Value: "+1 202-555-0101", Type: "Home"},
				},
				OrgDetails: []ContactOrganization{
					{Name: "Northstar Labs", Title: "Software Engineer"},
				},
				Addresses: []ContactLabeledValue{
					{Value: "1 Market St, San Francisco, CA", Type: "Work"},
				},
				Notes: "Test contact 01",
			},
			email:   "aarav.mehta@example.com",
			etype:   "Other",
			phone:   "+1 202-555-0101",
			ptype:   "Home",
			title:   "Software Engineer",
			company: "Northstar Labs",
			address: "1 Market St, San Francisco, CA",
			note:    "Test contact 01",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			person := personFromContactsBackup(tt.backup)
			if person == nil {
				t.Fatal("person is nil")
			}
			if person.EmailAddresses[0].Value != tt.email || person.EmailAddresses[0].Type != tt.etype {
				t.Fatalf("email = %+v", person.EmailAddresses[0])
			}
			if person.PhoneNumbers[0].Value != tt.phone || person.PhoneNumbers[0].Type != tt.ptype {
				t.Fatalf("phone = %+v", person.PhoneNumbers[0])
			}
			if person.Organizations[0].Title != tt.title || person.Organizations[0].Name != tt.company {
				t.Fatalf("org = %+v", person.Organizations[0])
			}
			if person.Addresses[0].FormattedValue != tt.address {
				t.Fatalf("address = %+v", person.Addresses[0])
			}
			if person.Biographies[0].Value != tt.note {
				t.Fatalf("note = %+v", person.Biographies[0])
			}
		})
	}
}

func TestPersonFromContactsBackup_parsesVaultJSON(t *testing.T) {
	raw := []byte(`{"resource_name":"people/c1","name":"Sales Desk","phones":["(22) 2278-5000"],"emails":[],"updated_at":"2026-09-18T00:00:00Z"}`)
	var backup ContactsBackupObject
	if err := json.Unmarshal(raw, &backup); err != nil {
		t.Fatal(err)
	}
	person := personFromContactsBackup(backup)
	if person == nil || person.Names[0].UnstructuredName != "Sales Desk" {
		t.Fatalf("got %+v", person)
	}
}

func TestFlatContactFromPerson_keepsRemainingFields(t *testing.T) {
	person := &people.Person{
		ResourceName: "people/c1",
		Names: []*people.Name{{
			DisplayName: "tst3 fsdgd", GivenName: "tst3", FamilyName: "fsdgd", PhoneticFullName: "gdgdgdf",
		}},
		Nicknames:      []*people.Nickname{{Value: "gdgdgdf"}},
		FileAses:       []*people.FileAs{{Value: "fgdfg"}},
		EmailAddresses: []*people.EmailAddress{{Value: "ada@example.com", Type: "home"}},
		PhoneNumbers:   []*people.PhoneNumber{{Value: "08460248842", Type: "mobile"}},
		Addresses: []*people.Address{{
			FormattedValue: "53, Dharmiyan Raw House", Type: "work",
			StreetAddress: "53, Dharmiyan Raw House", City: "Surat", Region: "Himachal Pradesh", PostalCode: "39101", Country: "IN",
		}},
		Birthdays:     []*people.Birthday{{Date: &people.Date{Year: 2005, Month: 12, Day: 4}}},
		Events:        []*people.Event{{Date: &people.Date{Year: 2009, Month: 12, Day: 8}, Type: "other"}},
		Urls:          []*people.Url{{Value: "https://contacts.google.com/new", Type: "blog"}},
		Organizations: []*people.Organization{{Name: "888", Title: "Assistant"}},
		UserDefined:   []*people.UserDefined{{Key: "cccc", Value: "88888"}},
		Relations:     []*people.Relation{{Person: "fbcbcvbfgggdfg", Type: "friend"}},
		Memberships:   []*people.Membership{{ContactGroupMembership: &people.ContactGroupMembership{ContactGroupResourceName: "contactGroups/label1"}}},
		Photos:        []*people.Photo{{Url: "https://lh3.googleusercontent.com/photo", Default: false}, {Url: "https://lh3.googleusercontent.com/default", Default: true}},
		Biographies:   []*people.Biography{{Value: "a note"}},
	}
	flat := flatContactFromPerson(person)
	ApplyContactGroupNames(&flat, map[string]string{"contactGroups/label1": "tesff", "contactGroups/myContacts": "myContacts"})
	if flat.Name != "tst3 fsdgd" || flat.Names[0].Given != "tst3" || flat.Names[0].PhoneticFull != "gdgdgdf" {
		t.Fatalf("name = %+v %q", flat.Names, flat.Name)
	}
	if flat.Nicknames[0].Value != "gdgdgdf" || flat.FileAs[0] != "fgdfg" {
		t.Fatalf("nickname/file-as = %+v %+v", flat.Nicknames, flat.FileAs)
	}
	if flat.Addresses[0].City != "Surat" || flat.Addresses[0].PostalCode != "39101" || flat.Addresses[0].Type != "work" {
		t.Fatalf("address = %+v", flat.Addresses[0])
	}
	if flat.Birthday != "2005-12-04" || flat.Events[0].Date != "2009-12-08" || flat.Events[0].Type != "other" {
		t.Fatalf("dates = %q %+v", flat.Birthday, flat.Events)
	}
	if flat.Websites[0].Value != "https://contacts.google.com/new" || flat.Websites[0].Type != "blog" {
		t.Fatalf("website = %+v", flat.Websites)
	}
	if flat.CustomFields[0].Key != "cccc" || flat.Relations[0].Value != "fbcbcvbfgggdfg" || len(flat.Labels) != 1 || flat.Labels[0] != "tesff" {
		t.Fatalf("custom/relation/label = %+v %+v %+v", flat.CustomFields, flat.Relations, flat.Labels)
	}
	if flat.PhotoURL != "https://lh3.googleusercontent.com/photo" || flat.Notes != "a note" {
		t.Fatalf("photo/notes = %q %q", flat.PhotoURL, flat.Notes)
	}

	stored := BackupFromFlat(flat)
	restored := personFromContactsBackup(stored)
	if restored.Names[0].GivenName != "tst3" || restored.Names[0].DisplayName != "" {
		t.Fatalf("restored name = %+v", restored.Names[0])
	}
	if restored.Addresses[0].City != "Surat" || restored.Events[0].Type != "other" || restored.Urls[0].Type != "blog" {
		t.Fatalf("restored details = %+v", restored)
	}
	if restored.UserDefined[0].Value != "88888" || restored.Relations[0].Person != "fbcbcvbfgggdfg" || restored.Nicknames[0].Value != "gdgdgdf" {
		t.Fatalf("restored extra = %+v", restored)
	}
}

func TestPeopleStoredTypeUsesDisplayLabel(t *testing.T) {
	if got := peopleStoredType("gfdgdgdg", "other"); got != "gfdgdgdg" {
		t.Fatalf("custom label %q", got)
	}
	if got := peopleStoredType("Work", "work"); got != "Work" {
		t.Fatalf("work label %q", got)
	}
	if got := peopleStoredType("", "blog"); got != "blog" {
		t.Fatalf("raw label %q", got)
	}
}

func TestContactBackupStaleWhenEditDayChanges(t *testing.T) {
	key := "user@gmail.com/contacts/2026/07/21/c1.json"
	if !ContactBackupStale(key, "2026-10-21T10:00:00Z") {
		t.Fatal("later edit should rewrite the contact")
	}
	if ContactBackupStale(key, "2026-07-21T18:00:00Z") {
		t.Fatal("same day should keep the contact file")
	}
}

func TestContactPhotoURLAllowed(t *testing.T) {
	if !contactPhotoURLAllowed("https://lh3.googleusercontent.com/abc") {
		t.Fatal("google photo host should be allowed")
	}
	if contactPhotoURLAllowed("http://lh3.googleusercontent.com/abc") || contactPhotoURLAllowed("https://example.com/photo.jpg") {
		t.Fatal("non-google or insecure photo url should be rejected")
	}
}

package google

import (
	"testing"

	people "google.golang.org/api/people/v1"
)

func TestNormalizeContactIdentity(t *testing.T) {
	cases := []struct {
		name  string
		email string
		phone string
		wantE string
		wantP string
	}{
		{name: "email lowercased", email: " Ada@Example.com ", wantE: "ada@example.com"},
		{name: "phone last 10 digits", phone: "+1 202-555-0101", wantP: "2025550101"},
		{name: "indian mobile keeps last 10", phone: "+91 98765 43210", wantP: "9876543210"},
		{name: "short phone kept as digits", phone: "555-0100", wantP: "5550100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.email != "" {
				if got := NormalizeContactEmail(tc.email); got != tc.wantE {
					t.Fatalf("NormalizeContactEmail = %q, want %q", got, tc.wantE)
				}
			}
			if tc.phone != "" {
				if got := NormalizeContactPhone(tc.phone); got != tc.wantP {
					t.Fatalf("NormalizeContactPhone = %q, want %q", got, tc.wantP)
				}
			}
		})
	}
}

func TestContactIdentityIndexHasPerson(t *testing.T) {
	idx := NewContactIdentityIndex()
	idx.AddFlat(FlatContact{
		Emails: []string{"aditya.verma@example.com"},
		Phones: []string{"+1 202-555-0115"},
	})

	cases := []struct {
		name   string
		person *people.Person
		want   bool
	}{
		{
			name: "same email skips create",
			person: &people.Person{
				EmailAddresses: []*people.EmailAddress{{Value: "Aditya.Verma@example.com"}},
			},
			want: true,
		},
		{
			name: "same phone skips create",
			person: &people.Person{
				Names:        []*people.Name{{UnstructuredName: "bhago"}},
				PhoneNumbers: []*people.PhoneNumber{{Value: "12025550115"}},
			},
			want: true,
		},
		{
			name: "new contact is created",
			person: &people.Person{
				EmailAddresses: []*people.EmailAddress{{Value: "new.person@example.com"}},
				PhoneNumbers:   []*people.PhoneNumber{{Value: "+1 202-555-0199"}},
			},
			want: false,
		},
		{
			name: "short phone is not a match",
			person: &people.Person{
				PhoneNumbers: []*people.PhoneNumber{{Value: "555"}},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := idx.HasPerson(tc.person); got != tc.want {
				t.Fatalf("HasPerson = %v, want %v", got, tc.want)
			}
		})
	}
}

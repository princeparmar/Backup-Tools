package google

import (
	"context"
	"strings"
	"sync"
	"unicode"

	people "google.golang.org/api/people/v1"
)

// ContactIdentityIndex is the email/phone set already in the restore target mailbox.
// Restore skips CreateContact when a vault contact matches, so invited and owner
// restore-all do not keep adding the same people.
type ContactIdentityIndex struct {
	mu     sync.Mutex
	emails map[string]struct{}
	phones map[string]struct{}
}

func NewContactIdentityIndex() *ContactIdentityIndex {
	return &ContactIdentityIndex{
		emails: make(map[string]struct{}),
		phones: make(map[string]struct{}),
	}
}

func NormalizeContactEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func NormalizeContactPhone(value string) string {
	var digits strings.Builder
	for _, r := range value {
		if unicode.IsDigit(r) {
			digits.WriteRune(r)
		}
	}
	out := digits.String()
	if len(out) > 10 {
		return out[len(out)-10:]
	}
	return out
}

func (idx *ContactIdentityIndex) AddEmail(value string) {
	if idx == nil {
		return
	}
	key := NormalizeContactEmail(value)
	if key == "" {
		return
	}
	idx.mu.Lock()
	idx.emails[key] = struct{}{}
	idx.mu.Unlock()
}

func (idx *ContactIdentityIndex) AddPhone(value string) {
	if idx == nil {
		return
	}
	key := NormalizeContactPhone(value)
	if len(key) < 7 {
		return
	}
	idx.mu.Lock()
	idx.phones[key] = struct{}{}
	idx.mu.Unlock()
}

func (idx *ContactIdentityIndex) AddFlat(item FlatContact) {
	if idx == nil {
		return
	}
	for _, email := range item.Emails {
		idx.AddEmail(email)
	}
	for _, row := range item.EmailDetails {
		idx.AddEmail(row.Value)
	}
	for _, phone := range item.Phones {
		idx.AddPhone(phone)
	}
	for _, row := range item.PhoneDetails {
		idx.AddPhone(row.Value)
	}
}

func (idx *ContactIdentityIndex) AddPerson(person *people.Person) {
	if idx == nil || person == nil {
		return
	}
	for _, email := range person.EmailAddresses {
		if email != nil {
			idx.AddEmail(email.Value)
		}
	}
	for _, phone := range person.PhoneNumbers {
		if phone != nil {
			idx.AddPhone(phone.Value)
		}
	}
}

func (idx *ContactIdentityIndex) HasPerson(person *people.Person) bool {
	if idx == nil || person == nil {
		return false
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for _, email := range person.EmailAddresses {
		if email == nil {
			continue
		}
		if _, ok := idx.emails[NormalizeContactEmail(email.Value)]; ok {
			return true
		}
	}
	for _, phone := range person.PhoneNumbers {
		if phone == nil {
			continue
		}
		key := NormalizeContactPhone(phone.Value)
		if len(key) < 7 {
			continue
		}
		if _, ok := idx.phones[key]; ok {
			return true
		}
	}
	return false
}

// LoadContactIdentityIndex lists every connection in the target mailbox.
func LoadContactIdentityIndex(ctx context.Context, service *people.Service) (*ContactIdentityIndex, error) {
	idx := NewContactIdentityIndex()
	if service == nil {
		return idx, nil
	}
	pageToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return idx, err
		}
		page, err := ListAllContactsFlatWithService(service, pageToken)
		if err != nil {
			return idx, err
		}
		for i := range page.Contacts {
			idx.AddFlat(page.Contacts[i])
		}
		if strings.TrimSpace(page.NextPageToken) == "" {
			return idx, nil
		}
		pageToken = page.NextPageToken
	}
}

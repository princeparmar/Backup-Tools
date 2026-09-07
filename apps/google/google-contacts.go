package google

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	people "google.golang.org/api/people/v1"
)

const (
	contactsPageSize      = 100
	contactsPersonFields  = "names,emailAddresses,phoneNumbers,organizations,addresses,birthdays,photos,metadata"
	contactsReadonlyScope = "https://www.googleapis.com/auth/contacts.readonly"
	contactsScope         = "https://www.googleapis.com/auth/contacts"
)

// ContactsBackupObject is the JSON stored in the vault by cron autosync.
type ContactsBackupObject struct {
	ResourceName    string   `json:"resource_name"`
	Name            string   `json:"name"`
	Phones          []string `json:"phones"`
	Emails          []string `json:"emails"`
	Organizations   []string `json:"organizations,omitempty"`
	ETag            string   `json:"etag"`
	SourceUpdatedAt string   `json:"source_updated_at,omitempty"`
	UpdatedAt       string   `json:"updated_at"`
}

// FlatContactsResponse is the paginated contacts listing (HTTP route + cron).
type FlatContactsResponse struct {
	Contacts            []FlatContact `json:"contacts"`
	NextPageToken       string        `json:"nextPageToken"`
	NextPageTokenLegacy string        `json:"next_page_token,omitempty"`
}

// FlatContact is a slim contact for listing and sync.
type FlatContact struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Phones          []string `json:"phones"`
	Emails          []string `json:"emails"`
	Organizations   []string `json:"organizations,omitempty"`
	ETag            string   `json:"etag,omitempty"`
	SourceUpdatedAt string   `json:"source_updated_at,omitempty"`
	Synced          bool     `json:"synced,omitempty"`
}

// ListAllContactsFlat returns a paginated connections list via People API.
func ListAllContactsFlat(c echo.Context, pageToken string) (*FlatContactsResponse, error) {
	service, err := NewPeopleServiceFromContext(c)
	if err != nil {
		return nil, err
	}
	return ListAllContactsFlatWithService(service, pageToken)
}

// NewPeopleServiceFromContext builds a People API client from the request JWT token.
func NewPeopleServiceFromContext(c echo.Context) (*people.Service, error) {
	httpClient, err := client(c)
	if err != nil {
		return nil, err
	}
	return people.NewService(c.Request().Context(), option.WithHTTPClient(httpClient))
}

// NewPeopleServiceWithAccessToken builds a People API client for cron autosync.
func NewPeopleServiceWithAccessToken(ctx context.Context, accessToken string) (*people.Service, error) {
	return newPeopleServiceWithAccessToken(ctx, accessToken, contactsReadonlyScope)
}

// NewPeopleServiceForRestore builds a People API client for restore (requires contacts write scope on token).
func NewPeopleServiceForRestore(ctx context.Context, accessToken string) (*people.Service, error) {
	return newPeopleServiceWithAccessToken(ctx, accessToken, contactsScope)
}

func newPeopleServiceWithAccessToken(ctx context.Context, accessToken, scope string) (*people.Service, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}
	config, err := oauth2google.ConfigFromJSON(b, scope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}
	token := &oauth2.Token{AccessToken: accessToken}
	httpClient := config.Client(ctx, token)
	return people.NewService(ctx, option.WithHTTPClient(httpClient))
}

// ListAllContactsFlatWithService lists people.connections with pagination.
func ListAllContactsFlatWithService(service *people.Service, pageToken string) (*FlatContactsResponse, error) {
	if service == nil {
		return nil, fmt.Errorf("people service is nil")
	}
	call := service.People.Connections.List("people/me").
		PageSize(contactsPageSize).
		PersonFields(contactsPersonFields)
	if token := strings.TrimSpace(pageToken); token != "" {
		call = call.PageToken(token)
	}
	response, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	items := make([]FlatContact, 0, len(response.Connections))
	for _, person := range response.Connections {
		if person == nil || strings.TrimSpace(person.ResourceName) == "" {
			continue
		}
		items = append(items, flatContactFromPerson(person))
	}
	next := strings.TrimSpace(response.NextPageToken)
	return &FlatContactsResponse{
		Contacts:            items,
		NextPageToken:       next,
		NextPageTokenLegacy: next,
	}, nil
}

func flatContactFromPerson(person *people.Person) FlatContact {
	out := FlatContact{
		ID:   strings.TrimSpace(person.ResourceName),
		ETag: strings.TrimSpace(person.Etag),
	}
	if person.Metadata != nil {
		for _, src := range person.Metadata.Sources {
			if src == nil {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(src.Type), "CONTACT") && strings.TrimSpace(src.UpdateTime) != "" {
				out.SourceUpdatedAt = strings.TrimSpace(src.UpdateTime)
				break
			}
		}
		if out.SourceUpdatedAt == "" {
			for _, src := range person.Metadata.Sources {
				if src == nil {
					continue
				}
				if v := strings.TrimSpace(src.UpdateTime); v != "" {
					out.SourceUpdatedAt = v
					break
				}
			}
		}
	}
	if len(person.Names) > 0 {
		name := person.Names[0]
		if strings.TrimSpace(name.DisplayName) != "" {
			out.Name = strings.TrimSpace(name.DisplayName)
		} else {
			out.Name = strings.TrimSpace(strings.TrimSpace(name.GivenName) + " " + strings.TrimSpace(name.FamilyName))
		}
	}
	for _, email := range person.EmailAddresses {
		if email == nil {
			continue
		}
		if v := strings.TrimSpace(email.Value); v != "" {
			out.Emails = append(out.Emails, v)
		}
	}
	for _, phone := range person.PhoneNumbers {
		if phone == nil {
			continue
		}
		if v := strings.TrimSpace(phone.Value); v != "" {
			out.Phones = append(out.Phones, v)
		}
	}
	for _, org := range person.Organizations {
		if org == nil {
			continue
		}
		label := strings.TrimSpace(org.Name)
		if label == "" {
			label = strings.TrimSpace(org.Title)
		}
		if label != "" {
			out.Organizations = append(out.Organizations, label)
		}
	}
	return out
}

// ContactsIDFromResourceName returns the stable id segment from people/{id}.
func ContactsIDFromResourceName(resourceName string) string {
	resourceName = strings.TrimSpace(resourceName)
	if resourceName == "" {
		return ""
	}
	const prefix = "people/"
	if strings.HasPrefix(resourceName, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(resourceName, prefix))
	}
	return strings.ReplaceAll(resourceName, "/", "_")
}

// ContactsObjectKey returns storage path: {email}/contacts/{yyyy}/{mm}/{dd}/{contactId}.json
// sourceUpdatedAt is People API sources[].updateTime (RFC3339).
func ContactsObjectKey(email, resourceName, sourceUpdatedAt string) string {
	id := ContactsIDFromResourceName(resourceName)
	return fmt.Sprintf("%s/contacts/%s/%s.json",
		strings.TrimSpace(email),
		ObjectKeyDatePathFromRFC3339(sourceUpdatedAt),
		id,
	)
}

// BuildContactsSyncedIDSet builds a contactId set from dated synced object keys under email prefix.
func BuildContactsSyncedIDSet(objectKeys map[string]bool, emailPrefix string) map[string]struct{} {
	set := make(map[string]struct{})
	prefix := strings.TrimSpace(emailPrefix)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	contactsPrefix := prefix + "contacts/"
	for key := range objectKeys {
		key = strings.TrimSpace(key)
		if !strings.HasPrefix(key, contactsPrefix) || !strings.HasSuffix(key, ".json") {
			continue
		}
		segment := strings.TrimSuffix(strings.TrimPrefix(key, contactsPrefix), ".json")
		// Expect yyyy/mm/dd/{id}
		parts := strings.Split(segment, "/")
		if len(parts) != 4 {
			continue
		}
		if id := strings.TrimSpace(parts[3]); id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

// PageHasAnyNewContactsItems returns true if any contact id is not in syncedSet.
func PageHasAnyNewContactsItems(items []FlatContact, syncedSet map[string]struct{}) bool {
	for i := range items {
		id := ContactsIDFromResourceName(items[i].ID)
		if id == "" {
			continue
		}
		if _, ok := syncedSet[id]; !ok {
			return true
		}
	}
	return false
}

// IsContactSynced checks whether a contact object exists in dated synced_objects paths.
func IsContactSynced(syncedMap map[string]bool, email, resourceName string) bool {
	if syncedMap == nil {
		return false
	}
	id := ContactsIDFromResourceName(resourceName)
	if id == "" {
		return false
	}
	suffix := "/" + id + ".json"
	prefix := strings.TrimSpace(email) + "/contacts/"
	for key := range syncedMap {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
			continue
		}
		rest := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".json")
		if len(strings.Split(rest, "/")) == 4 {
			return true
		}
	}
	return false
}

// IsContactsRestoreObjectKey returns true for backed-up contact JSON paths (not placeholders).
func IsContactsRestoreObjectKey(objectKey string) bool {
	objectKey = strings.TrimSpace(objectKey)
	return strings.Contains(objectKey, "/contacts/") && strings.HasSuffix(objectKey, ".json") && !strings.Contains(objectKey, ".file_placeholder")
}

// RestoreContactFromBackup creates a Google contact from vault JSON.
func RestoreContactFromBackup(ctx context.Context, service *people.Service, data []byte) error {
	if service == nil {
		return fmt.Errorf("people service is nil")
	}
	var backup ContactsBackupObject
	if err := json.Unmarshal(data, &backup); err != nil {
		return fmt.Errorf("parse contact backup: %w", err)
	}
	person := personFromContactsBackup(backup)
	if person == nil {
		return fmt.Errorf("contact backup has no restorable fields")
	}
	_, err := service.People.CreateContact(person).Do()
	if err != nil {
		return fmt.Errorf("create contact: %w", err)
	}
	return nil
}

func personFromContactsBackup(backup ContactsBackupObject) *people.Person {
	person := &people.Person{}
	if name := strings.TrimSpace(backup.Name); name != "" {
		person.Names = []*people.Name{{DisplayName: name}}
	}
	for _, e := range backup.Emails {
		if v := strings.TrimSpace(e); v != "" {
			person.EmailAddresses = append(person.EmailAddresses, &people.EmailAddress{Value: v})
		}
	}
	for _, p := range backup.Phones {
		if v := strings.TrimSpace(p); v != "" {
			person.PhoneNumbers = append(person.PhoneNumbers, &people.PhoneNumber{Value: v})
		}
	}
	for _, o := range backup.Organizations {
		if v := strings.TrimSpace(o); v != "" {
			person.Organizations = append(person.Organizations, &people.Organization{Name: v})
		}
	}
	if len(person.Names) == 0 && len(person.EmailAddresses) == 0 && len(person.PhoneNumbers) == 0 && len(person.Organizations) == 0 {
		return nil
	}
	return person
}

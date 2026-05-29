package google

import (
	"context"
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
	contactsPageSize     = 100
	contactsPersonFields = "names,emailAddresses,phoneNumbers,organizations,addresses,birthdays,photos,metadata"
	contactsReadonlyScope = "https://www.googleapis.com/auth/contacts.readonly"
)

// FlatContactsResponse is the paginated contacts listing (HTTP route + cron).
type FlatContactsResponse struct {
	Contacts          []FlatContact `json:"contacts"`
	NextPageToken     string        `json:"nextPageToken"`
	NextPageTokenLegacy string      `json:"next_page_token,omitempty"`
}

// FlatContact is a slim contact for listing and sync.
type FlatContact struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Phones       []string `json:"phones"`
	Emails       []string `json:"emails"`
	Organizations []string `json:"organizations,omitempty"`
	ETag         string   `json:"etag,omitempty"`
	Synced       bool     `json:"synced,omitempty"`
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
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}
	config, err := oauth2google.ConfigFromJSON(b, contactsReadonlyScope)
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

// ContactsObjectKey returns storage path: {email}/contacts/{contactId}.json
func ContactsObjectKey(email, resourceName string) string {
	id := ContactsIDFromResourceName(resourceName)
	return fmt.Sprintf("%s/contacts/%s.json", strings.TrimSpace(email), id)
}

// BuildContactsSyncedIDSet builds a contactId set from synced object keys under email prefix.
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
		if id := strings.TrimSpace(segment); id != "" {
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

// IsContactSynced checks whether a contact object exists in synced_objects paths.
func IsContactSynced(syncedMap map[string]bool, email, resourceName string) bool {
	if syncedMap == nil {
		return false
	}
	return syncedMap[ContactsObjectKey(email, resourceName)]
}

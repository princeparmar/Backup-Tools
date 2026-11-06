package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	people "google.golang.org/api/people/v1"
)

const (
	contactsPageSize      = 100
	contactsPersonFields  = "names,nicknames,fileAses,emailAddresses,phoneNumbers,organizations,addresses,birthdays,events,biographies,urls,relations,userDefined,memberships,photos,metadata"
	contactsReadonlyScope = "https://www.googleapis.com/auth/contacts.readonly"
	contactsScope         = "https://www.googleapis.com/auth/contacts"
	contactPhotoMaxBytes  = 4 << 20
)

// ContactLabeledValue is a phone, email, address, website, or relation with Google's type.
// Street through country code are set for addresses.
type ContactLabeledValue struct {
	Value       string `json:"value"`
	Type        string `json:"type,omitempty"`
	Street      string `json:"street,omitempty"`
	Extended    string `json:"extended,omitempty"`
	POBox       string `json:"po_box,omitempty"`
	City        string `json:"city,omitempty"`
	Region      string `json:"region,omitempty"`
	PostalCode  string `json:"postal_code,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
}

// ContactOrganization is a company and job title from People API organizations.
type ContactOrganization struct {
	Name           string `json:"name,omitempty"`
	Title          string `json:"title,omitempty"`
	Department     string `json:"department,omitempty"`
	Location       string `json:"location,omitempty"`
	JobDescription string `json:"job_description,omitempty"`
	Type           string `json:"type,omitempty"`
}

// ContactName is the writable name of a contact. Display name stays in ContactsBackupObject.Name.
type ContactName struct {
	Given          string `json:"given,omitempty"`
	Middle         string `json:"middle,omitempty"`
	Family         string `json:"family,omitempty"`
	Prefix         string `json:"prefix,omitempty"`
	Suffix         string `json:"suffix,omitempty"`
	PhoneticGiven  string `json:"phonetic_given,omitempty"`
	PhoneticMiddle string `json:"phonetic_middle,omitempty"`
	PhoneticFamily string `json:"phonetic_family,omitempty"`
	PhoneticFull   string `json:"phonetic_full,omitempty"`
	Unstructured   string `json:"unstructured,omitempty"`
}

// ContactEvent is a birthday or other dated event (anniversary, custom).
type ContactEvent struct {
	Date string `json:"date,omitempty"`
	Type string `json:"type,omitempty"`
}

// ContactCustomField is a user-defined key and value.
type ContactCustomField struct {
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

// ContactsBackupObject is the JSON stored in the vault by cron autosync.
type ContactsBackupObject struct {
	ResourceName    string                `json:"resource_name"`
	Name            string                `json:"name"`
	Phones          []string              `json:"phones"`
	Emails          []string              `json:"emails"`
	Organizations   []string              `json:"organizations,omitempty"`
	PhoneDetails    []ContactLabeledValue `json:"phone_details,omitempty"`
	EmailDetails    []ContactLabeledValue `json:"email_details,omitempty"`
	OrgDetails      []ContactOrganization `json:"organization_details,omitempty"`
	Addresses       []ContactLabeledValue `json:"addresses,omitempty"`
	Birthday        string                `json:"birthday,omitempty"`
	Notes           string                `json:"notes,omitempty"`
	Names           []ContactName         `json:"names,omitempty"`
	Nicknames       []ContactLabeledValue `json:"nicknames,omitempty"`
	FileAs          []string              `json:"file_as,omitempty"`
	Events          []ContactEvent        `json:"events,omitempty"`
	Websites        []ContactLabeledValue `json:"websites,omitempty"`
	Relations       []ContactLabeledValue `json:"relations,omitempty"`
	CustomFields    []ContactCustomField  `json:"custom_fields,omitempty"`
	Labels          []string              `json:"labels,omitempty"`
	PhotoURL        string                `json:"photo_url,omitempty"`
	Photo           string                `json:"photo,omitempty"`
	ETag            string                `json:"etag"`
	SourceUpdatedAt string                `json:"source_updated_at,omitempty"`
	UpdatedAt       string                `json:"updated_at"`
}

// FlatContactsResponse is the paginated contacts listing (HTTP route + cron).
type FlatContactsResponse struct {
	Contacts            []FlatContact `json:"contacts"`
	NextPageToken       string        `json:"nextPageToken"`
	NextPageTokenLegacy string        `json:"next_page_token,omitempty"`
}

// FlatContact is a slim contact for listing and sync.
type FlatContact struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	Phones          []string              `json:"phones"`
	Emails          []string              `json:"emails"`
	Organizations   []string              `json:"organizations,omitempty"`
	PhoneDetails    []ContactLabeledValue `json:"phone_details,omitempty"`
	EmailDetails    []ContactLabeledValue `json:"email_details,omitempty"`
	OrgDetails      []ContactOrganization `json:"organization_details,omitempty"`
	Addresses       []ContactLabeledValue `json:"addresses,omitempty"`
	Birthday        string                `json:"birthday,omitempty"`
	Notes           string                `json:"notes,omitempty"`
	Names           []ContactName         `json:"names,omitempty"`
	Nicknames       []ContactLabeledValue `json:"nicknames,omitempty"`
	FileAs          []string              `json:"file_as,omitempty"`
	Events          []ContactEvent        `json:"events,omitempty"`
	Websites        []ContactLabeledValue `json:"websites,omitempty"`
	Relations       []ContactLabeledValue `json:"relations,omitempty"`
	CustomFields    []ContactCustomField  `json:"custom_fields,omitempty"`
	Labels          []string              `json:"labels,omitempty"`
	PhotoURL        string                `json:"photo_url,omitempty"`
	GroupIDs        []string              `json:"-"`
	ETag            string                `json:"etag,omitempty"`
	SourceUpdatedAt string                `json:"source_updated_at,omitempty"`
	Synced          bool                  `json:"synced,omitempty"`
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
	for _, name := range person.Names {
		if name == nil {
			continue
		}
		stored := contactNameFrom(name)
		if stored == (ContactName{}) && strings.TrimSpace(name.DisplayName) == "" {
			continue
		}
		out.Names = append(out.Names, stored)
	}
	if len(person.Names) > 0 && person.Names[0] != nil {
		name := person.Names[0]
		if strings.TrimSpace(name.DisplayName) != "" {
			out.Name = strings.TrimSpace(name.DisplayName)
		} else if strings.TrimSpace(name.UnstructuredName) != "" {
			out.Name = strings.TrimSpace(name.UnstructuredName)
		} else {
			out.Name = strings.TrimSpace(strings.TrimSpace(name.GivenName) + " " + strings.TrimSpace(name.FamilyName))
		}
	}
	for _, nick := range person.Nicknames {
		if nick == nil {
			continue
		}
		if v := strings.TrimSpace(nick.Value); v != "" {
			out.Nicknames = append(out.Nicknames, ContactLabeledValue{Value: v, Type: strings.TrimSpace(nick.Type)})
		}
	}
	for _, fileAs := range person.FileAses {
		if fileAs == nil {
			continue
		}
		if v := strings.TrimSpace(fileAs.Value); v != "" {
			out.FileAs = append(out.FileAs, v)
		}
	}
	for _, email := range person.EmailAddresses {
		if email == nil {
			continue
		}
		if v := strings.TrimSpace(email.Value); v != "" {
			out.Emails = append(out.Emails, v)
			out.EmailDetails = append(out.EmailDetails, ContactLabeledValue{Value: v, Type: peopleStoredType(email.FormattedType, email.Type)})
		}
	}
	for _, phone := range person.PhoneNumbers {
		if phone == nil {
			continue
		}
		if v := strings.TrimSpace(phone.Value); v != "" {
			out.Phones = append(out.Phones, v)
			out.PhoneDetails = append(out.PhoneDetails, ContactLabeledValue{Value: v, Type: peopleStoredType(phone.FormattedType, phone.Type)})
		}
	}
	for _, org := range person.Organizations {
		if org == nil {
			continue
		}
		name := strings.TrimSpace(org.Name)
		title := strings.TrimSpace(org.Title)
		department := strings.TrimSpace(org.Department)
		location := strings.TrimSpace(org.Location)
		job := strings.TrimSpace(org.JobDescription)
		orgType := peopleStoredType(org.FormattedType, org.Type)
		if name == "" && title == "" && department == "" && location == "" && job == "" {
			continue
		}
		out.OrgDetails = append(out.OrgDetails, ContactOrganization{
			Name: name, Title: title, Department: department, Location: location, JobDescription: job, Type: orgType,
		})
		label := name
		if label == "" {
			label = title
		}
		out.Organizations = append(out.Organizations, label)
	}
	for _, addr := range person.Addresses {
		if addr == nil {
			continue
		}
		v := strings.TrimSpace(addr.FormattedValue)
		if v == "" {
			parts := []string{
				addr.StreetAddress, addr.ExtendedAddress, addr.City,
				addr.Region, addr.PostalCode, addr.Country,
			}
			v = joinNonEmpty(parts, ", ")
		}
		if v == "" {
			continue
		}
		out.Addresses = append(out.Addresses, ContactLabeledValue{
			Value: v, Type: peopleStoredType(addr.FormattedType, addr.Type),
			Street: strings.TrimSpace(addr.StreetAddress), Extended: strings.TrimSpace(addr.ExtendedAddress),
			POBox: strings.TrimSpace(addr.PoBox), City: strings.TrimSpace(addr.City),
			Region: strings.TrimSpace(addr.Region), PostalCode: strings.TrimSpace(addr.PostalCode),
			Country: strings.TrimSpace(addr.Country), CountryCode: strings.TrimSpace(addr.CountryCode),
		})
	}
	out.Birthday = formatPeopleBirthday(person.Birthdays)
	for _, event := range person.Events {
		if event == nil {
			continue
		}
		if date := formatPeopleDate(event.Date); date != "" {
			out.Events = append(out.Events, ContactEvent{Date: date, Type: peopleStoredType(event.FormattedType, event.Type)})
		}
	}
	var notes []string
	for _, bio := range person.Biographies {
		if bio == nil {
			continue
		}
		if v := strings.TrimSpace(bio.Value); v != "" {
			notes = append(notes, v)
		}
	}
	out.Notes = strings.Join(notes, "\n\n")
	for _, website := range person.Urls {
		if website == nil {
			continue
		}
		if v := strings.TrimSpace(website.Value); v != "" {
			out.Websites = append(out.Websites, ContactLabeledValue{Value: v, Type: peopleStoredType(website.FormattedType, website.Type)})
		}
	}
	for _, relation := range person.Relations {
		if relation == nil {
			continue
		}
		if v := strings.TrimSpace(relation.Person); v != "" {
			out.Relations = append(out.Relations, ContactLabeledValue{Value: v, Type: peopleStoredType(relation.FormattedType, relation.Type)})
		}
	}
	for _, custom := range person.UserDefined {
		if custom == nil {
			continue
		}
		key, value := strings.TrimSpace(custom.Key), strings.TrimSpace(custom.Value)
		if key == "" && value == "" {
			continue
		}
		out.CustomFields = append(out.CustomFields, ContactCustomField{Key: key, Value: value})
	}
	for _, member := range person.Memberships {
		if member == nil || member.ContactGroupMembership == nil {
			continue
		}
		if id := strings.TrimSpace(member.ContactGroupMembership.ContactGroupResourceName); id != "" {
			out.GroupIDs = append(out.GroupIDs, id)
		}
	}
	for _, photo := range person.Photos {
		if photo == nil || photo.Default {
			continue
		}
		if v := strings.TrimSpace(photo.Url); v != "" {
			out.PhotoURL = v
			break
		}
	}
	return out
}

// peopleStoredType keeps the label Google shows. formattedType is "Work", "Blog", or the custom text.
// The raw type is often a code such as work or other, so it is only the fallback.
func peopleStoredType(formatted, raw string) string {
	if v := strings.TrimSpace(formatted); v != "" {
		return v
	}
	return strings.TrimSpace(raw)
}

func contactNameFrom(name *people.Name) ContactName {
	return ContactName{
		Given: strings.TrimSpace(name.GivenName), Middle: strings.TrimSpace(name.MiddleName), Family: strings.TrimSpace(name.FamilyName),
		Prefix: strings.TrimSpace(name.HonorificPrefix), Suffix: strings.TrimSpace(name.HonorificSuffix),
		PhoneticGiven: strings.TrimSpace(name.PhoneticGivenName), PhoneticMiddle: strings.TrimSpace(name.PhoneticMiddleName),
		PhoneticFamily: strings.TrimSpace(name.PhoneticFamilyName), PhoneticFull: strings.TrimSpace(name.PhoneticFullName),
		Unstructured: strings.TrimSpace(name.UnstructuredName),
	}
}

func joinNonEmpty(parts []string, sep string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return strings.Join(out, sep)
}

func formatPeopleBirthday(days []*people.Birthday) string {
	for _, b := range days {
		if b == nil {
			continue
		}
		if date := formatPeopleDate(b.Date); date != "" {
			return date
		}
	}
	return ""
}

func formatPeopleDate(d *people.Date) string {
	if d == nil {
		return ""
	}
	if d.Year > 0 && d.Month > 0 && d.Day > 0 {
		return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
	}
	if d.Month > 0 && d.Day > 0 {
		return fmt.Sprintf("%02d-%02d", d.Month, d.Day)
	}
	return ""
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

const contactsObjectTitleSep = " - "

func looksLikeGoogleContactKeyID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 || (value[0] != 'c' && value[0] != 'C') {
		return false
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// SanitizeContactKeyName makes a display name safe as a vault filename segment.
func SanitizeContactKeyName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	if name == "" || looksLikeGoogleContactKeyID(name) {
		return ""
	}
	return name
}

// ContactKeyDisplayName is the list label encoded into the object key (Gmail-style).
func ContactKeyDisplayName(item FlatContact) string {
	if name := SanitizeContactKeyName(item.Name); name != "" {
		return name
	}
	for _, email := range item.Emails {
		if v := strings.TrimSpace(email); v != "" {
			return SanitizeContactKeyName(v)
		}
	}
	for _, row := range item.EmailDetails {
		if v := strings.TrimSpace(row.Value); v != "" {
			return SanitizeContactKeyName(v)
		}
	}
	return ""
}

// ContactsObjectTitle is the leaf filename without .json: "{name} - {id}" or "{id}".
func ContactsObjectTitle(displayName, contactID string) string {
	id := strings.TrimSpace(contactID)
	name := SanitizeContactKeyName(displayName)
	if name == "" || id == "" {
		return id
	}
	return name + contactsObjectTitleSep + id
}

// ParseContactsObjectFilename reads a named or legacy contact leaf (`Priya Shah - c123.json` / `c123.json`).
func ParseContactsObjectFilename(filename string) (name, id string) {
	filename = strings.TrimSpace(filename)
	filename = strings.TrimSuffix(filename, ".json")
	if looksLikeGoogleContactKeyID(filename) {
		return "", filename
	}
	if i := strings.LastIndex(filename, contactsObjectTitleSep); i >= 0 {
		id = strings.TrimSpace(filename[i+len(contactsObjectTitleSep):])
		name = strings.TrimSpace(filename[:i])
		if looksLikeGoogleContactKeyID(id) {
			return name, id
		}
	}
	return "", filename
}

// ContactsIDFromObjectKey returns the People id from a dated vault key.
func ContactsIDFromObjectKey(objectKey string) string {
	_, id := ParseContactsObjectFilename(path.Base(strings.TrimSpace(objectKey)))
	if looksLikeGoogleContactKeyID(id) {
		return id
	}
	return strings.TrimSpace(id)
}

// ContactsObjectKey returns storage path: {email}/contacts/{yyyy}/{mm}/{dd}/{name} - {contactId}.json
// Older backups used {contactId}.json only. sourceUpdatedAt is People API sources[].updateTime.
func ContactsObjectKey(email, resourceName, sourceUpdatedAt, displayName string) string {
	id := ContactsIDFromResourceName(resourceName)
	return fmt.Sprintf("%s/contacts/%s/%s.json",
		strings.TrimSpace(email),
		ObjectKeyDatePathFromRFC3339(sourceUpdatedAt),
		ContactsObjectTitle(displayName, id),
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
		// Expect yyyy/mm/dd/{name - id|id}
		parts := strings.Split(segment, "/")
		if len(parts) != 4 {
			continue
		}
		if id := ContactsIDFromObjectKey(key); id != "" {
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

// ContactBackupStale reports that Google edited the contact after the vault file's date.
func ContactBackupStale(existingKey, sourceUpdatedAt string) bool {
	sourceUpdatedAt = strings.TrimSpace(sourceUpdatedAt)
	existingKey = strings.TrimSpace(existingKey)
	if sourceUpdatedAt == "" || existingKey == "" {
		return false
	}
	const marker = "/contacts/"
	idx := strings.Index(existingKey, marker)
	if idx < 0 {
		return false
	}
	rest := strings.TrimSuffix(existingKey[idx+len(marker):], ".json")
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return false
	}
	got := parts[0] + "/" + parts[1] + "/" + parts[2]
	return got != ObjectKeyDatePathFromRFC3339(sourceUpdatedAt)
}

// ExistingContactObjectKey is the vault key of a contact already backed up, or "".
func ExistingContactObjectKey(syncedMap map[string]bool, email, resourceName string) string {
	if syncedMap == nil {
		return ""
	}
	id := ContactsIDFromResourceName(resourceName)
	if id == "" {
		return ""
	}
	prefix := strings.TrimSpace(email) + "/contacts/"
	for key := range syncedMap {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".json")
		if len(strings.Split(rest, "/")) != 4 {
			continue
		}
		if ContactsIDFromObjectKey(key) == id {
			return key
		}
	}
	return ""
}

// IsContactSynced checks whether a contact object exists in dated synced_objects paths.
func IsContactSynced(syncedMap map[string]bool, email, resourceName string) bool {
	return ExistingContactObjectKey(syncedMap, email, resourceName) != ""
}

// IsContactsRestoreObjectKey returns true for backed-up contact JSON paths (not placeholders).
func IsContactsRestoreObjectKey(objectKey string) bool {
	objectKey = strings.TrimSpace(objectKey)
	return strings.Contains(objectKey, "/contacts/") && strings.HasSuffix(objectKey, ".json") && !strings.Contains(objectKey, ".file_placeholder")
}

// RestoreContactFromBackup creates a Google contact from vault JSON.
// seen is the target mailbox; a matching email or phone is skipped so restore-all
// does not keep adding the same extras for the owner or an invited member.
func RestoreContactFromBackup(ctx context.Context, service *people.Service, data []byte, seen *ContactIdentityIndex) error {
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
	if seen != nil && seen.HasPerson(person) {
		return nil
	}
	attachContactLabels(ctx, service, person, backup.Labels)
	created, err := service.People.CreateContact(person).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("create contact: %w", err)
	}
	if seen != nil {
		seen.AddPerson(person)
		seen.AddPerson(created)
	}
	restoreContactPhoto(ctx, service, created, backup.Photo)
	return nil
}

func personFromContactsBackup(backup ContactsBackupObject) *people.Person {
	person := &people.Person{}
	// displayName is OUTPUT ONLY on createContact — use the writable name parts.
	if len(backup.Names) > 0 {
		for _, n := range backup.Names {
			if stored := peopleNameFrom(n); stored != nil {
				person.Names = append(person.Names, stored)
			}
		}
	}
	if len(person.Names) == 0 {
		if name := strings.TrimSpace(backup.Name); name != "" {
			person.Names = []*people.Name{{UnstructuredName: name}}
		}
	}
	if len(backup.EmailDetails) > 0 {
		for _, e := range backup.EmailDetails {
			if v := strings.TrimSpace(e.Value); v != "" {
				person.EmailAddresses = append(person.EmailAddresses, &people.EmailAddress{Value: v, Type: strings.TrimSpace(e.Type)})
			}
		}
	} else {
		for _, e := range backup.Emails {
			if v := strings.TrimSpace(e); v != "" {
				person.EmailAddresses = append(person.EmailAddresses, &people.EmailAddress{Value: v})
			}
		}
	}
	if len(backup.PhoneDetails) > 0 {
		for _, p := range backup.PhoneDetails {
			if v := strings.TrimSpace(p.Value); v != "" {
				person.PhoneNumbers = append(person.PhoneNumbers, &people.PhoneNumber{Value: v, Type: strings.TrimSpace(p.Type)})
			}
		}
	} else {
		for _, p := range backup.Phones {
			if v := strings.TrimSpace(p); v != "" {
				person.PhoneNumbers = append(person.PhoneNumbers, &people.PhoneNumber{Value: v})
			}
		}
	}
	if len(backup.OrgDetails) > 0 {
		for _, o := range backup.OrgDetails {
			if strings.TrimSpace(o.Name) == "" && strings.TrimSpace(o.Title) == "" && strings.TrimSpace(o.Department) == "" && strings.TrimSpace(o.Location) == "" && strings.TrimSpace(o.JobDescription) == "" {
				continue
			}
			person.Organizations = append(person.Organizations, &people.Organization{
				Name: o.Name, Title: o.Title, Department: o.Department, Location: o.Location, JobDescription: o.JobDescription, Type: strings.TrimSpace(o.Type),
			})
		}
	} else {
		for _, o := range backup.Organizations {
			if v := strings.TrimSpace(o); v != "" {
				person.Organizations = append(person.Organizations, &people.Organization{Name: v})
			}
		}
	}
	for _, a := range backup.Addresses {
		v := strings.TrimSpace(a.Value)
		street := strings.TrimSpace(a.Street)
		if v == "" && street == "" && strings.TrimSpace(a.City) == "" && strings.TrimSpace(a.PostalCode) == "" {
			continue
		}
		person.Addresses = append(person.Addresses, &people.Address{
			FormattedValue: v, Type: strings.TrimSpace(a.Type),
			StreetAddress: street, ExtendedAddress: strings.TrimSpace(a.Extended), PoBox: strings.TrimSpace(a.POBox),
			City: strings.TrimSpace(a.City), Region: strings.TrimSpace(a.Region), PostalCode: strings.TrimSpace(a.PostalCode),
			Country: strings.TrimSpace(a.Country), CountryCode: strings.TrimSpace(a.CountryCode),
		})
	}
	if bday := strings.TrimSpace(backup.Birthday); bday != "" {
		if date := birthdayDate(bday); date != nil {
			person.Birthdays = []*people.Birthday{{Date: date}}
		}
	}
	if note := strings.TrimSpace(backup.Notes); note != "" {
		person.Biographies = []*people.Biography{{Value: note}}
	}
	for _, nick := range backup.Nicknames {
		if v := strings.TrimSpace(nick.Value); v != "" {
			person.Nicknames = append(person.Nicknames, &people.Nickname{Value: v, Type: strings.TrimSpace(nick.Type)})
		}
	}
	for _, fileAs := range backup.FileAs {
		if v := strings.TrimSpace(fileAs); v != "" {
			person.FileAses = append(person.FileAses, &people.FileAs{Value: v})
		}
	}
	for _, event := range backup.Events {
		if date := birthdayDate(event.Date); date != nil {
			person.Events = append(person.Events, &people.Event{Date: date, Type: strings.TrimSpace(event.Type)})
		}
	}
	for _, website := range backup.Websites {
		if v := strings.TrimSpace(website.Value); v != "" {
			person.Urls = append(person.Urls, &people.Url{Value: v, Type: strings.TrimSpace(website.Type)})
		}
	}
	for _, relation := range backup.Relations {
		if v := strings.TrimSpace(relation.Value); v != "" {
			person.Relations = append(person.Relations, &people.Relation{Person: v, Type: strings.TrimSpace(relation.Type)})
		}
	}
	for _, custom := range backup.CustomFields {
		key, value := strings.TrimSpace(custom.Key), strings.TrimSpace(custom.Value)
		if key == "" && value == "" {
			continue
		}
		person.UserDefined = append(person.UserDefined, &people.UserDefined{Key: key, Value: value})
	}
	if len(person.Names) == 0 && len(person.EmailAddresses) == 0 && len(person.PhoneNumbers) == 0 && len(person.Organizations) == 0 && len(person.Addresses) == 0 && len(person.Biographies) == 0 && len(person.Nicknames) == 0 && len(person.FileAses) == 0 && len(person.Events) == 0 && len(person.Urls) == 0 && len(person.Relations) == 0 && len(person.UserDefined) == 0 {
		return nil
	}
	return person
}

func peopleNameFrom(n ContactName) *people.Name {
	name := &people.Name{
		GivenName: strings.TrimSpace(n.Given), MiddleName: strings.TrimSpace(n.Middle), FamilyName: strings.TrimSpace(n.Family),
		HonorificPrefix: strings.TrimSpace(n.Prefix), HonorificSuffix: strings.TrimSpace(n.Suffix),
		PhoneticGivenName: strings.TrimSpace(n.PhoneticGiven), PhoneticMiddleName: strings.TrimSpace(n.PhoneticMiddle),
		PhoneticFamilyName: strings.TrimSpace(n.PhoneticFamily), PhoneticFullName: strings.TrimSpace(n.PhoneticFull),
		UnstructuredName: strings.TrimSpace(n.Unstructured),
	}
	if name.GivenName == "" && name.MiddleName == "" && name.FamilyName == "" && name.HonorificPrefix == "" && name.HonorificSuffix == "" && name.PhoneticGivenName == "" && name.PhoneticMiddleName == "" && name.PhoneticFamilyName == "" && name.PhoneticFullName == "" && name.UnstructuredName == "" {
		return nil
	}
	return name
}

func birthdayDate(raw string) *people.Date {
	parts := strings.Split(raw, "-")
	date := &people.Date{}
	switch len(parts) {
	case 3:
		var y, m, d int64
		if _, err := fmt.Sscanf(raw, "%d-%d-%d", &y, &m, &d); err != nil || m == 0 || d == 0 {
			return nil
		}
		date.Year, date.Month, date.Day = y, m, d
	case 2:
		var m, d int64
		if _, err := fmt.Sscanf(raw, "%d-%d", &m, &d); err != nil || m == 0 || d == 0 {
			return nil
		}
		date.Month, date.Day = m, d
	default:
		return nil
	}
	return date
}

// BackupFromFlat copies a listed contact into the vault object. Photo bytes are filled by the sync.
func BackupFromFlat(item FlatContact) ContactsBackupObject {
	return ContactsBackupObject{
		ResourceName: item.ID, Name: item.Name, Phones: item.Phones, Emails: item.Emails,
		Organizations: item.Organizations, PhoneDetails: item.PhoneDetails, EmailDetails: item.EmailDetails,
		OrgDetails: item.OrgDetails, Addresses: item.Addresses, Birthday: item.Birthday, Notes: item.Notes,
		Names: item.Names, Nicknames: item.Nicknames, FileAs: item.FileAs, Events: item.Events,
		Websites: item.Websites, Relations: item.Relations, CustomFields: item.CustomFields,
		Labels: item.Labels, PhotoURL: item.PhotoURL, ETag: item.ETag, SourceUpdatedAt: item.SourceUpdatedAt,
	}
}

// ListUserContactGroups returns user-created label resource names and their names.
func ListUserContactGroups(ctx context.Context, service *people.Service) (map[string]string, error) {
	if service == nil {
		return nil, fmt.Errorf("people service is nil")
	}
	out := map[string]string{}
	token := ""
	for {
		call := service.ContactGroups.List().PageSize(1000).GroupFields("name,groupType").Context(ctx)
		if token != "" {
			call = call.PageToken(token)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("list contact groups: %w", err)
		}
		for _, g := range resp.ContactGroups {
			if g == nil || g.GroupType != "USER_CONTACT_GROUP" {
				continue
			}
			name := strings.TrimSpace(g.Name)
			if name == "" || strings.TrimSpace(g.ResourceName) == "" {
				continue
			}
			out[g.ResourceName] = name
		}
		token = strings.TrimSpace(resp.NextPageToken)
		if token == "" {
			break
		}
	}
	return out, nil
}

// ApplyContactGroupNames fills Labels from user contact groups. System groups are left out.
func ApplyContactGroupNames(item *FlatContact, groups map[string]string) {
	if item == nil || len(groups) == 0 {
		return
	}
	seen := map[string]struct{}{}
	item.Labels = nil
	for _, id := range item.GroupIDs {
		name := strings.TrimSpace(groups[id])
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		item.Labels = append(item.Labels, name)
	}
}

// ContactsPhotoClient is the HTTP client that can read a contact photo.
// Google photo links reject a download that has no contact token.
func ContactsPhotoClient(ctx context.Context, accessToken, mailbox string, useDWD bool) (*http.Client, error) {
	if useDWD {
		return jwtHTTPClientForBackupDelegation(ctx, mailbox, "contacts")
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("contact photo access token is empty")
	}
	return oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken})), nil
}

// DownloadContactPhoto reads a Google-hosted contact photo and returns standard base64.
func DownloadContactPhoto(ctx context.Context, rawURL string, client *http.Client) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", nil
	}
	if !contactPhotoURLAllowed(rawURL) {
		return "", fmt.Errorf("contact photo url is not a google host")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("contact photo http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, contactPhotoMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) == 0 || len(body) > contactPhotoMaxBytes {
		return "", fmt.Errorf("contact photo is empty or too large")
	}
	return base64.StdEncoding.EncodeToString(body), nil
}

func contactPhotoURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{"googleusercontent.com", "google.com", "ggpht.com"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

type contactGroupCache struct {
	mu     sync.Mutex
	byName map[string]string
	loaded bool
}

var restoreGroupCaches sync.Map

func contactGroupsFor(service *people.Service) *contactGroupCache {
	if v, ok := restoreGroupCaches.Load(service); ok {
		return v.(*contactGroupCache)
	}
	c := &contactGroupCache{byName: map[string]string{}}
	actual, _ := restoreGroupCaches.LoadOrStore(service, c)
	return actual.(*contactGroupCache)
}

func attachContactLabels(ctx context.Context, service *people.Service, person *people.Person, labels []string) {
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		resource, err := ensureContactGroup(ctx, service, label)
		if err != nil {
			logger.Warn(ctx, "contact label not restored", logger.String("label", label), logger.ErrorField(err))
			continue
		}
		person.Memberships = append(person.Memberships, &people.Membership{
			ContactGroupMembership: &people.ContactGroupMembership{ContactGroupResourceName: resource},
		})
	}
}

func ensureContactGroup(ctx context.Context, service *people.Service, name string) (string, error) {
	cache := contactGroupsFor(service)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !cache.loaded {
		groups, err := ListUserContactGroups(ctx, service)
		if err != nil {
			return "", err
		}
		for resource, groupName := range groups {
			cache.byName[strings.ToLower(groupName)] = resource
		}
		cache.loaded = true
	}
	key := strings.ToLower(name)
	if resource, ok := cache.byName[key]; ok {
		return resource, nil
	}
	created, err := service.ContactGroups.Create(&people.CreateContactGroupRequest{
		ContactGroup: &people.ContactGroup{Name: name},
	}).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	resource := strings.TrimSpace(created.ResourceName)
	if resource == "" {
		return "", fmt.Errorf("contact group %q was created without an id", name)
	}
	cache.byName[key] = resource
	return resource, nil
}

func restoreContactPhoto(ctx context.Context, service *people.Service, created *people.Person, photo string) {
	photo = strings.TrimSpace(photo)
	if photo == "" || created == nil || strings.TrimSpace(created.ResourceName) == "" {
		return
	}
	if _, err := base64.StdEncoding.DecodeString(photo); err != nil {
		logger.Warn(ctx, "contact photo backup is not base64", logger.ErrorField(err))
		return
	}
	if _, err := service.People.UpdateContactPhoto(created.ResourceName, &people.UpdateContactPhotoRequest{PhotoBytes: photo}).Context(ctx).Do(); err != nil {
		logger.Warn(ctx, "contact photo not restored", logger.String("contact", created.ResourceName), logger.ErrorField(err))
	}
}

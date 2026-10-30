package outlook

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ContactCard is a vault contact shaped for UI detail screens (Outlook card parity).
// Vault objects remain raw Graph JSON; this helper maps Graph keys → display labels.
//
// Graph → UI:
//
//	emailAddresses → Email, businessPhones → Phone · Work, homePhones → Phone · Home,
//	mobilePhone → Phone · Mobile, spouseName → Significant other, categories → Categories,
//	imAddresses → Chat, companyName → Company, businessHomePage → Website,
//	jobTitle → Job title, birthday → Birthday, personalNotes → Notes,
//	homeAddress / businessAddress / otherAddress → Address · *,
//	Anniversary → ContactAnniversaryFromBackup (extended prop, not a first-class field).
type ContactCard struct {
	ID               string              `json:"id,omitempty"`
	DisplayName      string              `json:"display_name,omitempty"`
	GivenName        string              `json:"given_name,omitempty"`
	Surname          string              `json:"surname,omitempty"`
	JobTitle         string              `json:"job_title,omitempty"`
	CompanyName      string              `json:"company_name,omitempty"`
	Department       string              `json:"department,omitempty"`
	Emails           []ContactCardEmail  `json:"emails,omitempty"`
	Phones           []ContactCardPhone  `json:"phones,omitempty"`
	Addresses        []ContactCardAddr   `json:"addresses,omitempty"`
	Birthday         string              `json:"birthday,omitempty"`
	Anniversary      string              `json:"anniversary,omitempty"`
	SignificantOther string              `json:"significant_other,omitempty"`
	Categories       []string            `json:"categories,omitempty"`
	Chat             []string            `json:"chat,omitempty"`
	Website          string              `json:"website,omitempty"`
	Notes            string              `json:"notes,omitempty"`
	HasPhoto         bool                `json:"has_photo,omitempty"`
	Photo            *ContactPhotoBackup `json:"photo,omitempty"`          // vault storx_contact_photo
	PhotoDataURL     string              `json:"photo_data_url,omitempty"` // data:<ct>;base64,... for <img src>
}

// ContactCardEmail is one email on a ContactCard.
type ContactCardEmail struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"` // work / home / other when known
}

// ContactCardPhone is one phone on a ContactCard.
type ContactCardPhone struct {
	Number string `json:"number"`
	Type   string `json:"type,omitempty"` // work / home / mobile
}

// ContactCardAddr is one postal address on a ContactCard.
type ContactCardAddr struct {
	Type            string `json:"type,omitempty"` // home / work / other
	Street          string `json:"street,omitempty"`
	City            string `json:"city,omitempty"`
	State           string `json:"state,omitempty"`
	PostalCode      string `json:"postal_code,omitempty"`
	CountryOrRegion string `json:"country_or_region,omitempty"`
	Line            string `json:"line,omitempty"` // single-line summary for UI
}

// ContactCardFromBackup maps a vault contact JSON (Graph shape) into ContactCard for UI.
// Legacy FlatContact (display_name) is also accepted.
func ContactCardFromBackup(raw []byte) (ContactCard, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ContactCard{}, fmt.Errorf("parse contact: %w", err)
	}
	if _, flat := m["display_name"]; flat {
		return contactCardFromLegacy(raw)
	}
	out := ContactCard{
		ID:               strField(m, "id"),
		DisplayName:      strField(m, "displayName"),
		GivenName:        strField(m, "givenName"),
		Surname:          strField(m, "surname"),
		JobTitle:         strField(m, "jobTitle"),
		CompanyName:      strField(m, "companyName"),
		Department:       strField(m, "department"),
		Birthday:         strField(m, "birthday"),
		Anniversary:      ContactAnniversaryFromBackup(raw),
		SignificantOther: strField(m, "spouseName"),
		Website:          strField(m, "businessHomePage"),
		Notes:            strField(m, "personalNotes"),
	}
	if photo, ok := ReadContactPhotoBackup(raw); ok {
		out.HasPhoto = true
		p := photo
		out.Photo = &p
		ct := strings.TrimSpace(photo.ContentType)
		if ct == "" {
			ct = "image/jpeg"
		}
		out.PhotoDataURL = "data:" + ct + ";base64," + photo.Data
	}
	if cats, ok := m["categories"].([]any); ok {
		for _, c := range cats {
			if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
				out.Categories = append(out.Categories, s)
			}
		}
	}
	if ims, ok := m["imAddresses"].([]any); ok {
		for _, c := range ims {
			if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
				out.Chat = append(out.Chat, s)
			}
		}
	}
	if emails, ok := m["emailAddresses"].([]any); ok {
		for _, e := range emails {
			em, _ := e.(map[string]any)
			addr := strField(em, "address")
			if addr == "" {
				continue
			}
			out.Emails = append(out.Emails, ContactCardEmail{
				Address: addr, Name: strField(em, "name"), Type: "work",
			})
		}
	}
	for _, p := range strList(m, "businessPhones") {
		out.Phones = append(out.Phones, ContactCardPhone{Number: p, Type: "work"})
	}
	for _, p := range strList(m, "homePhones") {
		out.Phones = append(out.Phones, ContactCardPhone{Number: p, Type: "home"})
	}
	if mob := strField(m, "mobilePhone"); mob != "" {
		out.Phones = append(out.Phones, ContactCardPhone{Number: mob, Type: "mobile"})
	}
	for key, typ := range map[string]string{
		"homeAddress": "home", "businessAddress": "work", "otherAddress": "other",
	} {
		if addr, ok := m[key].(map[string]any); ok {
			if ca := contactCardAddr(typ, addr); ca.Line != "" || ca.Street != "" {
				out.Addresses = append(out.Addresses, ca)
			}
		}
	}
	return out, nil
}

func contactCardFromLegacy(raw []byte) (ContactCard, error) {
	var c FlatContact
	if err := json.Unmarshal(raw, &c); err != nil {
		return ContactCard{}, err
	}
	out := ContactCard{
		ID: c.ID, DisplayName: c.DisplayName, GivenName: c.GivenName, Surname: c.Surname,
		CompanyName: c.CompanyName, JobTitle: c.JobTitle,
	}
	for _, e := range c.Emails {
		if e = strings.TrimSpace(e); e != "" {
			out.Emails = append(out.Emails, ContactCardEmail{Address: e, Type: "work"})
		}
	}
	for _, p := range c.Phones {
		if p = strings.TrimSpace(p); p != "" {
			out.Phones = append(out.Phones, ContactCardPhone{Number: p, Type: "work"})
		}
	}
	return out, nil
}

func contactCardAddr(typ string, m map[string]any) ContactCardAddr {
	a := ContactCardAddr{
		Type:   typ,
		Street: strField(m, "street"), City: strField(m, "city"), State: strField(m, "state"),
		PostalCode: strField(m, "postalCode"), CountryOrRegion: strField(m, "countryOrRegion"),
	}
	parts := []string{a.Street, a.City, a.State, a.PostalCode, a.CountryOrRegion}
	var nonEmpty []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	a.Line = strings.Join(nonEmpty, ", ")
	return a
}

func strField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func strList(m map[string]any, key string) []string {
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

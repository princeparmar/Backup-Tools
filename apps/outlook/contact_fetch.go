package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PidLidWeddingAnniversary — Outlook "Anniversary" on a contact (not a v1.0 first-class field).
const contactAnniversaryPropID = "SystemTime {00062004-0000-0000-C000-000000000046} Id 0x8008"

// OutlookContactsLayoutFull is the layout that stores full Graph contacts including anniversary
// extended properties and photos.
const OutlookContactsLayoutFull = 2

// ContactItemURL is GET …/contacts/{id} (folder-scoped when folderID set).
func ContactItemURL(userBase, folderID, contactID string) string {
	base := strings.TrimRight(userBase, "/")
	contactID = url.PathEscape(strings.TrimSpace(contactID))
	if folderID = strings.TrimSpace(folderID); folderID != "" {
		return base + "/contactFolders/" + url.PathEscape(folderID) + "/contacts/" + contactID
	}
	return base + "/contacts/" + contactID
}

// contactGraphSelect is the explicit Graph property set we store for vault + restore + UI.
// Mirrors Outlook contact card fields (Significant other→spouseName, Chat→imAddresses,
// Website→businessHomePage, Categories→categories, Anniversary via $expand below).
const contactGraphSelect = "id,changeKey,parentFolderId,createdDateTime,lastModifiedDateTime," +
	"displayName,givenName,surname,middleName,nickName,title,fileAs,generation,initials," +
	"emailAddresses,businessPhones,homePhones,mobilePhone," +
	"businessAddress,homeAddress,otherAddress," +
	"companyName,jobTitle,department,officeLocation,profession," +
	"businessHomePage,imAddresses,categories,personalNotes,spouseName,children," +
	"birthday,assistantName,manager," +
	"yomiCompanyName,yomiGivenName,yomiSurname"

// FetchOutlookContactRaw loads one contact JSON including the Anniversary extended property.
func FetchOutlookContactRaw(ctx context.Context, accessToken, userBase, folderID, contactID string) ([]byte, error) {
	contactID = strings.TrimSpace(contactID)
	if contactID == "" || strings.TrimSpace(userBase) == "" {
		return nil, fmt.Errorf("user base and contact id are required")
	}
	expand := "singleValueExtendedProperties($filter=id eq '" + contactAnniversaryPropID + "')"
	q := url.Values{}
	q.Set("$select", contactGraphSelect)
	q.Set("$expand", expand)
	reqURL := ContactItemURL(userBase, folderID, contactID) + "?" + q.Encode()
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("contact http %d: %s", status, truncateForErr(body))
	}
	return body, nil
}

// ContactAnniversaryFromBackup returns the Anniversary date stored on a contact backup, if any.
func ContactAnniversaryFromBackup(raw []byte) string {
	var doc struct {
		Props []struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		} `json:"singleValueExtendedProperties"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	for _, p := range doc.Props {
		if strings.EqualFold(strings.TrimSpace(p.ID), contactAnniversaryPropID) {
			return strings.TrimSpace(p.Value)
		}
	}
	return ""
}

// contactAnniversaryCreateProps keeps only the Anniversary extended property for Graph create.
func contactAnniversaryCreateProps(raw []byte) []map[string]string {
	v := ContactAnniversaryFromBackup(raw)
	if v == "" {
		return nil
	}
	return []map[string]string{{"id": contactAnniversaryPropID, "value": v}}
}

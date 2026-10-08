package mstenant

import (
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

var capabilityForService = map[string]string{
	"outlook":            outlook.CapabilityMail,
	"mail":               outlook.CapabilityMail,
	"outlook_calendar":   outlook.CapabilityCalendar,
	"calendar":           outlook.CapabilityCalendar,
	"outlook_contacts":   outlook.CapabilityContacts,
	"contacts":           outlook.CapabilityContacts,
	"outlook_onedrive":   outlook.CapabilityOneDrive,
	"onedrive":           outlook.CapabilityOneDrive,
	"outlook_sharepoint": outlook.CapabilitySharePoint,
	"sharepoint":         outlook.CapabilitySharePoint,
	"outlook_teams":      outlook.CapabilityTeamsChannel,
	"teams":              outlook.CapabilityTeamsChannel,
	"outlook_groups":     outlook.CapabilityGroups,
	"groups":             outlook.CapabilityGroups,
}

// CapabilityForService returns the capability required by a service or job method ("" if none).
func CapabilityForService(service string) string {
	return capabilityForService[strings.ToLower(strings.TrimSpace(service))]
}

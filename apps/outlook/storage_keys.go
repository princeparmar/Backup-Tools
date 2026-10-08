package outlook

import (
	"regexp"
	"strings"
)

var resourceKeySegmentReplacer = strings.NewReplacer(
	"/", "_", "\\", "_", ":", "_", "?", "_", "*", "_", "#", "_", ",", "_",
)

var tenantIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// keyResourceTypes are the resource types that appear in Microsoft storage keys.
var keyResourceTypes = map[string]bool{"user": true, "site": true, "team": true, "group": true}

// ResourceKeyPrefix is the storage prefix of a Microsoft resource inside its service bucket:
// {tenant_id}/{resource_type}/{resource_id}. Every Microsoft object key starts with it, so the
// same mailbox name or ID in two tenants never shares keys. Returns "" when any part is missing.
func ResourceKeyPrefix(tenantID, resourceType, resourceID string) string {
	tenantID = strings.ToLower(strings.TrimSpace(tenantID))
	resourceType = strings.ToLower(strings.TrimSpace(resourceType))
	resourceID = resourceKeySegmentReplacer.Replace(strings.TrimSpace(resourceID))
	if tenantID == "" || resourceType == "" || resourceID == "" {
		return ""
	}
	return tenantID + "/" + resourceType + "/" + resourceID
}

// SplitResourceKey splits an object key written under ResourceKeyPrefix into the prefix, its
// parts and the remainder. ok is false for keys in any other layout.
func SplitResourceKey(objectKey string) (prefix, tenantID, resourceType, resourceID, rest string, ok bool) {
	parts := strings.SplitN(strings.Trim(objectKey, "/"), "/", 4)
	if len(parts) < 3 || !tenantIDPattern.MatchString(parts[0]) || !keyResourceTypes[parts[1]] || parts[2] == "" {
		return "", "", "", "", "", false
	}
	if len(parts) == 4 {
		rest = parts[3]
	}
	return strings.Join(parts[:3], "/"), parts[0], parts[1], parts[2], rest, true
}

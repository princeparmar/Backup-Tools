package restore

import "strings"

// ShouldSkipObjectKey filters placeholders and empty keys (shared by processors and tests).
func ShouldSkipObjectKey(objectKey string) bool {
	key := strings.TrimSpace(objectKey)
	return key == "" || strings.Contains(key, "/.file_placeholder")
}

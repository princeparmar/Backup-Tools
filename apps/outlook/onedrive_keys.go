package outlook

import (
	"maps"
	"net/url"
	"strconv"
	"strings"
)

// OneDrive backups mirror the drive like Google Drive backups, one object per file:
//
//	{prefix}/MY_DRIVE/{parentFolderId}/.../{itemId}${name}
//	{prefix}/MY_DRIVE/{parentFolderId}/.../{folderId}/.folder__{name}
//	{prefix}/BIN/{itemId}${name}
//
// prefix is ResourceKeyPrefix. Parent folders are listed by id from the drive root down, so a
// renamed folder keeps its contents' keys. Files deleted from OneDrive move to the flat BIN
// section. "$" in ids and names is written as "$$". Item details are custom object metadata.
const (
	OneDriveSectionMyDrive = "MY_DRIVE"
	OneDriveSectionBin     = "BIN"

	oneDriveFolderLeafPrefix = ".folder__"
	oneDriveIDNameSep        = "$"
)

// Custom metadata on OneDrive backup objects.
const (
	OneDriveMetaItemID     = "onedrive-item-id"
	OneDriveMetaName       = "original-name"
	OneDriveMetaMimeType   = "mime-type"
	OneDriveMetaSize       = "size"
	OneDriveMetaCTag       = "ctag"
	OneDriveMetaETag       = "etag"
	OneDriveMetaCreated    = "created-date-time"
	OneDriveMetaModified   = "last-modified-date-time"
	OneDriveMetaWebURL     = "web-url"
	OneDriveMetaParentPath = "parent-path"
	OneDriveMetaIsFolder   = "is-folder"
	OneDriveMetaRemovedAt  = "removed-at"
	OneDriveMetaBackedUpAt = "backed-up-at"
	// OneDriveMetaDriveID is the Graph drive of a SharePoint or group library backup; OneDrive
	// backups restore into the user's drive and leave it unset.
	OneDriveMetaDriveID = "drive-id"
)

// OneDriveObjectKey is a parsed OneDrive backup key.
type OneDriveObjectKey struct {
	Prefix    string
	Section   string
	ParentIDs []string
	ItemID    string
	Name      string
	IsFolder  bool
}

// OneDriveFileKey builds the key of a file. BIN keys have no parent folders.
func OneDriveFileKey(prefix, section string, parentIDs []string, itemID, name string) string {
	parts := oneDriveKeyHead(prefix, section, parentIDs)
	leaf := escapeOneDriveKeyPart(oneDriveKeyID(itemID)) + oneDriveIDNameSep + escapeOneDriveKeyPart(SanitizeOneDrivePathSegment(name))
	return strings.Join(append(parts, leaf), "/")
}

// OneDriveFolderKey builds the placeholder key of a folder in MY_DRIVE.
func OneDriveFolderKey(prefix string, parentIDs []string, folderID, name string) string {
	parts := oneDriveKeyHead(prefix, OneDriveSectionMyDrive, parentIDs)
	return strings.Join(append(parts, oneDriveKeyID(folderID), oneDriveFolderLeafPrefix+SanitizeOneDrivePathSegment(name)), "/")
}

// OneDriveFolderContentsPrefix is the key prefix of everything inside a MY_DRIVE folder.
func OneDriveFolderContentsPrefix(prefix string, parentIDs []string, folderID string) string {
	parts := oneDriveKeyHead(prefix, OneDriveSectionMyDrive, parentIDs)
	return strings.Join(append(parts, oneDriveKeyID(folderID)), "/") + "/"
}

func oneDriveKeyHead(prefix, section string, parentIDs []string) []string {
	parts := []string{strings.TrimSuffix(strings.TrimSpace(prefix), "/"), section}
	if section == OneDriveSectionBin {
		return parts
	}
	for _, id := range parentIDs {
		if id = oneDriveKeyID(id); id != "" {
			parts = append(parts, id)
		}
	}
	return parts
}

func oneDriveKeyID(id string) string {
	return strings.ReplaceAll(strings.TrimSpace(id), "/", "_")
}

func escapeOneDriveKeyPart(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}

// ParseOneDriveObjectKey parses a key written by OneDriveFileKey or OneDriveFolderKey.
func ParseOneDriveObjectKey(key string) (OneDriveObjectKey, bool) {
	prefix, _, _, _, rest, ok := SplitResourceKey(strings.TrimSpace(key))
	if !ok {
		return OneDriveObjectKey{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || (parts[0] != OneDriveSectionMyDrive && parts[0] != OneDriveSectionBin) {
		return OneDriveObjectKey{}, false
	}
	out := OneDriveObjectKey{Prefix: prefix, Section: parts[0]}
	leaf, mid := parts[len(parts)-1], parts[1:len(parts)-1]

	if name, isFolder := strings.CutPrefix(leaf, oneDriveFolderLeafPrefix); isFolder {
		if out.Section != OneDriveSectionMyDrive || len(mid) == 0 || mid[len(mid)-1] == "" {
			return OneDriveObjectKey{}, false
		}
		out.IsFolder = true
		out.ItemID, out.Name = mid[len(mid)-1], name
		out.ParentIDs = append([]string{}, mid[:len(mid)-1]...)
		return out, true
	}
	if out.Section == OneDriveSectionBin && len(mid) > 0 {
		return OneDriveObjectKey{}, false
	}
	id, name, ok := splitOneDriveLeaf(leaf)
	if !ok {
		return OneDriveObjectKey{}, false
	}
	out.ItemID, out.Name = id, name
	out.ParentIDs = append([]string{}, mid...)
	return out, true
}

// splitOneDriveLeaf splits "{id}${name}" where "$$" is a literal "$".
func splitOneDriveLeaf(s string) (id, name string, ok bool) {
	var left, right strings.Builder
	cur, sawSep := &left, false
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			cur.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			cur.WriteByte('$')
			i++
			continue
		}
		if sawSep {
			return "", "", false
		}
		sawSep, cur = true, &right
	}
	if !sawSep || left.Len() == 0 {
		return "", "", false
	}
	return left.String(), right.String(), true
}

// OneDriveLegacyKey is a key of the old two-object layout:
// {prefix}/meta/{yyyy/mm/dd}/{itemId}_{name}.json with the file at the same path under data/
// (without .json).
type OneDriveLegacyKey struct {
	Prefix string
	ItemID string
	IsMeta bool
}

// ParseOneDriveLegacyKey parses a key of the old meta/data layout.
func ParseOneDriveLegacyKey(key string) (OneDriveLegacyKey, bool) {
	prefix, _, _, _, rest, ok := SplitResourceKey(strings.TrimSpace(key))
	if !ok {
		return OneDriveLegacyKey{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 5 || (parts[0] != "meta" && parts[0] != "data") {
		return OneDriveLegacyKey{}, false
	}
	if !allDigits(parts[1], 4) || !allDigits(parts[2], 2) || !allDigits(parts[3], 2) {
		return OneDriveLegacyKey{}, false
	}
	isMeta := parts[0] == "meta"
	leaf := parts[4]
	if isMeta {
		var hasExt bool
		if leaf, hasExt = strings.CutSuffix(leaf, ".json"); !hasExt {
			return OneDriveLegacyKey{}, false
		}
	}
	id, _, _ := strings.Cut(leaf, "_")
	if id == "" {
		return OneDriveLegacyKey{}, false
	}
	return OneDriveLegacyKey{Prefix: prefix, ItemID: id, IsMeta: isMeta}, true
}

// OneDriveLegacyDataKey is the data twin of a legacy meta key.
func OneDriveLegacyDataKey(metaKey string) string {
	return strings.TrimSuffix(strings.Replace(metaKey, "/meta/", "/data/", 1), ".json")
}

// OneDriveObjectMeta is the custom metadata of a backed-up file or folder. parentPath is the
// folder names from the drive root, joined with "/".
func OneDriveObjectMeta(item *OneDriveItem, parentPath string) map[string]string {
	meta := map[string]string{
		OneDriveMetaItemID: strings.TrimSpace(item.ID),
		OneDriveMetaName:   strings.TrimSpace(item.Name),
	}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			meta[k] = v
		}
	}
	set(OneDriveMetaParentPath, parentPath)
	set(OneDriveMetaCreated, item.CreatedDateTime)
	set(OneDriveMetaModified, item.LastModifiedDateTime)
	set(OneDriveMetaWebURL, item.WebURL)
	if item.IsFolder {
		meta[OneDriveMetaIsFolder] = "true"
		return meta
	}
	set(OneDriveMetaMimeType, item.MimeType)
	set(OneDriveMetaCTag, item.CTag)
	set(OneDriveMetaETag, item.ETag)
	if item.Size > 0 {
		meta[OneDriveMetaSize] = strconv.FormatInt(item.Size, 10)
	}
	return meta
}

// OneDriveContentUnchanged reports whether stored metadata describes the same file content as
// item. The cTag changes only when content changes; without one, eTag and size are compared.
func OneDriveContentUnchanged(stored map[string]string, item *OneDriveItem) bool {
	if ctag := strings.TrimSpace(item.CTag); ctag != "" && stored[OneDriveMetaCTag] != "" {
		return stored[OneDriveMetaCTag] == ctag
	}
	etag := strings.TrimSpace(item.ETag)
	return etag != "" && stored[OneDriveMetaETag] == etag && stored[OneDriveMetaSize] == strconv.FormatInt(item.Size, 10)
}

// OneDriveRemovedMeta marks stored metadata as deleted from OneDrive at removedAt.
func OneDriveRemovedMeta(stored map[string]string, removedAt string) map[string]string {
	meta := maps.Clone(stored)
	if meta == nil {
		meta = map[string]string{}
	}
	meta[OneDriveMetaRemovedAt] = removedAt
	return meta
}

// OneDriveLegacyMetaToObjectMeta converts a legacy meta JSON record into object metadata.
func OneDriveLegacyMetaToObjectMeta(m OneDriveCronBackupMeta) map[string]string {
	item := OneDriveItem{
		ID: m.ItemID, Name: m.Name, Size: m.Size, MimeType: m.MimeType,
		CreatedDateTime: m.CreatedDateTime, LastModifiedDateTime: m.LastModifiedDateTime,
		WebURL: m.WebURL, ETag: m.ETag, CTag: m.CTag,
	}
	return OneDriveObjectMeta(&item, OneDriveParentPathFromGraph(m.ParentPath))
}

// OneDriveParentPathFromGraph turns a Graph parentReference.path ("/drive/root:/A/B") into
// the folder path "A/B".
func OneDriveParentPathFromGraph(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.Index(p, "root:"); i >= 0 {
		p = p[i+len("root:"):]
	}
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	return strings.Trim(p, "/")
}

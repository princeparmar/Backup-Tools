package outlook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// Files up to oneDriveSimpleUploadMax are restored with one PUT; larger files use an upload
// session sent in oneDriveUploadChunk pieces (Graph requires multiples of 320 KiB).
const (
	oneDriveSimpleUploadMax = 4 << 20
	oneDriveUploadChunk     = 32 * 320 << 10
)

// OneDriveRestoreSource reads OneDrive backup objects for a restore.
type OneDriveRestoreSource struct {
	Stat       func(ctx context.Context, key string) (meta map[string]string, size int64, err error)
	DownloadTo func(ctx context.Context, key string, w io.Writer) error
	// FolderNames maps folder id -> name from the backed-up folder placeholders; see
	// OneDriveFolderNames. Missing names fall back to the path saved with the file.
	FolderNames map[string]string
}

// OneDriveFolderNames maps folder id -> name for every folder placeholder among keys.
func OneDriveFolderNames(keys []string) map[string]string {
	out := map[string]string{}
	for _, key := range keys {
		if p, ok := ParseOneDriveObjectKey(key); ok && p.IsFolder && p.Name != "" {
			out[p.ItemID] = p.Name
		}
	}
	return out
}

// IsOneDriveRestoreKey reports backup keys that restore on their own: files and folders of the
// tree layout and meta keys of the legacy layout.
func IsOneDriveRestoreKey(key string) bool {
	if _, ok := ParseOneDriveObjectKey(key); ok {
		return true
	}
	k, ok := ParseOneDriveLegacyKey(key)
	return ok && k.IsMeta
}

// RestoreOneDriveBackup restores one backup key into the drive at {userBase}/drive. Files go back
// into their original folder, created when missing; folder keys recreate the folder. Files in BIN
// return to the folder they were deleted from.
func RestoreOneDriveBackup(ctx context.Context, accessToken, userBase, objectKey string, src OneDriveRestoreSource) error {
	return RestoreDriveBackup(ctx, accessToken, userDriveURL(userBase), objectKey, src)
}

// RestoreSharePointBackup restores one tree-layout SharePoint or group library backup into the
// drive recorded on the object.
func RestoreSharePointBackup(ctx context.Context, accessToken, objectKey string, src OneDriveRestoreSource) error {
	meta, _, err := src.Stat(ctx, objectKey)
	if err != nil {
		return fmt.Errorf("read backup %s: %w", objectKey, err)
	}
	driveID := strings.TrimSpace(meta[OneDriveMetaDriveID])
	if driveID == "" {
		return fmt.Errorf("sharepoint backup %s has no drive id", objectKey)
	}
	return RestoreDriveBackup(ctx, accessToken, DriveRootURLFromDriveID(driveID), objectKey, src)
}

// RestoreDriveBackup is RestoreOneDriveBackup for the drive at driveURL (".../drive" or
// ".../drives/{id}").
func RestoreDriveBackup(ctx context.Context, accessToken, driveURL, objectKey string, src OneDriveRestoreSource) error {
	if p, ok := ParseOneDriveObjectKey(objectKey); ok {
		meta, size, err := src.Stat(ctx, objectKey)
		if err != nil {
			return fmt.Errorf("read backup %s: %w", objectKey, err)
		}
		folder := oneDriveRestoreFolder(p, meta, src.FolderNames)
		name := strings.TrimSpace(meta[OneDriveMetaName])
		if name == "" {
			name = p.Name
		}
		if p.IsFolder {
			return ensureDriveFolder(ctx, accessToken, driveURL, joinOneDrivePath(folder, name))
		}
		return restoreOneDriveObject(ctx, accessToken, driveURL, folder, name, objectKey, size, src)
	}

	k, ok := ParseOneDriveLegacyKey(objectKey)
	if !ok || !k.IsMeta {
		return fmt.Errorf("not a onedrive backup key: %s", objectKey)
	}
	var raw bytes.Buffer
	if err := src.DownloadTo(ctx, objectKey, &raw); err != nil {
		return fmt.Errorf("read backup %s: %w", objectKey, err)
	}
	var m OneDriveCronBackupMeta
	if err := json.Unmarshal(raw.Bytes(), &m); err != nil {
		return fmt.Errorf("parse backup %s: %w", objectKey, err)
	}
	if m.RemovedFromOneDrive || m.IsFolder {
		return fmt.Errorf("onedrive object skipped (removed or folder)")
	}
	dataKey := strings.TrimSpace(m.DataObjectKey)
	if dataKey == "" {
		dataKey = OneDriveLegacyDataKey(objectKey)
	}
	_, size, err := src.Stat(ctx, dataKey)
	if err != nil {
		return fmt.Errorf("read backup %s: %w", dataKey, err)
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = path.Base(dataKey)
	}
	return restoreOneDriveObject(ctx, accessToken, driveURL, OneDriveParentPathFromGraph(m.ParentPath), name, dataKey, size, src)
}

func userDriveURL(userBase string) string {
	return strings.TrimRight(strings.TrimSpace(userBase), "/") + "/drive"
}

func restoreOneDriveObject(ctx context.Context, accessToken, driveURL, folder, name, key string, size int64, src OneDriveRestoreSource) error {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(src.DownloadTo(ctx, key, pw))
	}()
	defer pr.Close()
	return restoreDriveFile(ctx, accessToken, driveURL, folder, name, pr, size)
}

// oneDriveRestoreFolder is the folder path a backup returns to.
func oneDriveRestoreFolder(p OneDriveObjectKey, meta map[string]string, names map[string]string) string {
	saved := strings.TrimSpace(meta[OneDriveMetaParentPath])
	if p.Section == OneDriveSectionBin {
		return saved
	}
	parts := make([]string, 0, len(p.ParentIDs))
	for _, id := range p.ParentIDs {
		name := strings.TrimSpace(names[id])
		if name == "" {
			return saved
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, "/")
}

// RestoreOneDriveFile uploads a file into folderPath ("A/B"; "" is the root) of the drive at
// {userBase}/drive. Missing folders are created, and a file that already exists under the same
// name is kept: the restored copy is renamed. size is the content length.
func RestoreOneDriveFile(ctx context.Context, accessToken, userBase, folderPath, name string, body io.Reader, size int64) error {
	return restoreDriveFile(ctx, accessToken, userDriveURL(userBase), folderPath, name, body, size)
}

func restoreDriveFile(ctx context.Context, accessToken, driveURL, folderPath, name string, body io.Reader, size int64) error {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "restored.bin"
	}
	itemURL := strings.TrimRight(strings.TrimSpace(driveURL), "/") + "/root:/" + escapeOneDrivePath(joinOneDrivePath(folderPath, name)) + ":"
	if size <= oneDriveSimpleUploadMax {
		data, err := io.ReadAll(body)
		if err != nil {
			return fmt.Errorf("read backup content: %w", err)
		}
		return putDriveContent(ctx, accessToken, itemURL+"/content?@microsoft.graph.conflictBehavior=rename", data)
	}
	return uploadOneDriveSession(ctx, accessToken, itemURL+"/createUploadSession", body, size)
}

func uploadOneDriveSession(ctx context.Context, accessToken, sessionURL string, body io.Reader, size int64) error {
	payload, _ := json.Marshal(map[string]any{"item": map[string]string{"@microsoft.graph.conflictBehavior": "rename"}})
	resp, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, sessionURL, payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("onedrive upload session http %d: %s", status, truncateForErr(resp))
	}
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := json.Unmarshal(resp, &session); err != nil || session.UploadURL == "" {
		return fmt.Errorf("onedrive upload session has no uploadUrl")
	}

	buf := make([]byte, oneDriveUploadChunk)
	for offset := int64(0); offset < size; {
		n, err := io.ReadFull(body, buf[:min(int64(len(buf)), size-offset)])
		if err != nil {
			cancelOneDriveSession(ctx, session.UploadURL)
			return fmt.Errorf("read backup content: %w", err)
		}
		// The upload URL is pre-authorized; Graph rejects an Authorization header on it.
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, bytes.NewReader(buf[:n]))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+int64(n)-1, size))
		res, err := graphHTTPDoWithRetry(ctx, req)
		if err != nil {
			cancelOneDriveSession(ctx, session.UploadURL)
			return err
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		_ = res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			cancelOneDriveSession(ctx, session.UploadURL)
			return fmt.Errorf("onedrive upload chunk http %d: %s", res.StatusCode, truncateForErr(b))
		}
		offset += int64(n)
	}
	return nil
}

func cancelOneDriveSession(ctx context.Context, uploadURL string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, uploadURL, nil)
	if err != nil {
		return
	}
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
	}
}

// EnsureOneDriveFolder creates folderPath ("A/B") and its parents in the drive at
// {userBase}/drive; folders that already exist are kept.
func EnsureOneDriveFolder(ctx context.Context, accessToken, userBase, folderPath string) error {
	return ensureDriveFolder(ctx, accessToken, userDriveURL(userBase), folderPath)
}

func ensureDriveFolder(ctx context.Context, accessToken, driveURL, folderPath string) error {
	driveURL = strings.TrimRight(strings.TrimSpace(driveURL), "/")
	parent := ""
	for _, seg := range splitOneDrivePath(folderPath) {
		reqURL := driveURL + "/root/children"
		if parent != "" {
			reqURL = driveURL + "/root:/" + escapeOneDrivePath(parent) + ":/children"
		}
		payload, _ := json.Marshal(map[string]any{
			"name": seg, "folder": map[string]any{}, "@microsoft.graph.conflictBehavior": "fail",
		})
		resp, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, reqURL, payload)
		if err != nil {
			return err
		}
		if status != http.StatusConflict && (status < 200 || status >= 300) {
			return fmt.Errorf("onedrive create folder %q http %d: %s", seg, status, truncateForErr(resp))
		}
		parent = joinOneDrivePath(parent, seg)
	}
	return nil
}

func splitOneDrivePath(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func joinOneDrivePath(folderPath, name string) string {
	return strings.Join(append(splitOneDrivePath(folderPath), splitOneDrivePath(name)...), "/")
}

func escapeOneDrivePath(p string) string {
	segs := splitOneDrivePath(p)
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

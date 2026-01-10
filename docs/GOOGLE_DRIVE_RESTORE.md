# Google Drive Restore Implementation

## Overview

This document describes the production-grade Google Drive restore implementation that supports:
- **My Drive** files and folders (including Trash)
- **Shared Drives** (conditional on membership)
- **Shared With Me** files (restored as copies)
- **Nested folder structures** preservation
- **Trashed and permanently deleted files** handling
- **Metadata and permissions** restoration (where allowed by Google)

## Architecture

### File Structure

```
apps/google/
├── google-drive.go              # Existing Drive service functions
├── google-drive-restore.go      # NEW: Restore logic and utilities

handler/
├── google_drive_handlers.go     # Existing Drive handlers
└── google_drive_restore_handler.go  # NEW: Restore HTTP handlers
```

### Key Components

#### 1. **DriveFileMetadata** (`apps/google/google-drive-restore.go`)

Represents backup metadata for each file/folder:

```go
type DriveFileMetadata struct {
    Key          string            // Path in backup (e.g., "documents/reports/2025/file1.docx")
    Type         string            // "file" or "folder"
    Name         string            // Original name
    MimeType     string            // MIME type
    Parents      []string          // Original parent path array
    DriveID      string            // Optional: Shared Drive ID
    LocationType string            // MY_DRIVE | SHARED_DRIVE | SHARED_WITH_ME
    Permissions  []DrivePermission // Optional: list of allowed editors/viewers
    ModifiedTime string            // Last modified timestamp
    Starred      bool              // Optional boolean
}
```

#### 2. **RestoreContext** (`apps/google/google-drive-restore.go`)

Manages restore operations with folder caching and access validation:

```go
type RestoreContext struct {
    Service      *drive.Service
    FolderCache  map[string]string // Maps folder path to folder ID
    UserEmail    string
    DriveID      string            // For Shared Drive operations
    LocationType string            // MY_DRIVE | SHARED_DRIVE | SHARED_WITH_ME
}
```

#### 3. **DriveClient** (`apps/google/google-drive-restore.go`)

High-level client for restore operations:

```go
type DriveClient struct {
    Service *drive.Service
}
```

#### 4. **DriveRestoreService** (`handler/google_drive_restore_handler.go`)

Handler-level service for concurrent restore operations:

```go
type DriveRestoreService struct {
    client      *google.DriveClient
    accessGrant string
    userEmail   string
}
```

## Restore Flow

### Step 0: Detect Restore Target

The restore process begins by parsing the backup metadata to determine:
- Location type (My Drive, Shared Drive, or Shared With Me)
- Drive ID (for Shared Drives)
- Parent folder path
- File or folder type

### Step 1: Validate Access

```go
func (rc *RestoreContext) ValidateAccess(ctx context.Context, metadata *DriveFileMetadata) error
```

**Validation Logic:**

| Target       | Validation                                    | Fallback                |
|--------------|-----------------------------------------------|-------------------------|
| My Drive     | Always accessible                             | N/A                     |
| Shared Drive | Check `drives.Get(driveId)`                   | Fallback to My Drive    |
| SWM          | Always restore as copy to My Drive            | N/A                     |

### Step 2: Folder Hierarchy Rebuild

```go
func (rc *RestoreContext) RebuildFolderHierarchy(ctx context.Context, key string) (string, error)
```

**Algorithm:**
1. Split `key` by `/` to get folder hierarchy
2. For each folder level:
   - Check cache for existing folder ID
   - If not cached, check if folder exists in Drive
   - If doesn't exist, create folder
   - Cache the folder ID
3. Return the final parent folder ID

**Example:**
- Key: `documents/reports/2025/file1.docx`
- Creates: `documents/` → `documents/reports/` → `documents/reports/2025/`
- Returns: ID of `2025` folder

### Step 3: Restore Files

```go
func (rc *RestoreContext) RestoreFile(ctx context.Context, metadata *DriveFileMetadata, fileBytes []byte) error
```

#### My Drive Files

1. **Check if file exists:**
   ```go
   existingFile, err := rc.CheckFileExists(ctx, fileName, parentID)
   ```

2. **If file is trashed:**
   ```go
   rc.Service.Files.Update(fileID, &drive.File{Trashed: false})
   ```

3. **If permanently deleted:**
   ```go
   rc.CreateFile(ctx, fileName, parentID, metadata, fileBytes)
   ```

4. **If file exists and not trashed:**
   - Append "(Restored Copy)" to filename
   - Create new file

#### Shared With Me Files

Always restored as copies to `My Drive/Restored From Shared/[OriginalPath]`:

```go
func (rc *RestoreContext) CopySharedFile(ctx context.Context, metadata *DriveFileMetadata, originalFileID string) error
```

#### Shared Drive Files

Same as My Drive, but with additional flags:
```go
.SupportsAllDrives(true)
.IncludeItemsFromAllDrives(true)
.DriveId(driveID)
```

If drive is deleted or user lost membership → fallback to My Drive.

### Step 4: Restore Metadata

```go
func (rc *RestoreContext) UpdateFileMetadata(ctx context.Context, fileID string, metadata *DriveFileMetadata) error
```

**Allowed metadata:**
- ✅ Name
- ✅ ModifiedTime
- ✅ Starred
- ❌ Original fileId (new ID assigned)
- ❌ CreatedTime (cannot be changed)
- ❌ Ownership (cannot be changed)

### Step 5: Restore Permissions

```go
func (rc *RestoreContext) ApplyPermissions(ctx context.Context, fileID string, permissions []DrivePermission)
```

**Rules:**
- My Drive → reapply editors/viewers if allowed
- Shared Drive → permissions inherit by default
- Owner → cannot be changed (skipped)

### Step 6: Trash & Deleted Handling

| Case                           | Action                       |
|--------------------------------|------------------------------|
| File in Trash (My Drive)       | Restore with `Trashed=false` |
| Permanently deleted (My Drive) | Recreate new file            |
| SWM file trashed               | Copy to My Drive anyway      |
| SWM file deleted               | Cannot restore, log warning  |
| Shared Drive trashed           | Restore in drive if member   |
| Shared Drive deleted           | Fallback to My Drive         |

## API Endpoints

### 1. Restore by Keys

**Endpoint:** `POST /api/google-drive/restore`

**Request Body:**
```json
{
  "keys": [
    "user@example.com/documents/report.docx",
    "user@example.com/photos/vacation.jpg"
  ]
}
```

**Response:**
```json
{
  "processed_keys": ["user@example.com/documents/report.docx"],
  "failed_keys": ["user@example.com/photos/vacation.jpg"],
  "message": "Google Drive restore completed",
  "total_files": 1,
  "total_folders": 0
}
```

**Handler:** `HandleGoogleDriveDownloadAndRestore`

### 2. Restore by Path Pattern

**Endpoint:** `GET /api/google-drive/restore/path?path=user@example.com/documents/`

**Response:**
```json
{
  "message": "Google Drive restore completed",
  "total_files": 15,
  "total_folders": 3,
  "processed_keys": ["..."],
  "failed_keys": ["..."],
  "pattern": "user@example.com/documents/",
  "total_matched": 18
}
```

**Handler:** `HandleGoogleDriveRestoreByPath`

### 3. Restore Entire Folder

**Endpoint:** `POST /api/google-drive/restore/folder/:path`

**Example:** `POST /api/google-drive/restore/folder/user@example.com/documents/reports/`

**Response:**
```json
{
  "message": "Folder 'user@example.com/documents/reports/' restore completed",
  "total_files": 25,
  "total_folders": 5,
  "processed_keys": ["..."],
  "failed_keys": ["..."],
  "folder_path": "user@example.com/documents/reports/"
}
```

**Handler:** `HandleGoogleDriveRestoreFolder`

### 4. Restore from Trash

**Endpoint:** `POST /api/google-drive/restore/trash`

**Request Body:**
```json
{
  "keys": [
    "user@example.com/deleted_file.docx"
  ]
}
```

**Response:**
```json
{
  "message": "Trash restore completed",
  "total_files": 1,
  "total_folders": 0,
  "processed_keys": ["user@example.com/deleted_file.docx"],
  "failed_keys": []
}
```

**Handler:** `HandleGoogleDriveRestoreTrash`

## Backup Data Structure

Files backed up to Satellite should use this structure:

```json
{
  "metadata": {
    "key": "user@example.com/documents/report.docx",
    "type": "file",
    "name": "report.docx",
    "mime_type": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    "parents": ["root"],
    "drive_id": "",
    "location_type": "MY_DRIVE",
    "permissions": [
      {
        "type": "user",
        "role": "reader",
        "email_address": "viewer@example.com"
      }
    ],
    "modified_time": "2025-01-10T10:30:00Z",
    "starred": false
  },
  "content": "<base64 encoded file content>"
}
```

## Concurrency & Performance

- **Concurrent Processing:** Uses `errgroup` with limit of 10 concurrent operations
- **Folder Caching:** Prevents redundant API calls for folder lookups
- **Batch Operations:** Supports restoring multiple files in a single request
- **Error Isolation:** Individual file failures don't stop the entire restore process

## Error Handling

### Recoverable Errors
- **Shared Drive inaccessible:** Fallback to My Drive
- **File already exists:** Append "(Restored Copy)" to filename
- **Permission denied:** Log warning, continue with file creation

### Non-Recoverable Errors
- **Invalid backup metadata:** Skip file, add to failed list
- **Satellite download failure:** Skip file, add to failed list
- **Google API quota exceeded:** Return error with partial results

## Google Compliance

✅ **Compliant:**
- Only recreates files/folders from backup
- Ownership cannot be restored (Google limitation)
- Shared With Me → copy only
- Shared Drive → conditional on membership
- Trash → restore using `Trashed=false`
- Metadata → only allowed fields

❌ **Not Claimed:**
- Recovery of permanently deleted files from other users
- Restoration of original file IDs
- Restoration of revision history
- Changing file ownership

## Required OAuth Scopes

Minimum scope:
```
https://www.googleapis.com/auth/drive
```

For read-only backup (if needed):
```
https://www.googleapis.com/auth/drive.readonly
```

## Testing

### Unit Tests (Recommended)

```go
// Test folder hierarchy rebuild
func TestRebuildFolderHierarchy(t *testing.T) { ... }

// Test file existence check
func TestCheckFileExists(t *testing.T) { ... }

// Test trash restoration
func TestRestoreFromTrash(t *testing.T) { ... }

// Test Shared Drive fallback
func TestSharedDriveFallback(t *testing.T) { ... }
```

### Integration Tests

1. **Backup → Restore → Verify:**
   - Backup files to Satellite
   - Delete from Drive
   - Restore from Satellite
   - Verify file content and metadata

2. **Trash Restoration:**
   - Backup file
   - Move to trash in Drive
   - Restore from backup
   - Verify file is untrashed

3. **Shared Drive:**
   - Backup from Shared Drive
   - Remove user from drive
   - Restore (should fallback to My Drive)

## Usage Example

### From Handler

```go
// In your router setup
e.POST("/api/google-drive/restore", handler.HandleGoogleDriveDownloadAndRestore)
e.GET("/api/google-drive/restore/path", handler.HandleGoogleDriveRestoreByPath)
e.POST("/api/google-drive/restore/folder/:path", handler.HandleGoogleDriveRestoreFolder)
e.POST("/api/google-drive/restore/trash", handler.HandleGoogleDriveRestoreTrash)
```

### From Code

```go
// Create Drive service
srv, err := google.GetDriveService(c)
if err != nil {
    return err
}

// Create Drive client
driveClient := google.NewDriveClient(srv)

// Restore a single file
metadata := &google.DriveFileMetadata{
    Key:          "documents/report.docx",
    Type:         "file",
    Name:         "report.docx",
    MimeType:     "application/pdf",
    LocationType: "MY_DRIVE",
}

err = driveClient.RestoreFromBackup(ctx, "user@example.com", metadataJSON, fileBytes)
```

## Monitoring & Logging

All restore operations include structured logging:

```go
logger.Info(ctx, "Created file",
    logger.String("file_name", fileName),
    logger.String("file_id", createdFile.Id),
    logger.String("parent_id", parentID))

logger.Warn(ctx, "Failed to apply permission",
    logger.String("file_id", fileID),
    logger.String("email", perm.EmailAddress),
    logger.ErrorField(err))
```

## Future Enhancements

1. **Incremental Restore:** Restore only files modified after a certain date
2. **Conflict Resolution:** User-selectable strategies (skip, overwrite, rename)
3. **Progress Tracking:** WebSocket-based real-time progress updates
4. **Restore Preview:** Show what will be restored before executing
5. **Selective Metadata:** Allow users to choose which metadata to restore
6. **Batch Size Configuration:** Make concurrent limit configurable
7. **Retry Logic:** Automatic retry for transient failures

## Troubleshooting

### Common Issues

**Issue:** "Failed to create folder: already exists"
- **Solution:** Folder cache may be stale. Clear cache or restart service.

**Issue:** "Permission denied"
- **Solution:** Check OAuth scopes. Ensure `drive` scope is granted.

**Issue:** "Shared Drive not found"
- **Solution:** User may have lost access. Restore will fallback to My Drive automatically.

**Issue:** "File already exists"
- **Solution:** File will be created with "(Restored Copy)" suffix.

## Summary

This implementation provides a **production-grade, Google-compliant** restore solution for Google Drive that:

✅ Handles all location types (My Drive, Shared Drives, Shared With Me)  
✅ Preserves folder hierarchy  
✅ Restores from trash  
✅ Handles permanently deleted files  
✅ Applies metadata and permissions where allowed  
✅ Provides comprehensive error handling and logging  
✅ Follows the same pattern as Gmail and Outlook restore  
✅ Supports concurrent processing for performance  
✅ Includes multiple restore strategies (by keys, by path, by folder, from trash)

The implementation is ready for integration into your backup/restore workflow.

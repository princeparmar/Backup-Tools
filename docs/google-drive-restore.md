# Google Drive Restore Implementation - Key Path Only Approach

## Overview
This document describes the production-ready Google Drive restore implementation that uses **only key paths** (no original file IDs) to restore files and folders from Satellite backup to Google Drive.

## Architecture

### Key Components

1. **Handler Layer** (`handler/google_drive_handlers.go`)
   - `HandleGoogleDriveDownloadAndRestore`: Main restore endpoint handler
   - Supports both JSON and form-data with base64-encoded keys
   - Uses concurrent processing (10 goroutines) for performance

2. **Service Layer** (`apps/google/google-drive.go`)
   - `RestoreFromBackup`: Core restore logic
   - `RestoreContext`: Manages restore state and folder caching
   - `DriveFileMetadata`: Metadata structure for backup items

## How It Works

### 1. Key Path Structure

Backup keys follow this format:
```
email/folder1/folder2/filename.ext
```

Example:
```
user@example.com/documents/reports/2025/file1.docx
```

### 2. Restore Flow

```
┌─────────────────────────────────────────────────────────────┐
│ 1. Client sends base64-encoded keys (form-data or JSON)    │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 2. Handler decodes keys and validates (max 10 keys)        │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 3. Download backup data from Satellite (concurrent)        │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 4. Parse DriveBackupItem (metadata + content)              │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 5. Rebuild folder hierarchy from key path                  │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 6. Restore file/folder to Google Drive                     │
└─────────────────────────────────────────────────────────────┘
```

### 3. Folder Hierarchy Reconstruction

The `RebuildFolderHierarchy` function:
1. Splits key path into segments: `documents / reports / 2025 / file1.docx`
2. Iterates through each folder segment
3. Checks if folder exists (using cache for performance)
4. Creates folder if it doesn't exist
5. Returns the final parent folder ID

**Example:**
```go
Key: "documents/reports/2025/file1.docx"

Step 1: Check/Create "documents" → ID: abc123
Step 2: Check/Create "reports" under abc123 → ID: def456
Step 3: Check/Create "2025" under def456 → ID: ghi789
Step 4: Restore file1.docx under ghi789
```

### 4. Location Type Handling

The restore logic handles three location types:

#### **MY_DRIVE**
- Default restore location
- Files restored to user's My Drive
- Full hierarchy preserved

#### **SHARED_DRIVE**
- Validates user still has access to the Shared Drive
- If access exists: restores to original Shared Drive
- If access lost: **fallback to My Drive** with same folder structure

#### **SHARED_WITH_ME**
- Cannot restore original ownership (Google limitation)
- Always copies to My Drive
- Preserves folder structure under "Restored From Shared/"

### 5. Conflict Handling

When restoring a file that already exists:

1. **File in Trash**: Restore from trash and update metadata
2. **File exists (not trashed)**: Create copy with name `filename (Restored Copy).ext`
3. **File permanently deleted**: Create new file

## API Specification

### Endpoint
```
POST /api/google-drive/restore
```

### Headers
```
ACCESS_TOKEN: <satellite_access_grant>
Content-Type: application/json OR application/x-www-form-urlencoded
```

### Request Body

**Option 1: JSON (Recommended)**
```json
{
  "ids": [
    "dXNlckBleGFtcGxlLmNvbS9kb2N1bWVudHMvZmlsZTEuZG9jeA==",
    "dXNlckBleGFtcGxlLmNvbS9yZXBvcnRzL2ZpbGUyLnBkZg=="
  ]
}
```

**Option 2: Form Data**
```
ids=dXNlckBleGFtcGxlLmNvbS9kb2N1bWVudHMvZmlsZTEuZG9jeA==,dXNlckBleGFtcGxlLmNvbS9yZXBvcnRzL2ZpbGUyLnBkZg==
```

**Note:** Keys must be base64-encoded. Original key format:
```
user@example.com/documents/file1.docx
```

### Response

**Success (200 OK)**
```json
{
  "message": "Google Drive restore completed",
  "processed_keys": [
    "user@example.com/documents/file1.docx",
    "user@example.com/reports/file2.pdf"
  ],
  "failed_keys": []
}
```

**Error (400/403/500)**
```json
{
  "error": "error description",
  "processed_keys": ["..."],
  "failed_keys": ["..."]
}
```

## Validation & Limits

- **Maximum keys per request**: 10
- **Concurrent processing**: 10 goroutines
- **Key format**: Must be base64-encoded
- **Empty keys**: Automatically filtered out

## Metadata Structure

### DriveFileMetadata
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

### DriveBackupItem
```go
type DriveBackupItem struct {
    Metadata DriveFileMetadata // File/folder metadata
    Content  []byte            // File content (empty for folders)
}
```

## Performance Optimizations

1. **Folder Caching**: Prevents redundant API calls for folder existence checks
2. **Concurrent Processing**: 10 parallel restore operations
3. **Batch Validation**: Early validation before processing
4. **Error Isolation**: Failed restores don't block successful ones

## Error Handling

### Graceful Degradation
- Individual file failures don't stop the entire restore process
- Failed keys are collected and returned in the response
- Detailed logging for debugging

### Common Errors
1. **Download Failed**: Satellite object not found or access denied
2. **Parse Failed**: Invalid backup metadata format
3. **Restore Failed**: Google Drive API error or quota exceeded
4. **Access Lost**: Shared Drive no longer accessible (auto-fallback to My Drive)

## Security & Compliance

### Google-Compliant Approach
- ✅ No storage of original file IDs
- ✅ Recreates files instead of "reviving" deleted ones
- ✅ Respects current user permissions
- ✅ Cannot restore ownership (Google limitation)

### Privacy
- User email used for path construction
- Access tokens validated on every request
- No persistent storage of credentials

## Comparison with Google Photos Restore

| Feature | Google Photos | Google Drive |
|---------|--------------|--------------|
| Key Format | `email/albumID_albumTitle/photoID_filename` | `email/folder1/folder2/filename` |
| Hierarchy | Album-based | Folder-based (nested) |
| Metadata | Album info, creation time | MIME type, permissions, location type |
| Conflict Handling | Duplicate upload | Rename or trash restore |
| Fallback | Create album if missing | Fallback to My Drive |

## Example Usage

### JavaScript (Frontend)
```javascript
// Encode keys to base64
const keys = [
  "user@example.com/documents/file1.docx",
  "user@example.com/reports/file2.pdf"
];

const encodedKeys = keys.map(key => btoa(key));

// Send request
const response = await fetch('/api/google-drive/restore', {
  method: 'POST',
  headers: {
    'ACCESS_TOKEN': accessToken,
    'Content-Type': 'application/json'
  },
  body: JSON.stringify({ ids: encodedKeys })
});

const result = await response.json();
console.log('Restored:', result.processed_keys);
console.log('Failed:', result.failed_keys);
```

### cURL (Testing)
```bash
# Encode key
KEY="user@example.com/documents/file1.docx"
ENCODED=$(echo -n "$KEY" | base64)

# Send request
curl -X POST http://localhost:8080/api/google-drive/restore \
  -H "ACCESS_TOKEN: your_access_token" \
  -H "Content-Type: application/json" \
  -d "{\"ids\": [\"$ENCODED\"]}"
```

## Future Enhancements

1. **Batch Size Configuration**: Allow clients to specify batch size
2. **Progress Tracking**: WebSocket-based progress updates
3. **Selective Metadata Restore**: Choose which metadata fields to restore
4. **Dry Run Mode**: Preview restore without executing
5. **Restore History**: Track restore operations in database

## Troubleshooting

### Issue: "invalid base64 format"
**Solution**: Ensure keys are properly base64-encoded before sending

### Issue: "maximum 10 keys allowed"
**Solution**: Split large restore operations into multiple requests

### Issue: "failed to rebuild folder hierarchy"
**Solution**: Check if folder names contain invalid characters or exceed Google Drive limits

### Issue: "access token not found"
**Solution**: Ensure ACCESS_TOKEN header is set correctly

---

**Last Updated**: 2026-01-10  
**Version**: 1.0.0  
**Status**: Production Ready ✅

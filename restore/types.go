package restore

import (
	"sync"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"golang.org/x/time/rate"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/people/v1"
)

// APIService is the UI-facing service name in POST /restore/all.
type APIService string

const (
	APIServiceGmail     APIService = "gmail"
	APIServiceDrive     APIService = "drive"
	APIServicePhotos    APIService = "photos"
	APIServiceCalendar  APIService = "calendar"
	APIServiceContacts  APIService = "contacts"
)

// ServiceConfig holds per-method batch and concurrency limits.
type ServiceConfig struct {
	Method           string
	Bucket           string
	Source           string
	ObjectType       string
	BatchSize        int
	MaxConcurrency   int
	VaultConcurrency int
	RateLimitPerSec  float64
}

var serviceConfigs = map[string]ServiceConfig{
	"gmail": {
		Method: "gmail", Bucket: satellite.ReserveBucket_Gmail,
		Source: "google", ObjectType: "gmail",
		BatchSize: 50, MaxConcurrency: 10, VaultConcurrency: 10, RateLimitPerSec: 20,
	},
	"google_drive": {
		Method: "google_drive", Bucket: satellite.ReserveBucket_Drive,
		Source: "google", ObjectType: "drive",
		BatchSize: 25, MaxConcurrency: 10, VaultConcurrency: 5, RateLimitPerSec: 20,
	},
	"google_photos": {
		Method: "google_photos", Bucket: satellite.ReserveBucket_Photos,
		Source: "google", ObjectType: "photos",
		BatchSize: 10, MaxConcurrency: 3, VaultConcurrency: 3, RateLimitPerSec: 5,
	},
	"google_calendar": {
		Method: "google_calendar", Bucket: satellite.ReserveBucket_Calendar,
		Source: "google", ObjectType: "calendar",
		BatchSize: 100, MaxConcurrency: 20, VaultConcurrency: 20, RateLimitPerSec: 40,
	},
	"google_contacts": {
		Method: "google_contacts", Bucket: satellite.ReserveBucket_Contacts,
		Source: "google", ObjectType: "contacts",
		BatchSize: 100, MaxConcurrency: 20, VaultConcurrency: 20, RateLimitPerSec: 40,
	},
}

// APIServiceToMethod maps API body service to internal method key.
var APIServiceToMethod = map[APIService]string{
	APIServiceGmail:    "gmail",
	APIServiceDrive:    "google_drive",
	APIServicePhotos:   "google_photos",
	APIServiceCalendar: "google_calendar",
	APIServiceContacts: "google_contacts",
}

func ConfigForMethod(method string) (ServiceConfig, bool) {
	cfg, ok := serviceConfigs[method]
	return cfg, ok
}

// RestoreDeps is per-task runtime state (clients created in Processor.Setup).
type RestoreDeps struct {
	Store         *db.PostgresDb
	Job           *repo.RestoreJobListingDB
	CronJob       *repo.CronJobListingDB
	StorxRecovery *storxrefresh.Recovery
	AccessGrant   string
	GoogleToken        string
	RefreshToken       string
	AuthMode           string
	LoginID            string
	GoogleWriteSubject string // DWD impersonation target (migration cross-mailbox); defaults to LoginID
	Config             ServiceConfig

	GmailClient     *google.GmailClient
	DriveService    *drive.Service
	CalendarService *calendar.Service
	PeopleService   *people.Service
	PhotosClient    *google.GPotosClient

	PhotosAlbumCache map[string]*albums.Album
	photosAlbumMu    sync.Mutex

	googleLimiter *rate.Limiter
	vaultSem      chan struct{}
}

// BatchResult summarizes one batch execution.
type BatchResult struct {
	Processed    uint
	Failed       uint
	LastObjectID uint
	FailedKeys   []FailedKey
}

type FailedKey struct {
	ObjectKey string
	Reason    string
	ErrorCode string
}

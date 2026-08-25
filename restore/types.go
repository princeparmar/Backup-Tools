package restore

import (
	"strings"
	"sync"
	"time"

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

	// Microsoft UI services (distinct from Google calendar/contacts).
	APIServiceOutlook           APIService = "outlook"
	APIServiceMail              APIService = "mail"
	APIServiceOutlookCalendar   APIService = "outlook_calendar"
	APIServiceOutlookContacts   APIService = "outlook_contacts"
	APIServiceOutlookOneDrive   APIService = "outlook_onedrive"
	APIServiceOneDrive          APIService = "onedrive"
	APIServiceOutlookSharePoint APIService = "outlook_sharepoint"
	APIServiceSharePoint        APIService = "sharepoint"
	APIServiceOutlookTeams      APIService = "outlook_teams"
	APIServiceTeams             APIService = "teams"
	APIServiceOutlookGroups     APIService = "outlook_groups"
	APIServiceGroups            APIService = "groups"
)

const (
	RestoreProviderGoogle     = "google"
	RestoreProviderMicrosoft  = "microsoft"
)

// ServiceConfig holds per-method batch and concurrency limits.
type ServiceConfig struct {
	Method           string
	Bucket           string
	Source           string
	ObjectType       string
	Provider         string // google | microsoft
	BatchSize        int
	MaxConcurrency   int
	VaultConcurrency int
	RateLimitPerSec  float64
}

var serviceConfigs = map[string]ServiceConfig{
	"gmail": {
		Method: "gmail", Bucket: satellite.ReserveBucket_Gmail,
		Source: "google", ObjectType: "gmail", Provider: RestoreProviderGoogle,
		BatchSize: 50, MaxConcurrency: 10, VaultConcurrency: 10, RateLimitPerSec: 20,
	},
	"google_drive": {
		Method: "google_drive", Bucket: satellite.ReserveBucket_Drive,
		Source: "google", ObjectType: "drive", Provider: RestoreProviderGoogle,
		BatchSize: 25, MaxConcurrency: 10, VaultConcurrency: 5, RateLimitPerSec: 20,
	},
	"google_photos": {
		Method: "google_photos", Bucket: satellite.ReserveBucket_Photos,
		Source: "google", ObjectType: "photos", Provider: RestoreProviderGoogle,
		BatchSize: 10, MaxConcurrency: 3, VaultConcurrency: 3, RateLimitPerSec: 5,
	},
	"google_calendar": {
		Method: "google_calendar", Bucket: satellite.ReserveBucket_Calendar,
		Source: "google", ObjectType: "calendar", Provider: RestoreProviderGoogle,
		BatchSize: 100, MaxConcurrency: 20, VaultConcurrency: 20, RateLimitPerSec: 40,
	},
	"google_contacts": {
		Method: "google_contacts", Bucket: satellite.ReserveBucket_Contacts,
		Source: "google", ObjectType: "contacts", Provider: RestoreProviderGoogle,
		BatchSize: 100, MaxConcurrency: 20, VaultConcurrency: 20, RateLimitPerSec: 40,
	},
	"outlook": {
		Method: "outlook", Bucket: satellite.ReserveBucket_Outlook,
		Source: "outlook", ObjectType: "outlook", Provider: RestoreProviderMicrosoft,
		BatchSize: 50, MaxConcurrency: 10, VaultConcurrency: 10, RateLimitPerSec: 20,
	},
	"outlook_calendar": {
		Method: "outlook_calendar", Bucket: satellite.ReserveBucket_OutlookCalendar,
		Source: "outlook", ObjectType: "outlook_calendar", Provider: RestoreProviderMicrosoft,
		BatchSize: 100, MaxConcurrency: 20, VaultConcurrency: 20, RateLimitPerSec: 40,
	},
	"outlook_contacts": {
		Method: "outlook_contacts", Bucket: satellite.ReserveBucket_OutlookContacts,
		Source: "outlook", ObjectType: "outlook_contacts", Provider: RestoreProviderMicrosoft,
		BatchSize: 100, MaxConcurrency: 20, VaultConcurrency: 20, RateLimitPerSec: 40,
	},
	"outlook_onedrive": {
		Method: "outlook_onedrive", Bucket: satellite.ReserveBucket_OutlookOneDrive,
		Source: "outlook", ObjectType: "outlook_onedrive", Provider: RestoreProviderMicrosoft,
		BatchSize: 25, MaxConcurrency: 10, VaultConcurrency: 5, RateLimitPerSec: 20,
	},
	"outlook_sharepoint": {
		Method: "outlook_sharepoint", Bucket: satellite.ReserveBucket_OutlookSharePoint,
		Source: "outlook", ObjectType: "outlook_sharepoint", Provider: RestoreProviderMicrosoft,
		BatchSize: 25, MaxConcurrency: 10, VaultConcurrency: 5, RateLimitPerSec: 20,
	},
	"outlook_teams": {
		Method: "outlook_teams", Bucket: satellite.ReserveBucket_OutlookTeams,
		Source: "outlook", ObjectType: "outlook_teams", Provider: RestoreProviderMicrosoft,
		BatchSize: 50, MaxConcurrency: 10, VaultConcurrency: 10, RateLimitPerSec: 20,
	},
	"outlook_groups": {
		Method: "outlook_groups", Bucket: satellite.ReserveBucket_OutlookGroups,
		Source: "outlook", ObjectType: "outlook_groups", Provider: RestoreProviderMicrosoft,
		BatchSize: 50, MaxConcurrency: 10, VaultConcurrency: 10, RateLimitPerSec: 20,
	},
}

// APIServiceToMethod maps API body service to internal method key.
var APIServiceToMethod = map[APIService]string{
	APIServiceGmail:             "gmail",
	APIServiceDrive:             "google_drive",
	APIServicePhotos:            "google_photos",
	APIServiceCalendar:          "google_calendar",
	APIServiceContacts:          "google_contacts",
	APIServiceOutlook:           "outlook",
	APIServiceMail:              "outlook",
	APIServiceOutlookCalendar:   "outlook_calendar",
	APIServiceOutlookContacts:   "outlook_contacts",
	APIServiceOutlookOneDrive:   "outlook_onedrive",
	APIServiceOneDrive:          "outlook_onedrive",
	APIServiceOutlookSharePoint: "outlook_sharepoint",
	APIServiceSharePoint:        "outlook_sharepoint",
	APIServiceOutlookTeams:      "outlook_teams",
	APIServiceTeams:             "outlook_teams",
	APIServiceOutlookGroups:     "outlook_groups",
	APIServiceGroups:            "outlook_groups",
}

// IsMicrosoftRestoreMethod reports whether method is a Microsoft restore-all method.
func IsMicrosoftRestoreMethod(method string) bool {
	cfg, ok := ConfigForMethod(method)
	return ok && cfg.Provider == RestoreProviderMicrosoft
}

// IsMicrosoftAPIService reports whether the UI service name maps to a Microsoft method.
func IsMicrosoftAPIService(service string) bool {
	method, ok := APIServiceToMethod[APIService(strings.TrimSpace(strings.ToLower(service)))]
	return ok && IsMicrosoftRestoreMethod(method)
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
	MicrosoftToken     string
	RefreshToken       string
	AuthMode           string
	LoginID            string
	GoogleWriteSubject string // DWD impersonation target (migration cross-mailbox); defaults to LoginID
	Config             ServiceConfig
	// WriteCred is the credential used to mint provider tokens (Google or Microsoft).
	WriteCred *repo.GoogleBackupCredentialDB

	GmailClient     *google.GmailClient
	DriveService    *drive.Service
	CalendarService *calendar.Service
	PeopleService   *people.Service
	PhotosClient    *google.GPotosClient

	PhotosAlbumCache map[string]*albums.Album
	PhotosAlbumMu    sync.Mutex

	googleLimiter *rate.Limiter
	vaultSem      chan struct{}

	heartbeatMu   sync.Mutex
	lastHeartbeat time.Time
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

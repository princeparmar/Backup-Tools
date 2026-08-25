package router

import (
	"context"
	"net/http"

	googlepack "github.com/StorX2-0/Backup-Tools/apps/google"
	outlookpack "github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"

	middleware "github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/labstack/echo/v4"
)

func StartServer(db *db.PostgresDb, address string) {
	e := echo.New()
	e.HideBanner = true

	// Initialize all middleware
	middleware.InitializeAllMiddleware(e, db)

	// Prometheus metrics endpoints
	e.GET("/metrics", echo.WrapHandler(monitor.CreateMetricsHandler()))

	// Swagger documentation endpoints
	e.GET("/swagger", handler.SwaggerUIHandler)
	e.GET("/swagger.yaml", handler.SwaggerYAMLHandler)

	e.GET("/health", func(c echo.Context) error {
		return c.String(http.StatusOK, "OK")
	})

	webhookPrivateKeyPath := utils.GetEnvWithKey("WEBHOOK_PRIVATE_KEY")
	if webhookPrivateKeyPath != "" {
		decryptor, err := handler.NewWebhookDecryptor(webhookPrivateKeyPath)
		if err != nil {
			logger.Info(context.Background(), "Failed to initialize webhook decryptor, webhook endpoint will be disabled", logger.ErrorField(err))
		} else {
			e.POST("/webhook", func(c echo.Context) error {
				c.Set("webhook_decryptor", decryptor)
				return handler.HandleWebhook(c)
			})
			logger.Info(context.Background(), "Webhook endpoint initialized at /webhook")
		}
	} else {
		logger.Info(context.Background(), "WEBHOOK_PRIVATE_KEY not set, webhook endpoint will be disabled")
	}

	e.POST("/satellite-auth", satellite.HandleSatelliteAuthentication)
	e.POST("/google-auth", googlepack.Autentificate)
	e.GET("/google-auth", googlepack.Autentificateg)
	// Microsoft restore auth for all Graph services (mirrors /google-auth): restore scopes → exchange Graph token → JWT for satellite-to-*.
	e.POST("/microsoft-auth", outlookpack.Authenticate)
	// e.POST("/auth/google/connect", handler.HandleGoogleConnect)
	e.GET("/google/gmail/corporate/domain-users", handler.HandleGmailCorporateDomainUsers)
	// Microsoft OAuth login moved to Satellite.
	// e.GET("/auth/microsoft/start", handler.HandleMicrosoftAuthRedirect)
	e.GET("/autobackup/summary", handler.HandleAutomaticBackupSummary)
	e.GET("/autosync/stats", handler.HandleAutomaticSyncStats)
	e.GET("/autosync/dashboard-alerts", handler.HandleAutosyncDashboardAlerts)

	backupRestore := e.Group("/backup-restore")
	backupRestore.GET("/logs", handler.HandleBackupRestoreLogs)

	usersGroups := e.Group("/users-groups")
	usersGroups.GET("/domains", handler.HandleAutosyncUsersGroupsDomains)
	usersGroups.PUT("/jobs/active", handler.HandleUsersGroupsJobsActive)
	usersGroups.GET("", handler.HandleAutosyncUsersGroupsList)
	usersGroups.GET("/mailbox/overview", handler.HandleAutosyncUsersGroupsMailboxOverview)
	usersGroups.GET("/mailbox/services", handler.HandleAutosyncUsersGroupsMailboxServices)
	usersGroups.GET("/mailbox/schedule", handler.HandleAutosyncUsersGroupsMailboxSchedule)
	usersGroups.GET("/mailbox/credentials", handler.HandleAutosyncUsersGroupsMailboxCredentials)

	autoSync := e.Group("/auto-sync") // Shared Google + Microsoft: list, live, job CRUD, policy, backup-now, tasks
	autoSync.GET("/live", handler.HandleAutomaticSyncActiveJobsForUser)
	autoSync.PUT("/task/hide", handler.HandleHideTask)

	job := autoSync.Group("/job")
	job.GET("/services", handler.HandleAutomaticSyncServicesForUser)
	job.GET("/", handler.HandleAutomaticSyncListForUser)
	job.POST("", handler.HandleAutomaticSyncCreate)
	job.GET("/interval", handler.HandleIntervalOnConfig)
	job.PUT("/project", handler.HandleAutomaticBackupUpdateByProject)
	job.GET("/:job_id", handler.HandleAutomaticSyncDetails)
	job.PUT("/:job_id", handler.HandleAutomaticBackupUpdate)
	// job.PUT("/:method/bulk-update", handler.HandleAutomaticBackupBulkUpdateByParent)
	job.DELETE("/:job_id", handler.HandleAutomaticSyncDelete)

	policy := autoSync.Group("/policy")
	policy.GET("", handler.HandleAutosyncPolicyList)
	policy.GET("/options", handler.HandleAutosyncPolicyOptions)
	policy.GET("/available-assignments", handler.HandleAutosyncPolicyAvailableAssignments)
	policy.POST("", handler.HandleAutosyncPolicyCreate)
	policy.POST("/move", handler.HandleAutosyncPolicyMove)
	policy.GET("/merge/preview", handler.HandleAutosyncPolicyMergePreview)
	policy.POST("/merge", handler.HandleAutosyncPolicyMerge)
	policy.GET("/:policy_id", handler.HandleAutosyncPolicyByID)
	policy.PUT("/:policy_id", handler.HandleAutosyncPolicyUpdate)
	policy.DELETE("/:policy_id", handler.HandleAutosyncPolicyDelete)

	task := autoSync.Group("/task")
	task.POST("/:job_id/backup-now", handler.HandleAutomaticSyncBackupNow)
	task.POST("/:job_id", handler.HandleAutomaticSyncCreateTask)
	task.GET("/:job_id", handler.HandleAutomaticSyncTaskList)

	// Admin endpoint for deleting jobs by email
	autoSync.DELETE("/delete-jobs-by-email", handler.HandleDeleteJobsByEmail)

	restoreGroup := e.Group("/restore")
	restoreGroup.GET("/workspaces", handler.HandleRestoreWorkspaces)
	restoreGroup.GET("/credentials", handler.HandleRestoreCredentials)
	restoreGroup.GET("/prepare", handler.HandleRestorePrepare)
	restoreGroup.GET("/live", handler.HandleRestoreLive)
	restoreGroup.POST("/all", handler.HandleRestoreAll)
	restoreGroup.GET("/jobs", handler.HandleListRestoreJobs)
	restoreGroup.GET("/job/:job_id", handler.HandleGetRestoreJob)
	restoreGroup.POST("/job/:job_id/cancel", handler.HandleCancelRestoreJob)
	restoreGroup.GET("/job/:job_id/dead-items", handler.HandleListRestoreDeadItems)

	// Microsoft product surface (outlook / outlook_*). Job list/live/update/policy/backup-now use shared /auto-sync/* above.
	microsoft := e.Group("/microsoft")
	microsoft.GET("/outlook/corporate/domain-users", handler.HandleMicrosoftCorporateDomainUsers)
	microsoft.GET("/account/detect", handler.HandleMicrosoftCorporateDomainUsers)
	microsoft.GET("/directory/users", handler.HandleMicrosoftDirectoryUsers)

	msUsersGroups := microsoft.Group("/users-groups")
	msUsersGroups.GET("/domains", handler.HandleMicrosoftAutosyncUsersGroupsDomains)
	msUsersGroups.PUT("/jobs/active", handler.HandleMicrosoftUsersGroupsJobsActive)
	msUsersGroups.GET("", handler.HandleMicrosoftAutosyncUsersGroupsList)
	msUsersGroups.GET("/mailbox/overview", handler.HandleMicrosoftAutosyncUsersGroupsMailboxOverview)
	msUsersGroups.GET("/mailbox/services", handler.HandleMicrosoftAutosyncUsersGroupsMailboxServices)
	msUsersGroups.GET("/mailbox/schedule", handler.HandleMicrosoftAutosyncUsersGroupsMailboxSchedule)
	msUsersGroups.GET("/mailbox/credentials", handler.HandleMicrosoftAutosyncUsersGroupsMailboxCredentials)

	msBackup := microsoft.Group("/backup")
	msBackup.POST("/onboarding/jobs", handler.HandleMicrosoftBackupOnboardingJobs)

	msAutoSync := microsoft.Group("/auto-sync")
	msAutoSync.POST("/job", handler.HandleMicrosoftAutomaticSyncCreate)

	microsoft.GET("/query-messages", handler.HandleMicrosoftQueryMessages)
	microsoft.GET("/contacts/list", handler.HandleMicrosoftListContacts)
	microsoft.GET("/calendar/list", handler.HandleMicrosoftListCalendars)
	microsoft.GET("/calendar/events/:calendarId", handler.HandleMicrosoftListCalendarEvents)
	microsoft.GET("/onedrive/flat-files", handler.HandleMicrosoftOneDriveFlatFiles)
	microsoft.GET("/outlook/flat-files", handler.HandleMicrosoftOutlookFlatFiles)
	microsoft.GET("/sharepoint/sites", handler.HandleMicrosoftSharePointSites)
	microsoft.GET("/sharepoint/flat-files", handler.HandleMicrosoftSharePointFlatFiles)
	microsoft.GET("/teams/list", handler.HandleMicrosoftTeamsList)
	microsoft.GET("/teams/channels", handler.HandleMicrosoftTeamChannels)
	microsoft.GET("/teams/flat-messages", handler.HandleMicrosoftTeamsFlatMessages)
	microsoft.GET("/groups/list", handler.HandleMicrosoftGroupsList)
	microsoft.GET("/groups/flat-conversations", handler.HandleMicrosoftGroupsFlatConversations)

	google := e.Group("/google")

	google.Use(middleware.JWTMiddleware)

	// See the requests description in README file

	// Google Drive
	google.GET("/drive-to-satellite/:ID", handler.HandleSendFileFromGoogleDriveToSatellite)

	google.GET("/satellite-to-drive/:name", handler.HandleSendFileFromSatelliteToGoogleDrive)
	// list all files in root and in root folders.
	google.GET("/drive-root-file-names", handler.HandleRootGoogleDriveFileNames)
	// List all files in root and not in root folder. Only files and folders in Root
	google.GET("/drive-get-file-names", handler.HandleGetGoogleDriveFileNames)
	// Flat list of non-folder files across all drives (pagination supported)
	google.GET("/drive-flat-files", handler.HandleFlatGoogleDriveFiles)
	google.GET("/drive-get-file/:ID", googlepack.GetFileByID)

	// list drive files in satellite
	google.GET("/satellite-drive", handler.HandleSatelliteDrive)

	//get list of files in satellite folder from drive
	google.GET("/satellite-drive-folder/:name", handler.HandleSatelliteDriveFolder)
	// All files and folders from drive to satellite
	// google.GET("/all-drive-to-satellite", handler.HandleSendAllFilesFromGoogleDriveToSatellite)
	// List files in a folder by name
	// google.GET("/folder/:name/list", handler.HandleListAllFolderFiles)
	// sync all files from drive folder to satellite using the folder name
	// google.POST("/folder/:name/sync", handler.HandleSyncAllFolderFiles)
	// list files in folder by folder ID
	google.GET("/folder/:id", handler.HandleListAllFolderFilesByID)
	// sync files in folder by folder ID
	// google.POST("/folder/:id", handler.HandleSyncAllFolderFilesByID)
	// Get all shared files
	google.GET("/get-shared-filenames", handler.HandleSharedGoogleDriveFileNames)
	// Sync all shared files
	// google.POST("/sync-shared", handler.HandleSyncAllSharedFolderAndFiles)
	// Send a list of items from google drive to satellite
	// google.POST("/sync-list-from-drive", handler.HandleSendListFromGoogleDriveToSatellite)

	// Google Contacts
	google.GET("/contacts/list", handler.HandleListContacts)
	google.POST("/satellite-to-contacts", handler.HandleGoogleContactsRestore)

	// Google Calendar
	google.GET("/calendar/list", handler.HandleListCalendars)
	google.GET("/calendar/events/:calendarId", handler.HandleListCalendarEvents)
	google.POST("/satellite-to-calendar", handler.HandleGoogleCalendarRestore)

	// Google Photos
	google.GET("/photos-flat-media", handler.HandleFlatPhotosMedia)
	google.GET("/photos-list-albums", handler.HandleListGPhotosAlbums)
	google.GET("/photos-list-photos-in-album/:ID", handler.HandleListPhotosInAlbum)
	google.GET("/photos-list-all", handler.HandleListAllPhotos)
	google.GET("/satellite-to-photos/:name", handler.HandleSendFileFromSatelliteToGooglePhotos)
	google.POST("/satellite-to-photos", handler.HandleGooglePhotosRestore)
	google.POST("/photos-to-satellite", handler.HandleSendFileFromGooglePhotosToSatellite)
	google.POST("/all-photos-from-album-to-satellite", handler.HandleSendAllFilesFromGooglePhotosToSatellite)
	google.POST("/list-photos-to-satellite", handler.HandleSendListFilesFromGooglePhotosToSatellite)

	// In the existing google group routes section
	google.POST("/gmail/insert-mail", handler.HandleGmailDownloadAndInsert) // used by desktop app to sync emails to satellite.
	// google.POST("/gmail-list-to-satellite", handler.HandleListGmailMessagesToSatellite) // used by desktop app to sync emails to satellite.
	google.GET("/query-messages", handler.HandleGmailGetThreadsIDsControlled) // used by desktop app to show email list on backup tools UI.

	// Google Drive restore endpoint (similar to Gmail and Outlook)
	google.POST("/satellite-to-drive", handler.HandleGoogleDriveDownloadAndRestore)

	// Google Cloud Storage
	google.GET("/storage-list-buckets/:projectName", handler.HandleStorageListBuckets)
	google.GET("/storage-list-items/:bucketName", handler.HandleStorageListObjects)
	google.GET("/bucket-metadata/:bucketName", handler.HandleBucketMetadata)
	google.GET("/storage-item-to-satellite/:bucketName/:itemName", handler.HandleGoogleCloudItemToSatellite)
	google.GET("/storage-item-from-satellite-to-google-cloud/:bucketName/:itemName", handler.HandleSatelliteToGoogleCloud)
	google.POST("/storage-all-items-to-satellite/:bucketName", handler.HandleAllFilesFromGoogleCloudBucketToSatellite)
	google.POST("/list-projects-to-satellite", handler.HandleListProjects)
	google.POST("/list-buckets-to-satellite", handler.HandleListBuckets)
	google.POST("/list-items-to-satellite", handler.HandleSyncCloudItems)
	e.GET("/satellite/:bucketName", func(c echo.Context) error {
		bucketName := c.Param("bucketName")

		accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
		if accesGrant == "" {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "access token not found",
			})
		}
		list, err := satellite.ListObjectsRecursive(context.Background(), accesGrant, bucketName)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		return c.JSON(http.StatusOK, list)
	})

	google.GET("/cloud/list-projects", handler.HandleStorageListProjects)
	google.GET("/cloud/list-organizations", handler.HandleStorageListOrganizations)

	// Dropbox
	dropbox := e.Group("/dropbox")

	dropbox.GET("/file-to-satellite/:filePath", handler.HandleDropboxToSatellite)
	dropbox.GET("/file-from-satellite/:filePath", handler.HandleSatelliteToDropbox)

	// office 365
	office365 := e.Group("/office365")
	office365.GET("/get-outlook-messages", handler.HandleOutlookGetMessages)
	office365.GET("/get-outlook-messages/:id", handler.HandleOutlookGetMessageById)
	office365.POST("/outlook-messages-to-satellite", handler.HandleListOutlookMessagesToSatellite)
	office365.POST("/satellite-to-outlook", handler.HandleOutlookDownloadAndInsert)
	// Manual select-and-restore (≤10 vault keys), same pattern as /google/satellite-to-*.
	office365.POST("/satellite-to-outlook-calendar", handler.HandleOutlookCalendarDownloadAndInsert)
	office365.POST("/satellite-to-outlook-contacts", handler.HandleOutlookContactsDownloadAndInsert)
	office365.POST("/satellite-to-onedrive", handler.HandleOneDriveDownloadAndInsert)
	office365.POST("/satellite-to-sharepoint", handler.HandleSharePointDownloadAndInsert)
	office365.POST("/satellite-to-teams", handler.HandleTeamsDownloadAndInsert)
	office365.POST("/satellite-to-groups", handler.HandleGroupsDownloadAndInsert)
	// AWS S3
	aws := e.Group("/aws")
	aws.GET("/list-files-in-bucket/:bucketName", handler.HandleListAWSs3BucketFiles)
	aws.GET("/file-from-aws-to-satellite/:bucketName/:itemName", handler.HandleS3toSatellite)
	aws.GET("/file-from-satellite-to-aws/:bucketName/:itemName", handler.HandleSatelliteToS3)

	// Github
	github := e.Group("/github")
	github.GET("/login", handler.HandleGithubLogin)
	github.GET("/callback", handler.HandleGithubCallback)
	github.GET("/list-repos", handler.HandleListRepos)
	github.GET("/get-repo", handler.HandleGetRepository)
	github.GET("/repo-to-satellite", handler.HandleGithubRepositoryToSatellite)
	github.GET("/recover-repo-to-github", handler.HandleRepositoryFromSatelliteToGithub)

	// Shopify
	shopify := e.Group("/shopify")
	shopify.GET("/login", handler.HandleShopifyAuth)
	shopify.GET("/callback", handler.HandleShopifyAuthRedirect)
	shopify.GET("/products-to-satellite/:shopname", handler.HandleShopifyProductsToSatellite)
	shopify.GET("/customers-to-satellite/:shopname", handler.HandleShopifyCustomersToSatellite)
	shopify.GET("/orders-to-satellite/:shopname", handler.HandleShopifyOrdersToSatellite)

	// Shopifys
	quickbooks := e.Group("/quickbooks")
	// shopify.GET("/login", handleShopifyAuth)
	// shopify.GET("/callback", handleShopifyAuthRedirect)
	quickbooks.GET("/customers-to-satellite", handler.HandleQuickbooksCustomersToSatellite)
	quickbooks.GET("/items-to-satellite", handler.HandleQuickbooksItemsToSatellite)
	quickbooks.GET("/invoices-to-satellite", handler.HandleQuickbooksInvoicesToSatellite)

	// Scheduled tasks
	scheduledTasks := e.Group("/tasks")
	scheduledTasks.POST("/:method", handler.HandleCreateScheduledTask)
	scheduledTasks.GET("", handler.HandleGetScheduledTasksByUserID)
	scheduledTasks.GET("/live", handler.HandleGetRunningScheduledTasks)

	err := e.Start(address)
	if err != nil {
		logger.Info(context.Background(), "Error starting server", logger.ErrorField(err))
	}
}

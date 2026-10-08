package crons

import (
	"context"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type outlookContactsProcessor struct{}

func NewOutlookContactsProcessor() *outlookContactsProcessor {
	return &outlookContactsProcessor{}
}

func (p *outlookContactsProcessor) Run(input ProcessorInput) error {
	return runOutlookPIMJob(input, "outlook_contacts", satellite.ReserveBucket_OutlookContacts, google.ContactsMinEstimateBytes, syncOutlookContacts)
}

// syncOutlookContacts backs up the default Contacts folder and every contact folder below it.
func syncOutlookContacts(ctx context.Context, run *pimRun, userBase, keyPrefix string) error {
	collections := []pimCollection{{
		name: "Contacts", dir: outlook.PIMContactsDir(keyPrefix, ""), listURL: outlook.PIMContactsURL(userBase, ""),
	}}
	folders, err := pimContactFoldersFn(ctx, run.accessToken, userBase)
	if err != nil {
		logger.Warn(ctx, "outlook contact folders unavailable; backing up the default folder only", logger.ErrorField(err))
	}
	for _, f := range folders {
		dir := outlook.PIMContactsDir(keyPrefix, f.ID)
		collections = append(collections, pimCollection{
			name: f.DisplayName, dir: dir, listURL: outlook.PIMContactsURL(userBase, f.ID),
			metaKey: dir + outlook.PIMFolderMetaName, meta: f,
		})
	}
	return run.syncCollections(ctx, collections)
}

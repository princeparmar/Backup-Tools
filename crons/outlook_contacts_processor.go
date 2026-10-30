package crons

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// Graph seam (overridden in tests).
var pimContactFoldersFn = outlook.ListPIMContactFolders

type outlookContactsProcessor struct{}

func NewOutlookContactsProcessor() *outlookContactsProcessor {
	return &outlookContactsProcessor{}
}

func (p *outlookContactsProcessor) Run(input ProcessorInput) error {
	return runOutlookPIMJob(input, "outlook_contacts", satellite.ReserveBucket_OutlookContacts, outlook.ContactsMinEstimateBytes, syncOutlookContacts)
}

// syncOutlookContacts backs up the default Contacts folder and every contact folder below it via
// contacts/delta. Folder listing failures fail the job. Folders missing from a complete listing
// are marked removed. Task memory keeps per-folder delta links and the photo layout version.
func syncOutlookContacts(ctx context.Context, run *pimRun, userBase, keyPrefix string) error {
	folders, err := pimContactFoldersFn(ctx, run.accessToken, userBase)
	if err != nil {
		return err
	}

	savedDeltas, savedParents := map[string]string{}, map[string]string{}
	if run.tm != nil {
		savedDeltas, savedParents = run.tm.OutlookContactFolderDeltas, run.tm.OutlookContactFolderParents
		if run.tm.OutlookContactsLayout < outlook.OutlookContactsLayoutFull {
			// Force a full pass (photos + anniversary + complete Graph contact): clear delta cursors.
			savedDeltas = nil
		}
	}

	collections := []pimCollection{contactsCollection(userBase, keyPrefix, "", "Contacts", savedDeltas)}
	parents := make(map[string]string, len(folders))
	for _, f := range folders {
		parents[f.ID] = f.ParentFolderID
		deltas := savedDeltas
		if savedParents[f.ID] != f.ParentFolderID {
			deltas = nil
		}
		col := contactsCollection(userBase, keyPrefix, f.ID, f.DisplayName, deltas)
		col.metaKey = outlook.PIMContactsDir(keyPrefix, f.ID) + outlook.PIMFolderMetaName
		col.meta = f
		collections = append(collections, col)
	}

	if err := run.syncCollections(ctx, collections); err != nil {
		return err
	}
	if err := run.markRemovedContactFolders(ctx, keyPrefix, folders); err != nil {
		return err
	}
	if run.tm != nil {
		run.tm.OutlookContactFolderParents = parents
		run.tm.OutlookContactsLayout = outlook.OutlookContactsLayoutFull
	}
	return nil
}

func contactsCollection(userBase, keyPrefix, folderID, name string, savedDeltas map[string]string) pimCollection {
	dir := outlook.PIMContactsDir(keyPrefix, folderID)
	start := ""
	if savedDeltas != nil {
		start = strings.TrimSpace(savedDeltas[folderID])
	}
	return pimCollection{
		name: name, dir: dir, listURL: outlook.PIMContactsURL(userBase, folderID),
		userBase: userBase, contactFolderID: folderID,
		useContactsDelta: true, contactsDeltaStart: start,
		contactsDeltaBaseline: start == "",
	}
}

// markRemovedContactFolders sets removed_at on the _folder.json of every backed-up contact folder
// missing from folders (a complete listing) and marks its contacts removed in its index.
func (r *pimRun) markRemovedContactFolders(ctx context.Context, keyPrefix string, folders []outlook.PIMContactFolder) error {
	listed := make(map[string]bool, len(folders))
	for _, f := range folders {
		listed[outlook.PIMContactsDir(keyPrefix, f.ID)+outlook.PIMFolderMetaName] = true
	}
	folderMetaPrefix := strings.TrimRight(keyPrefix, "/") + "/folders/"
	var gone []string
	for key := range r.synced {
		if path.Base(key) != outlook.PIMFolderMetaName || !strings.HasPrefix(key, folderMetaPrefix) || listed[key] {
			continue
		}
		gone = append(gone, key)
	}
	sort.Strings(gone)
	for _, metaKey := range gone {
		if err := r.markContactFolderRemoved(ctx, metaKey); err != nil {
			return err
		}
	}
	return nil
}

func (r *pimRun) markContactFolderRemoved(ctx context.Context, metaKey string) error {
	raw, err := r.store.get(ctx, metaKey)
	var folder outlook.PIMContactFolder
	if err == nil {
		err = json.Unmarshal(raw, &folder)
	}
	if err != nil {
		logger.Warn(ctx, "removed contact folder not marked", logger.String("key", metaKey), logger.ErrorField(err))
		return nil
	}
	if folder.RemovedAt != "" {
		return nil
	}
	removedAt := r.now().UTC().Format(time.RFC3339)
	indexKey := path.Dir(metaKey) + "/" + outlook.PIMIndexName
	index, err := r.loadIndex(ctx, indexKey)
	if err == nil && len(index.Items) > 0 {
		for id, e := range index.Items {
			if e.RemovedAt == "" {
				e.RemovedAt = removedAt
				index.Items[id] = e
				r.removed++
			}
		}
		err = r.saveIndex(ctx, indexKey, index)
	}
	if err != nil {
		if asErrStorageQuota(err) != nil {
			return err
		}
		logger.Warn(ctx, "removed contact folder not marked", logger.String("key", metaKey), logger.ErrorField(err))
		return nil
	}
	folder.RemovedAt = removedAt
	logger.Info(ctx, "contact folder removed from Microsoft; backup kept", logger.String("key", metaKey))
	return r.putJSON(ctx, metaKey, folder)
}

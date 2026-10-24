package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/quota"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const mailTestPrefix = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid-1"

type memMailStore struct {
	objects   map[string][]byte
	uploads   []string
	downloads []string
	removed   []string
}

func newMemMailStore() *memMailStore { return &memMailStore{objects: map[string][]byte{}} }

func (s *memMailStore) download(_ context.Context, key string) ([]byte, error) {
	s.downloads = append(s.downloads, key)
	b, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return b, nil
}

func (s *memMailStore) upload(_ context.Context, key string, data []byte) error {
	s.uploads = append(s.uploads, key)
	s.objects[key] = data
	return nil
}

func (s *memMailStore) remove(_ context.Context, key string) {
	s.removed = append(s.removed, key)
	delete(s.objects, key)
}

var (
	mailInbox   = outlook.MailFolder{ID: "F-INBOX", Path: "Inbox", WellKnownName: "inbox"}
	mailProject = outlook.MailFolder{ID: "F-PROJ", Path: "Inbox/Projects"}
)

func stubMailFetch(t *testing.T) *int {
	t.Helper()
	calls := 0
	prev := outlookMailFetchFn
	outlookMailFetchFn = func(_ context.Context, _, _, id string) ([]byte, *outlook.GraphOutlookMailMessageDetail, error) {
		calls++
		raw := []byte(fmt.Sprintf(`{"id":%q,"subject":"Hello","receivedDateTime":"2026-07-21T10:00:00Z","conversationId":"C1","from":{"emailAddress":{"address":"ann@contoso.com"}},"isRead":false}`, id))
		var d outlook.GraphOutlookMailMessageDetail
		_ = json.Unmarshal(raw, &d)
		return raw, &d, nil
	}
	t.Cleanup(func() { outlookMailFetchFn = prev })
	return &calls
}

func mailDelta(id string) *outlook.OutlookMailDeltaMessage {
	return &outlook.OutlookMailDeltaMessage{ID: id, Subject: "Hello", From: "ann@contoso.com", ConversationID: "C1", ReceivedDateTime: "2026-07-21T10:00:00Z"}
}

func mailKey(folder, id string) string {
	return outlook.OutlookMailObjectKey(mailTestPrefix, folder, "2026-07-21T10:00:00Z", "ann@contoso.com", "Hello", "C1", id)
}

func newTestMailRun(store *memMailStore, index map[string]string) *outlookMailRun {
	return newOutlookMailRun(ProcessorInput{}, store, "tok", "https://graph/users/u", mailTestPrefix, index)
}

func TestOutlookMailSyncNewMessage(t *testing.T) {
	fetches := stubMailFetch(t)
	store := newMemMailStore()
	run := newTestMailRun(store, nil)

	if err := run.syncMessage(context.Background(), mailInbox, mailDelta("M1"), true); err != nil {
		t.Fatal(err)
	}
	key := mailKey("Inbox", "M1")
	if *fetches != 1 || store.objects[key] == nil || run.index["M1"] != key {
		t.Fatalf("fetches=%d uploads=%v", *fetches, store.uploads)
	}
	state := outlook.ReadOutlookMailBackupState(store.objects[key])
	if state.FolderPath != "Inbox" || state.WellKnownFolder != "inbox" || state.BackedUpAt == "" {
		t.Fatalf("state %+v", state)
	}
}

func TestOutlookMailSyncBaselineSkipsUnchanged(t *testing.T) {
	fetches := stubMailFetch(t)
	store := newMemMailStore()
	key := mailKey("Inbox", "M1")
	store.objects[key] = []byte(`{"id":"M1"}`)
	run := newTestMailRun(store, map[string]string{"M1": key})

	if err := run.syncMessage(context.Background(), mailInbox, mailDelta("M1"), true); err != nil {
		t.Fatal(err)
	}
	if *fetches != 0 || len(store.downloads) != 0 || len(store.uploads) != 0 {
		t.Fatalf("fetches=%d downloads=%v uploads=%v", *fetches, store.downloads, store.uploads)
	}
}

func TestOutlookMailSyncUpdatesStateInPlace(t *testing.T) {
	fetches := stubMailFetch(t)
	store := newMemMailStore()
	key := mailKey("Inbox", "M1")
	store.objects[key] = []byte(`{"id":"M1","isRead":false}`)
	run := newTestMailRun(store, map[string]string{"M1": key})

	msg := mailDelta("M1")
	msg.IsRead = true
	if err := run.syncMessage(context.Background(), mailInbox, msg, false); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		IsRead bool `json:"isRead"`
	}
	_ = json.Unmarshal(store.objects[key], &doc)
	if *fetches != 0 || !doc.IsRead || len(store.removed) != 0 {
		t.Fatalf("fetches=%d doc=%+v removed=%v", *fetches, doc, store.removed)
	}

	store.uploads = nil
	if err := run.syncMessage(context.Background(), mailInbox, msg, false); err != nil {
		t.Fatal(err)
	}
	if len(store.uploads) != 0 {
		t.Fatalf("unchanged message uploaded again: %v", store.uploads)
	}
}

func TestOutlookMailSyncMoveReusesBackup(t *testing.T) {
	fetches := stubMailFetch(t)
	store := newMemMailStore()
	oldKey := mailKey("Inbox", "M1")
	store.objects[oldKey] = []byte(`{"id":"M1","body":{"content":"x"}}`)
	run := newTestMailRun(store, map[string]string{"M1": oldKey})

	if err := run.syncMessage(context.Background(), mailProject, mailDelta("M1"), false); err != nil {
		t.Fatal(err)
	}
	newKey := mailKey("Inbox/Projects", "M1")
	if *fetches != 0 || store.objects[newKey] == nil || store.objects[oldKey] != nil || run.index["M1"] != newKey {
		t.Fatalf("fetches=%d objects=%v", *fetches, store.uploads)
	}
	if s := outlook.ReadOutlookMailBackupState(store.objects[newKey]); s.FolderPath != "Inbox/Projects" {
		t.Fatalf("state %+v", s)
	}
}

func TestOutlookMailDraftsAreFetchedAgain(t *testing.T) {
	fetches := stubMailFetch(t)
	store := newMemMailStore()
	key := mailKey("Drafts", "D1")
	store.objects[key] = []byte(`{"id":"D1"}`)
	run := newTestMailRun(store, map[string]string{"D1": key})

	msg := mailDelta("D1")
	msg.IsDraft = true
	if err := run.syncMessage(context.Background(), outlook.MailFolder{ID: "F-D", Path: "Drafts", WellKnownName: "drafts"}, msg, true); err != nil {
		t.Fatal(err)
	}
	if *fetches != 1 {
		t.Fatalf("draft not refetched")
	}
}

func TestOutlookMailRemovedAndMovedAcrossFolders(t *testing.T) {
	stubMailFetch(t)
	store := newMemMailStore()
	goneKey, movedKey := mailKey("Inbox", "GONE"), mailKey("Inbox", "MOVED")
	store.objects[goneKey] = []byte(`{"id":"GONE"}`)
	store.objects[movedKey] = []byte(`{"id":"MOVED"}`)
	run := newTestMailRun(store, map[string]string{"GONE": goneKey, "MOVED": movedKey})
	ctx := context.Background()

	for _, id := range []string{"GONE", "MOVED"} {
		if err := run.syncMessage(ctx, mailInbox, &outlook.OutlookMailDeltaMessage{ID: id, IsRemoved: true}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.syncMessage(ctx, mailProject, mailDelta("MOVED"), false); err != nil {
		t.Fatal(err)
	}
	run.flushRemoved(ctx)

	if s := outlook.ReadOutlookMailBackupState(store.objects[goneKey]); !s.RemovedFromMailbox || s.RemovedAt == "" {
		t.Fatalf("deleted message not marked: %+v", s)
	}
	moved := store.objects[mailKey("Inbox/Projects", "MOVED")]
	if s := outlook.ReadOutlookMailBackupState(moved); moved == nil || s.RemovedFromMailbox {
		t.Fatalf("moved message marked removed or missing: %+v", s)
	}
}

func TestOutlookMailMigratesLegacyPairs(t *testing.T) {
	fetches := stubMailFetch(t)
	store := newMemMailStore()
	legacyMeta := mailTestPrefix + "/meta/2026/07/21/OLD1.json"
	legacyData := mailTestPrefix + "/data/2026/07/21/OLD1.json"
	store.objects[legacyMeta] = []byte(`{"message_id":"OLD1"}`)
	store.objects[legacyData] = []byte(`{"id":"OLD1","body":{"content":"kept"}}`)

	prevFolders, prevDelta, prevTranslate := outlookMailListFoldersFn, outlookMailDeltaPageFn, outlookMailTranslateFn
	t.Cleanup(func() {
		outlookMailListFoldersFn, outlookMailDeltaPageFn, outlookMailTranslateFn = prevFolders, prevDelta, prevTranslate
	})
	outlookMailListFoldersFn = func(context.Context, string, string) ([]outlook.MailFolder, error) {
		return []outlook.MailFolder{mailInbox}, nil
	}
	outlookMailTranslateFn = func(_ context.Context, _, _ string, ids []string) (map[string]string, error) {
		if len(ids) != 1 || ids[0] != "OLD1" {
			t.Fatalf("translate ids %v", ids)
		}
		return map[string]string{"OLD1": "IMM1"}, nil
	}
	outlookMailDeltaPageFn = func(context.Context, string, string) (*outlook.OutlookMailDeltaPage, error) {
		return &outlook.OutlookMailDeltaPage{Messages: []outlook.OutlookMailDeltaMessage{*mailDelta("IMM1")}, DeltaLink: "delta-1"}, nil
	}

	run := newTestMailRun(store, buildOutlookMailIndex(map[string]bool{legacyMeta: true, legacyData: true}))
	tm := &repo.TaskMemory{}
	if err := run.syncAll(context.Background(), tm); err != nil {
		t.Fatal(err)
	}

	newKey := mailKey("Inbox", "IMM1")
	var doc struct {
		Body struct {
			Content string `json:"content"`
		} `json:"body"`
	}
	_ = json.Unmarshal(store.objects[newKey], &doc)
	if *fetches != 0 || doc.Body.Content != "kept" {
		t.Fatalf("fetches=%d doc=%+v uploads=%v", *fetches, doc, store.uploads)
	}
	if store.objects[legacyMeta] != nil || store.objects[legacyData] != nil {
		t.Fatalf("legacy objects kept: %v", store.removed)
	}
	if tm.OutlookMailLayout != repo.OutlookMailLayoutFiles || tm.OutlookMailFolderDeltas["F-INBOX"] != "delta-1" || tm.OutlookMailFolderPaths["F-INBOX"] != "Inbox" {
		t.Fatalf("task memory %+v", tm)
	}
	if store.objects[mailTestPrefix+"/_folders.json"] == nil {
		t.Fatal("folder list not written")
	}
}

type fullMailStore struct{ *memMailStore }

func (s fullMailStore) upload(context.Context, string, []byte) error {
	return &quota.ErrStorageQuota{Method: "outlook", MidRun: true}
}

func stubMailFolders(t *testing.T, folders []outlook.MailFolder, pages map[string][]outlook.OutlookMailDeltaMessage) {
	t.Helper()
	prevFolders, prevDelta := outlookMailListFoldersFn, outlookMailDeltaPageFn
	t.Cleanup(func() { outlookMailListFoldersFn, outlookMailDeltaPageFn = prevFolders, prevDelta })
	outlookMailListFoldersFn = func(context.Context, string, string) ([]outlook.MailFolder, error) {
		return folders, nil
	}
	outlookMailDeltaPageFn = func(_ context.Context, _, reqURL string) (*outlook.OutlookMailDeltaPage, error) {
		for id, msgs := range pages {
			if strings.Contains(reqURL, id) {
				return &outlook.OutlookMailDeltaPage{Messages: msgs, DeltaLink: "delta-" + id}, nil
			}
		}
		return &outlook.OutlookMailDeltaPage{DeltaLink: "delta-empty"}, nil
	}
}

func TestOutlookMailPrecheckBlocksRun(t *testing.T) {
	fetches := stubMailFetch(t)
	folders := []outlook.MailFolder{
		{ID: "F-INBOX", Path: "Inbox", WellKnownName: "inbox", TotalItems: 30},
		{ID: "F-PROJ", Path: "Inbox/Projects", TotalItems: 10},
	}
	stubMailFolders(t, folders, map[string][]outlook.OutlookMailDeltaMessage{"F-INBOX": {*mailDelta("M1")}})

	store := newMemMailStore()
	run := newTestMailRun(store, nil)
	var gotEstimate int64
	run.precheck = func(_ context.Context, est int64) error {
		gotEstimate = est
		return &quota.ErrStorageQuota{Method: "outlook", EstimateBytes: est}
	}
	tm := &repo.TaskMemory{}
	err := run.syncAll(context.Background(), tm)
	if !quota.IsStorageQuota(err) {
		t.Fatalf("err = %v, want storage quota", err)
	}
	if gotEstimate != outlook.EstimateMailBytesFromFolders(folders) || gotEstimate == 0 {
		t.Fatalf("estimate = %d", gotEstimate)
	}
	if *fetches != 0 || len(store.uploads) != 0 || tm.OutlookMailFolderDeltas != nil {
		t.Fatalf("run continued: fetches=%d uploads=%v tm=%+v", *fetches, store.uploads, tm)
	}
}

func TestOutlookMailStopsWhenStorageFull(t *testing.T) {
	fetches := stubMailFetch(t)
	stubMailFolders(t, []outlook.MailFolder{mailInbox, mailProject}, map[string][]outlook.OutlookMailDeltaMessage{
		"F-INBOX": {*mailDelta("M1"), *mailDelta("M2")},
		"F-PROJ":  {*mailDelta("M3")},
	})

	run := newOutlookMailRun(ProcessorInput{}, fullMailStore{newMemMailStore()}, "tok", "https://graph/users/u", mailTestPrefix, nil)
	tm := &repo.TaskMemory{}
	err := run.syncAll(context.Background(), tm)
	if !quota.IsStorageQuota(err) {
		t.Fatalf("err = %v, want storage quota", err)
	}
	if *fetches != 1 {
		t.Fatalf("fetches = %d, want the run to stop at the first failed upload", *fetches)
	}
	if tm.OutlookMailFolderDeltas != nil {
		t.Fatalf("delta links saved after a failed run: %+v", tm.OutlookMailFolderDeltas)
	}
}

func TestBuildOutlookMailIndex(t *testing.T) {
	current := mailKey("Inbox", "M1")
	index := buildOutlookMailIndex(map[string]bool{
		current: true,
		mailTestPrefix + "/meta/2026/07/21/M1.json": true,
		mailTestPrefix + "/data/2026/07/21/M1.json": true,
		mailTestPrefix + "/meta/2026/07/21/M2.json": true,
		mailTestPrefix + "/data/2026/07/21/M3.json": true,
		mailTestPrefix + "/_folders.json":           true,
		mailTestPrefix + "/.file_placeholder":       true,
	})
	if len(index) != 2 || index["M1"] != current || index["M2"] != mailTestPrefix+"/meta/2026/07/21/M2.json" {
		t.Fatalf("index %v", index)
	}
}

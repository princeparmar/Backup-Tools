package crons

import (
	"context"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

func TestSharePointNonLibraryListsFilter(t *testing.T) {
	all := []outlook.SharePointSiteList{
		{ID: "lib1", Template: "documentLibrary", Kind: outlook.ClassifySharePointListKind("documentLibrary")},
		{ID: "list1", Template: "genericList", Kind: outlook.ClassifySharePointListKind("genericList")},
		{ID: "pages", Template: "webPageLibrary", Kind: outlook.ClassifySharePointListKind("webPageLibrary")},
	}
	non := outlook.SharePointNonLibraryLists(all)
	if len(non) != 1 || non[0].ID != "list1" {
		t.Fatalf("want only generic list (not doc lib, not Site Pages), got %#v", non)
	}
}

func TestSyncSharePointListsInventoryFailureIsNonFatal(t *testing.T) {
	prev := sharePointSiteListsFn
	t.Cleanup(func() { sharePointSiteListsFn = prev })
	sharePointSiteListsFn = func(context.Context, string, string) ([]outlook.SharePointSiteList, error) {
		return nil, errGraphForbidden
	}
	cov := newSharePointCoverage("site", spSitePrefix)
	site := sharePointSite{siteID: "site", siteKey: spSitePrefix, token: "tok"}
	if err := syncSharePointLists(context.Background(), spInput(), site, cov); err != nil {
		t.Fatalf("list inventory failure must be non-fatal: %v", err)
	}
	var lists *sharePointContentTypeCoverage
	for i := range cov.Types {
		if cov.Types[i].Type == "lists" {
			lists = &cov.Types[i]
			break
		}
	}
	if lists == nil || lists.InventoryComplete || lists.Status != spStatusPartial {
		t.Fatalf("lists coverage %#v", lists)
	}
}

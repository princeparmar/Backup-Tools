package outlook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const spTestSite = "contoso.sharepoint.com,11111111-1111-1111-1111-111111111111,22222222-2222-2222-2222-222222222222"

// spGraphServer serves /sites/{site}/drives in pages; next(base, page) writes page's nextLink.
func spGraphServer(t *testing.T, pages []string, next func(base string, page int) string) *[]string {
	t.Helper()
	var requests []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		if !strings.HasSuffix(r.URL.Path, "/drives") {
			http.NotFound(w, r)
			return
		}
		page := 0
		if p := r.URL.Query().Get("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}
		link := ""
		if page+1 < len(pages) {
			link = next(srv.URL, page+1)
		}
		body := `{"value":[` + pages[page] + `]`
		if link != "" {
			body += `,"@odata.nextLink":"` + link + `"`
		}
		_, _ = w.Write([]byte(body + "}"))
	}))
	t.Cleanup(srv.Close)
	prev := graphBaseURL
	graphBaseURL = srv.URL
	t.Cleanup(func() { graphBaseURL = prev })
	return &requests
}

func siteDrivesNext(base string, page int) string {
	return fmt.Sprintf("%s/sites/%s/drives?$select=id,name,driveType,webUrl&page=%d", base, spTestSite, page)
}

func TestListSharePointSiteLibrariesFollowsPages(t *testing.T) {
	requests := spGraphServer(t, []string{
		`{"id":"b!docs","name":"Documents","driveType":"documentLibrary","webUrl":"https://c/Shared Documents"},
		 {"id":"b!pres","name":"Preservation Hold Library","driveType":"business"}`,
		`{"id":"b!legal","name":"Legal","driveType":"documentLibrary","webUrl":"https://c/Legal"}`,
	}, siteDrivesNext)

	libs, err := ListSharePointSiteLibraries(context.Background(), "tok", spTestSite)
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 || libs[0].ID != "b!docs" || libs[0].Name != "Documents" || libs[1].ID != "b!legal" || libs[1].WebURL != "https://c/Legal" {
		t.Fatalf("libraries %+v", libs)
	}
	if len(*requests) != 2 || !strings.Contains((*requests)[0], "$select=id,name,driveType,webUrl") {
		t.Fatalf("requests %v", *requests)
	}
}

func TestListSharePointSiteLibrariesRejectsBadNextLinks(t *testing.T) {
	cases := map[string]func(base string, page int) string{
		"foreign host": func(_ string, page int) string {
			return fmt.Sprintf("https://evil.example.com/v1.0/sites/%s/drives?page=%d", spTestSite, page)
		},
		"other resource": func(base string, page int) string {
			return fmt.Sprintf("%s/users/u1/drives?page=%d", base, page)
		},
		"malformed": func(string, int) string { return "::not a url" },
		"repeated": func(base string, _ int) string {
			return fmt.Sprintf("%s/sites/%s/drives?$select=id,name,driveType,webUrl", base, spTestSite)
		},
	}
	for name, next := range cases {
		t.Run(name, func(t *testing.T) {
			requests := spGraphServer(t, []string{
				`{"id":"b!docs","name":"Documents","driveType":"documentLibrary"}`,
				`{"id":"b!legal","name":"Legal","driveType":"documentLibrary"}`,
			}, next)
			libs, err := ListSharePointSiteLibraries(context.Background(), "tok", spTestSite)
			if !errors.Is(err, ErrTeamsPaging) || libs != nil {
				t.Fatalf("libs=%v err=%v", libs, err)
			}
			if len(*requests) != 1 {
				t.Fatalf("followed a rejected link: %v", *requests)
			}
		})
	}
}

func TestListSharePointSiteLibrariesGraphError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"accessDenied","message":"no"}}`))
	}))
	defer srv.Close()
	prev := graphBaseURL
	graphBaseURL = srv.URL
	defer func() { graphBaseURL = prev }()

	_, err := ListSharePointSiteLibraries(context.Background(), "tok", spTestSite)
	var ge *TeamsGraphError
	if !errors.As(err, &ge) || ge.Status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
}

func TestSharePointSiteState(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/sites/") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()
	prev := graphBaseURL
	graphBaseURL = srv.URL
	defer func() { graphBaseURL = prev }()

	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusOK, ObjectStateActive},
		{http.StatusNotFound, ObjectStateGone},
		{http.StatusForbidden, ""},
		{http.StatusUnauthorized, ""},
	} {
		status = tc.status
		got, err := SharePointSiteState(context.Background(), "tok", spTestSite)
		if got != tc.want || (tc.want == "") != (err != nil) {
			t.Fatalf("status %d: state=%q err=%v", tc.status, got, err)
		}
	}
}

func TestRestoreSharePointNonDefaultLibraryUsesItsDrive(t *testing.T) {
	g := newFakeDriveGraph(t)
	prev := graphBaseURL
	graphBaseURL = g.srv.URL
	t.Cleanup(func() { graphBaseURL = prev })

	site := ResourceKeyPrefix(mailTenant, "site", "s1")
	legal := SharePointLibraryPrefix(site, "b!legal")
	fileKey := OneDriveFileKey(legal, OneDriveSectionMyDrive, []string{"f1"}, "i1", "nda.pdf")
	binKey := OneDriveFileKey(legal, OneDriveSectionBin, nil, "i2", "old.pdf")
	defaultKey := OneDriveFileKey(site, OneDriveSectionMyDrive, nil, "i3", "plan.docx")
	src := fakeDriveSource(map[string]fakeDriveObject{
		fileKey:    {meta: map[string]string{OneDriveMetaName: "nda.pdf", OneDriveMetaDriveID: "b!legal"}, data: []byte("nda")},
		binKey:     {meta: map[string]string{OneDriveMetaName: "old.pdf", OneDriveMetaDriveID: "b!legal", OneDriveMetaParentPath: "Archive"}, data: []byte("old")},
		defaultKey: {meta: map[string]string{OneDriveMetaName: "plan.docx", OneDriveMetaDriveID: "b!docs"}, data: []byte("doc")},
	}, map[string]string{"f1": "Contracts"})

	for _, key := range []string{fileKey, binKey, defaultKey} {
		if err := RestoreSharePointBackup(context.Background(), "tok", key, src); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	want := []string{
		"/drives/b!legal/root:/Contracts/nda.pdf:/content",
		"/drives/b!legal/root:/Archive/old.pdf:/content",
		"/drives/b!docs/root:/plan.docx:/content",
	}
	if len(g.calls) != len(want) {
		t.Fatalf("calls %+v", g.calls)
	}
	for i, w := range want {
		if g.calls[i].path != w {
			t.Fatalf("call %d path = %s, want %s", i, g.calls[i].path, w)
		}
	}
}

func TestSharePointLibraryKeys(t *testing.T) {
	site := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/site/contoso.sharepoint.com_s1_w1"
	lib := SharePointLibraryPrefix(site, "b!legal/x")
	if lib != site+"/library/b!legal_x" {
		t.Fatalf("prefix %q", lib)
	}
	if SharePointLibraryPrefix("", "b!x") != "" || SharePointLibraryPrefix(site, " ") != "" {
		t.Fatal("empty parts must give no prefix")
	}

	file := OneDriveFileKey(lib, OneDriveSectionMyDrive, []string{"F"}, "I1", "nda.pdf")
	p, ok := ParseOneDriveObjectKey(file)
	if !ok || p.Prefix != lib || p.Section != OneDriveSectionMyDrive || p.ItemID != "I1" || p.Name != "nda.pdf" || len(p.ParentIDs) != 1 {
		t.Fatalf("file %q -> %+v %v", file, p, ok)
	}
	folder := OneDriveFolderKey(lib, nil, "F", "Contracts")
	if p, ok := ParseOneDriveObjectKey(folder); !ok || p.Prefix != lib || !p.IsFolder || p.Name != "Contracts" {
		t.Fatalf("folder %q -> %+v %v", folder, p, ok)
	}
	bin := OneDriveFileKey(lib, OneDriveSectionBin, nil, "I2", "old.pdf")
	if p, ok := ParseOneDriveObjectKey(bin); !ok || p.Prefix != lib || p.Section != OneDriveSectionBin {
		t.Fatalf("bin %q -> %+v %v", bin, p, ok)
	}

	// The default library keeps the site prefix.
	def := OneDriveFileKey(site, OneDriveSectionMyDrive, nil, "I3", "plan.docx")
	if p, ok := ParseOneDriveObjectKey(def); !ok || p.Prefix != site {
		t.Fatalf("default %q -> %+v %v", def, p, ok)
	}
	// Only site keys have libraries.
	user := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/library/x/MY_DRIVE/I4$a.txt"
	if _, ok := ParseOneDriveObjectKey(user); ok {
		t.Fatalf("user key parsed as a library: %s", user)
	}
	for _, bad := range []string{site + "/library/b!x", site + "/library//MY_DRIVE/I$a", site + "/library/b!x/meta/2026/01/01/I_a.json"} {
		if _, ok := ParseOneDriveObjectKey(bad); ok {
			t.Fatalf("parsed %s", bad)
		}
	}
	if !IsOneDriveRestoreKey(file) || OneDriveFolderNames([]string{folder})["F"] != "Contracts" {
		t.Fatal("library keys must restore and name their folders")
	}
}

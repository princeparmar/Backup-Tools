//go:build graphlive

package outlook

// Read-only Graph paging probe for a designated test tenant. It sends only GET requests to Graph
// (plus the client-credentials token request), checks every continuation link with the same guards
// the backups use before following it, and writes nothing: no storage, database, cursor or job.
//
//	set -a; . ~/.storx-graph-test.env; set +a
//	STORX_GRAPH_LIVE=1 go test -tags graphlive -count=1 -run TestLiveGraphPaging -v ./apps/outlook/
//
// Required: MS_TENANT_ID, MS_CLIENT_ID, MS_CLIENT_SECRET.
// Optional: TEST_USER_ID (OneDrive owner), TEST_SITE_IDS (comma separated; default:
// the first GRAPH_PROBE_SITES listed sites), GRAPH_PROBE_TOP (page size, default 5),
// GRAPH_PROBE_MAX_PAGES (per listing, default 20), GRAPH_PROBE_SITES (default 3).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type liveProbe struct {
	name     string
	category string
	pages    int
	items    int
	complete bool
	capped   bool
	links    []string
	rejected string
	err      error
}

func (p liveProbe) outcome() string {
	switch {
	case p.rejected != "":
		return "failed closed (link rejected before it was requested)"
	case p.err != nil:
		return "error: " + redactErr(p.err)
	case p.capped:
		return "stopped at the page cap; all links followed so far were accepted"
	case p.complete:
		return "completed"
	}
	return "incomplete"
}

var liveSecretParens = regexp.MustCompile(`\([^)]*\)`)

// redactLink keeps scheme, host and path shape; query values and function parameters are hidden.
func redactLink(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return "<unparseable link>"
	}
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k+"=<redacted>")
	}
	path := liveSecretParens.ReplaceAllString(u.EscapedPath(), "(<redacted>)")
	out := u.Scheme + "://" + u.Host + path
	if len(keys) > 0 {
		out += "?" + strings.Join(keys, "&")
	}
	return out
}

var (
	liveBearer = regexp.MustCompile(`(?i)(bearer\s+)\S+`)
	liveURL    = regexp.MustCompile(`https?://[^\s"'<>]+`)
)

// redactErr hides bearer tokens and passes every URL through redactLink: transport errors quote
// the full request URL, whose query carries delta and skip tokens.
func redactErr(err error) string {
	s := liveBearer.ReplaceAllString(err.Error(), "${1}<redacted>")
	s = liveURL.ReplaceAllStringFunc(s, redactLink)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return def
}

// probeListPages pages a Graph listing with the graphListAll guard.
func probeListPages(ctx context.Context, token, name, first string, maxPages int) liveProbe {
	p := liveProbe{name: name, category: "listing"}
	guard := newGraphListGuard(first)
	next := first
	for next != "" {
		if p.pages >= maxPages {
			p.capped = true
			return p
		}
		body, status, err := graphDoJSON(ctx, token, http.MethodGet, next, nil)
		if err != nil {
			p.err = err
			return p
		}
		if status < 200 || status >= 300 {
			p.err = fmt.Errorf("http %d: %s", status, truncateForErr(body))
			return p
		}
		p.pages++
		var page struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			p.err = fmt.Errorf("decode page: %w", err)
			return p
		}
		p.items += len(page.Value)
		next = strings.TrimSpace(page.NextLink)
		if next == "" {
			p.complete = true
			return p
		}
		p.links = append(p.links, redactLink(next))
		if err := guard.Next(next); err != nil {
			p.rejected, p.err = redactLink(next), err
			return p
		}
	}
	return p
}

// probeDriveDelta pages a drive delta with the backup's DriveDeltaGuard and stops at the deltaLink.
func probeDriveDelta(ctx context.Context, token, name, driveRoot string, top, maxPages int) liveProbe {
	p := liveProbe{name: name, category: "drive delta"}
	guard := NewDriveDeltaGuard(driveRoot)
	next := OneDriveInitialDeltaURL(driveRoot) + "?$select=id&$top=" + strconv.Itoa(top)
	if err := guard.Start(next); err != nil {
		p.rejected, p.err = redactLink(next), err
		return p
	}
	for {
		if p.pages >= maxPages {
			p.capped = true
			return p
		}
		page, err := FetchOneDriveDeltaPage(ctx, token, next)
		if err != nil {
			p.err = err
			return p
		}
		p.pages++
		p.items += len(page.Items)
		if page.NextLink != "" {
			p.links = append(p.links, redactLink(page.NextLink))
			if err := guard.Next(page.NextLink); err != nil {
				p.rejected, p.err = redactLink(page.NextLink), err
				return p
			}
			next = page.NextLink
			continue
		}
		p.links = append(p.links, "deltaLink: "+redactLink(page.DeltaLink))
		if err := guard.Final(page.DeltaLink); err != nil {
			p.rejected, p.err = redactLink(page.DeltaLink), err
			return p
		}
		p.complete = true
		return p
	}
}

func TestLiveGraphPaging(t *testing.T) {
	if os.Getenv("STORX_GRAPH_LIVE") != "1" {
		t.Skip("live Graph probe: set STORX_GRAPH_LIVE=1 with the test-tenant env loaded")
	}
	tenant, clientID, secret := os.Getenv("MS_TENANT_ID"), os.Getenv("MS_CLIENT_ID"), os.Getenv("MS_CLIENT_SECRET")
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(clientID) == "" || strings.TrimSpace(secret) == "" {
		t.Fatal("MS_TENANT_ID, MS_CLIENT_ID and MS_CLIENT_SECRET are required")
	}
	top, maxPages := envInt("GRAPH_PROBE_TOP", 5), envInt("GRAPH_PROBE_MAX_PAGES", 20)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	token, _, err := requestAppOnlyToken(ctx, tenant, clientID, secret)
	if err != nil {
		t.Fatalf("app-only token: %s", redactErr(err))
	}
	t.Logf("tenant %s…, graph host %s, page size %d, page cap %d", tenant[:min(8, len(tenant))], graphBaseURL, top, maxPages)

	var probes []liveProbe
	probes = append(probes,
		probeListPages(ctx, token, "tenant users", graphBaseURL+"/users?$select=id&$top="+strconv.Itoa(top), maxPages),
		probeListPages(ctx, token, "sites: getAllSites", graphBaseURL+"/sites/getAllSites?$select=id&$top="+strconv.Itoa(top), maxPages),
		probeListPages(ctx, token, "sites: search=*", graphBaseURL+"/sites?search=*&$select=id&$top="+strconv.Itoa(top), maxPages),
	)

	if users, err := ListTenantDirectoryUsers(ctx, token); err != nil {
		probes = append(probes, liveProbe{name: "ListTenantDirectoryUsers (production)", category: "listing", err: err})
	} else {
		probes = append(probes, liveProbe{name: "ListTenantDirectoryUsers (production)", category: "listing", items: len(users), complete: true})
	}
	sites, err := ListTenantSharePointSites(ctx, token, "", 0)
	if err != nil {
		probes = append(probes, liveProbe{name: "ListTenantSharePointSites (production)", category: "listing", err: err})
	} else {
		probes = append(probes, liveProbe{name: "ListTenantSharePointSites (production)", category: "listing", items: len(sites), complete: true})
	}

	if user := strings.TrimSpace(os.Getenv("TEST_USER_ID")); user != "" {
		base, err := UserBaseURL(user, "", "", true)
		if err != nil {
			t.Fatal(err)
		}
		probes = append(probes, probeDriveDelta(ctx, token, "OneDrive of TEST_USER_ID", base+"/drive", top, maxPages))
	}

	var siteIDs []string
	for _, s := range strings.Split(os.Getenv("TEST_SITE_IDS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			siteIDs = append(siteIDs, s)
		}
	}
	if len(siteIDs) == 0 {
		for i := 0; i < len(sites) && i < envInt("GRAPH_PROBE_SITES", 3); i++ {
			siteIDs = append(siteIDs, sites[i].ID)
		}
	}
	for i, siteID := range siteIDs {
		libs, err := ListSharePointSiteLibraries(ctx, token, siteID)
		if err != nil {
			probes = append(probes, liveProbe{name: fmt.Sprintf("site %d libraries", i+1), category: "listing", err: err})
			continue
		}
		probes = append(probes, liveProbe{name: fmt.Sprintf("site %d libraries", i+1), category: "listing", items: len(libs), complete: true})
		for j, lib := range libs {
			probes = append(probes, probeDriveDelta(ctx, token, fmt.Sprintf("site %d library %d", i+1, j+1), DriveRootURLFromDriveID(lib.ID), top, maxPages))
		}
	}

	for _, p := range probes {
		t.Logf("[%s] %s: pages=%d items=%d -> %s", p.category, p.name, p.pages, p.items, p.outcome())
		for _, l := range p.links {
			t.Logf("    link %s", l)
		}
		if p.rejected != "" {
			t.Logf("    REJECTED %s: %s", p.rejected, redactErr(p.err))
		}
	}
	t.Log("writes: none (GET only; no storage, database, cursor or job access)")
}

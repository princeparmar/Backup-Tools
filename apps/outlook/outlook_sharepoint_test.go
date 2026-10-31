package outlook

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeSharePointSiteKey(t *testing.T) {
	raw := "contoso.sharepoint.com,abc123,def456"
	got := SanitizeSharePointSiteKey(raw)
	if strings.ContainsAny(got, ",/\\:?*#") {
		t.Fatalf("unsafe chars remain: %q", got)
	}
	if got == "" {
		t.Fatal("empty sanitize result")
	}
}

func TestGraphSharePointSiteID(t *testing.T) {
	t.Parallel()
	const graph = "space2.sharepoint.com,edf2e428-d991-4aa0-9c11-aaaaaaaaaaaa,bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	const vault = "space2.sharepoint.com_edf2e428-d991-4aa0-9c11-aaaaaaaaaaaa_bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	tests := []struct {
		in   string
		want string
	}{
		{in: graph, want: graph},
		{in: vault, want: graph},
		{in: "  " + vault + "  ", want: graph},
		{in: "", want: ""},
		{in: "plain-guid-site", want: "plain-guid-site"},
	}
	for _, tt := range tests {
		if got := GraphSharePointSiteID(tt.in); got != tt.want {
			t.Fatalf("%q: got %q want %q", tt.in, got, tt.want)
		}
	}
	key := ResourceKeyPrefix("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "site", graph) + "/MY_DRIVE/F1/ITEM1$file.docx"
	if got := GraphSharePointSiteIDFromKey(key); got != graph {
		t.Fatalf("from key: got %q want %q", got, graph)
	}
}

func TestSharePointIDBasedKeys(t *testing.T) {
	siteKey := "contoso.sharepoint.com,abc,def"
	meta := SharePointIDBasedMetaKey(siteKey, "ITEM1", "report.pdf", "2026-07-21T15:04:05Z")
	data := SharePointIDBasedDataKey(siteKey, "ITEM1", "report.pdf", "2026-07-21T15:04:05Z")
	if !strings.Contains(meta, "ITEM1_") || !strings.HasSuffix(meta, ".json") {
		t.Fatalf("meta key: %s", meta)
	}
	if !strings.Contains(data, "ITEM1_") || strings.HasSuffix(data, ".json") {
		t.Fatalf("data key: %s", data)
	}
	if !strings.Contains(meta, "/meta/2026/07/21/") {
		t.Fatalf("expected date path in meta: %s", meta)
	}
}

func TestSharePointInitialDeltaURL(t *testing.T) {
	got := SharePointInitialDeltaURL("drive123")
	if !strings.Contains(got, "/drives/drive123/root/delta") {
		t.Fatalf("unexpected delta url: %s", got)
	}
}

func TestErrSharePointDeltaInvalid(t *testing.T) {
	if !errors.Is(ErrSharePointDeltaInvalid, ErrSharePointDeltaInvalid) {
		t.Fatal("sentinel")
	}
}

func TestParseSharePointSiteURL(t *testing.T) {
	host, path, err := parseSharePointSiteURL("https://contoso.sharepoint.com/sites/HR")
	if err != nil {
		t.Fatal(err)
	}
	if host != "contoso.sharepoint.com" || path != "/sites/HR" {
		t.Fatalf("got host=%q path=%q", host, path)
	}
}

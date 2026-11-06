package mstenant

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Microsoft Graph tokens are minted only through the resolver, so every token is checked against
// the user's credential, the tenant link, consent and capabilities.
var microsoftMintCall = regexp.MustCompile(`\boutlook\.(AuthTokenUsingRefreshToken|AuthTokenForTenant|AuthTokenForTenantScope|AppOnlyToken)\b`)

func TestMicrosoftTokensAreMintedOnlyByResolver(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"mstenant": true, filepath.Join("apps", "outlook"): true}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if allowed[rel] || strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if m := microsoftMintCall.FindString(line); m != "" {
				t.Errorf("%s:%d calls %s; use mstenant.Resolve", rel, i+1, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

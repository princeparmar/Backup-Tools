package outlook

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ErrOneDriveDeltaPaging means a drive delta link was off the Graph host, not a delta of the same
// drive's root, or already followed. The drive sync stops and keeps its saved cursor.
var ErrOneDriveDeltaPaging = errors.New("onedrive delta paging error")

// maxDriveDeltaPages caps one drive delta round; repeated links are caught before this.
const maxDriveDeltaPages = 100000

// newGraphListGuard is the page guard of graphListAll. Besides the Teams checks it treats paths as
// one resource when Graph writes them differently: /me and /users/{id}, and casts or functions with
// or without the microsoft.graph. prefix and empty parentheses.
func newGraphListGuard(firstURL string) *TeamsPageGuard {
	g := NewTeamsPageGuard(firstURL)
	g.sameResource = sameGraphListResource
	return g
}

var graphNameSegment = regexp.MustCompile(`(?i)^(?:microsoft\.graph\.)?([a-z]+)(?:\(\))?$`)

// graphMeOwner stands for the user id that Graph may write in place of /me.
const graphMeOwner = "\x00me"

func graphBasePathSegments() int {
	base, err := url.Parse(graphBaseURL)
	if err != nil {
		return 0
	}
	p := strings.Trim(base.Path, "/")
	if p == "" {
		return 0
	}
	return len(strings.Split(p, "/"))
}

func graphListResource(path string) []string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	out := make([]string, 0, len(segs)+1)
	for _, s := range segs {
		if m := graphNameSegment.FindStringSubmatch(s); m != nil {
			s = strings.ToLower(m[1])
		}
		out = append(out, s)
	}
	n := graphBasePathSegments()
	if len(out) > n && out[n] == "me" {
		out = append(out[:n], append([]string{"users", graphMeOwner}, out[n+1:]...)...)
	}
	return out
}

func sameGraphListResource(first, next string) bool {
	a, b := graphListResource(first), graphListResource(next)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] && a[i] != graphMeOwner && b[i] != graphMeOwner {
			return false
		}
	}
	return true
}

// driveDeltaTarget is the drive a delta link reads: drives/{drive}, me/drive,
// {users|groups|sites}/{owner}/drive or sites/{owner}/drives/{drive}.
type driveDeltaTarget struct {
	kind  string
	owner string
	drive string
}

var driveDeltaSegment = regexp.MustCompile(`(?i)^(?:microsoft\.graph\.)?delta(?:\(.*\))?$`)

// parseDriveDeltaLink accepts an absolute link on the Graph host, under the API version, ending in
// /root/delta or /root/delta(token=…), on a drive.
func parseDriveDeltaLink(link string) (driveDeltaTarget, bool) {
	base, err := url.Parse(graphBaseURL)
	if err != nil {
		return driveDeltaTarget{}, false
	}
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.User != nil || u.Fragment != "" ||
		!strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return driveDeltaTarget{}, false
	}
	var segs []string
	for _, seg := range strings.Split(strings.Trim(u.EscapedPath(), "/"), "/") {
		s, err := url.PathUnescape(seg)
		if err != nil || s == "" || s == "." || s == ".." {
			return driveDeltaTarget{}, false
		}
		if m := teamsODataKey.FindStringSubmatch(s); m != nil {
			segs = append(segs, strings.ToLower(m[1]), m[2])
			continue
		}
		if teamsODataName.MatchString(s) {
			s = strings.ToLower(s)
		}
		segs = append(segs, s)
	}
	if bp := strings.Trim(base.Path, "/"); bp != "" {
		want := strings.Split(bp, "/")
		if len(segs) < len(want) {
			return driveDeltaTarget{}, false
		}
		for i := range want {
			if !strings.EqualFold(segs[i], want[i]) {
				return driveDeltaTarget{}, false
			}
		}
		segs = segs[len(want):]
	}
	n := len(segs)
	if n < 4 || !driveDeltaSegment.MatchString(segs[n-1]) || segs[n-2] != "root" {
		return driveDeltaTarget{}, false
	}
	res := segs[:n-2]
	switch {
	case len(res) == 2 && res[0] == "drives":
		return driveDeltaTarget{kind: "drives", drive: res[1]}, true
	case len(res) == 2 && res[0] == "me" && res[1] == "drive":
		return driveDeltaTarget{kind: "me"}, true
	case len(res) == 3 && res[2] == "drive" && (res[0] == "users" || res[0] == "groups" || res[0] == "sites"):
		return driveDeltaTarget{kind: res[0], owner: res[1]}, true
	case len(res) == 4 && res[0] == "sites" && res[2] == "drives":
		return driveDeltaTarget{kind: "sites", owner: res[1], drive: res[3]}, true
	}
	return driveDeltaTarget{}, false
}

// DriveDeltaGuard checks the links of one drive delta round: the first URL or saved cursor, each
// nextLink and the final deltaLink. Graph may answer a /users/{id}/drive request with
// /drives/{driveId} links and may write the token in the path, so links are matched on the drive
// they read; once a drives/{id} link is seen the round stays on that drive.
type DriveDeltaGuard struct {
	Max   int
	root  driveDeltaTarget
	ok    bool
	drive string
	seen  map[string]bool
	pages int
}

// NewDriveDeltaGuard starts a round for the drive at driveRootURL.
func NewDriveDeltaGuard(driveRootURL string) *DriveDeltaGuard {
	root, ok := parseDriveDeltaLink(OneDriveInitialDeltaURL(driveRootURL))
	return &DriveDeltaGuard{Max: maxDriveDeltaPages, root: root, ok: ok, drive: root.drive, seen: map[string]bool{}}
}

// Start accepts the round's first request URL.
func (g *DriveDeltaGuard) Start(link string) error {
	if err := g.check(link, "delta URL"); err != nil {
		return err
	}
	g.seen[strings.TrimSpace(link)] = true
	return nil
}

// Next accepts the next page URL of the round.
func (g *DriveDeltaGuard) Next(link string) error {
	g.pages++
	if g.Max > 0 && g.pages > g.Max {
		return fmt.Errorf("%w: more than %d pages", ErrOneDriveDeltaPaging, g.Max)
	}
	if err := g.check(link, "nextLink"); err != nil {
		return err
	}
	key := strings.TrimSpace(link)
	if g.seen[key] {
		return fmt.Errorf("%w: repeated nextLink", ErrOneDriveDeltaPaging)
	}
	g.seen[key] = true
	return nil
}

// Final accepts the deltaLink that ends the round.
func (g *DriveDeltaGuard) Final(link string) error {
	return g.check(link, "deltaLink")
}

func (g *DriveDeltaGuard) check(link, what string) error {
	if !g.ok {
		return fmt.Errorf("%w: drive root is not a Graph drive", ErrOneDriveDeltaPaging)
	}
	t, ok := parseDriveDeltaLink(link)
	if !ok {
		return fmt.Errorf("%w: %s is not a delta of a Graph drive root", ErrOneDriveDeltaPaging, what)
	}
	if !g.sameDrive(t) {
		return fmt.Errorf("%w: %s to another drive", ErrOneDriveDeltaPaging, what)
	}
	return nil
}

func (g *DriveDeltaGuard) sameDrive(t driveDeltaTarget) bool {
	if t.drive != "" {
		if g.drive == "" {
			g.drive = t.drive
			return true
		}
		return t.drive == g.drive
	}
	if g.root.drive != "" {
		return false
	}
	switch {
	case t.kind == "me" || g.root.kind == "me":
		return (t.kind == "me" || t.kind == "users") && (g.root.kind == "me" || g.root.kind == "users")
	case t.kind != g.root.kind:
		return false
	case t.kind == "users" && (strings.Contains(t.owner, "@") != strings.Contains(g.root.owner, "@")):
		return true
	}
	return strings.EqualFold(t.owner, g.root.owner)
}

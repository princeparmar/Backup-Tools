package restore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

// withRefreshHooks swaps the registered refresh handlers for one test.
func withRefreshHooks(t *testing.T, google, microsoft func(ctx context.Context, d *RestoreDeps) error) {
	t.Helper()
	registryMu.Lock()
	prevGoogle, prevMicrosoft := refreshGoogleTokenFn, refreshMicrosoftTokenFn
	refreshGoogleTokenFn, refreshMicrosoftTokenFn = google, microsoft
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		refreshGoogleTokenFn, refreshMicrosoftTokenFn = prevGoogle, prevMicrosoft
		registryMu.Unlock()
	})
}

type refreshCalls struct{ google, microsoft int }

func countingHooks(t *testing.T, googleErr, microsoftErr error) *refreshCalls {
	t.Helper()
	calls := &refreshCalls{}
	withRefreshHooks(t,
		func(context.Context, *RestoreDeps) error { calls.google++; return googleErr },
		func(context.Context, *RestoreDeps) error { calls.microsoft++; return microsoftErr },
	)
	return calls
}

func depsForMethod(method string) *RestoreDeps {
	return &RestoreDeps{
		Job:          &repo.RestoreJobListingDB{Method: method},
		RefreshToken: "rt",
		AuthMode:     RestoreAuthModeOAuth,
	}
}

func TestRefreshAccessTokenForJobGoogleUsesGoogleRefresh(t *testing.T) {
	for _, method := range []string{"gmail", "google_drive", "google_calendar"} {
		calls := countingHooks(t, nil, nil)
		if err := depsForMethod(method).refreshAccessTokenForJob(context.Background()); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if calls.google != 1 || calls.microsoft != 0 {
			t.Fatalf("%s: google=%d microsoft=%d", method, calls.google, calls.microsoft)
		}
	}
}

func TestRefreshAccessTokenForJobMicrosoftUsesMicrosoftRefresh(t *testing.T) {
	for _, method := range []string{"outlook", "outlook_calendar", "outlook_contacts", "outlook_onedrive", "outlook_sharepoint", "outlook_teams"} {
		calls := countingHooks(t, nil, nil)
		if err := depsForMethod(method).refreshAccessTokenForJob(context.Background()); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if calls.microsoft != 1 || calls.google != 0 {
			t.Fatalf("%s: google=%d microsoft=%d", method, calls.google, calls.microsoft)
		}
	}
}

func TestRefreshAccessTokenForJobMissingHandler(t *testing.T) {
	withRefreshHooks(t, nil, nil)

	err := depsForMethod("outlook").refreshAccessTokenForJob(context.Background())
	if err == nil || err.Error() != "microsoft token refresh: microsoft restore auth not registered" {
		t.Fatalf("microsoft: %v", err)
	}

	err = depsForMethod("gmail").refreshAccessTokenForJob(context.Background())
	if err == nil || err.Error() != "google restore auth not registered" {
		t.Fatalf("google: %v", err)
	}
}

func TestRefreshAccessTokenForJobMicrosoftOnlyHandlerMissing(t *testing.T) {
	calls := &refreshCalls{}
	withRefreshHooks(t, func(context.Context, *RestoreDeps) error { calls.google++; return nil }, nil)

	err := depsForMethod("outlook_onedrive").refreshAccessTokenForJob(context.Background())
	if err == nil || !strings.Contains(err.Error(), "microsoft restore auth not registered") {
		t.Fatalf("got %v", err)
	}
	if calls.google != 0 {
		t.Fatalf("google refresh must not run for a Microsoft job, ran %d times", calls.google)
	}
}

func TestRefreshAccessTokenForJobFailureNamesProvider(t *testing.T) {
	signin := errors.New("Microsoft sign-in required: refresh token expired")
	countingHooks(t, errors.New("google token refresh: invalid_grant"), signin)

	err := depsForMethod("outlook_calendar").refreshAccessTokenForJob(context.Background())
	if !errors.Is(err, signin) {
		t.Fatalf("microsoft error not wrapped: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "microsoft token refresh: ") || strings.Contains(strings.ToLower(err.Error()), "google") {
		t.Fatalf("microsoft error mislabelled: %v", err)
	}

	err = depsForMethod("gmail").refreshAccessTokenForJob(context.Background())
	if err == nil || err.Error() != "google token refresh: invalid_grant" {
		t.Fatalf("google error changed: %v", err)
	}
}

func TestShouldOAuth401RetryUnchangedForBothProviders(t *testing.T) {
	unauthorized := BatchResult{Failed: 2, FailedKeys: []FailedKey{{ErrorCode: "401"}, {ErrorCode: "401"}}}
	invalidToken := BatchResult{Failed: 1, FailedKeys: []FailedKey{{Reason: "Invalid_Token: expired"}}}
	partial := BatchResult{Processed: 1, Failed: 1, FailedKeys: []FailedKey{{ErrorCode: "401"}}}
	forbidden := BatchResult{Failed: 1, FailedKeys: []FailedKey{{ErrorCode: "403"}}}

	for _, method := range []string{"gmail", "outlook"} {
		deps := depsForMethod(method)
		cases := []struct {
			name   string
			deps   *RestoreDeps
			result BatchResult
			want   bool
		}{
			{"all 401", deps, unauthorized, true},
			{"invalid_token reason", deps, invalidToken, true},
			{"partial success", deps, partial, false},
			{"no failures", deps, BatchResult{Processed: 3}, false},
			{"non-401 failure", deps, forbidden, false},
			{"no refresh token", &RestoreDeps{Job: deps.Job, AuthMode: RestoreAuthModeOAuth}, unauthorized, false},
			{"dwd", &RestoreDeps{Job: deps.Job, RefreshToken: "rt", AuthMode: RestoreAuthModeDWD}, unauthorized, false},
		}
		for _, tc := range cases {
			if got := shouldOAuth401Retry(tc.deps, tc.result); got != tc.want {
				t.Fatalf("%s/%s: got %v want %v", method, tc.name, got, tc.want)
			}
		}
	}
}

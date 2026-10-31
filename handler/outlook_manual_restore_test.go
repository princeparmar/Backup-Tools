package handler

import (
	"strings"
	"testing"
)

func TestOutlookManualRestoreUserBase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		svc  *OutlookService
		want string
	}{
		{name: "signed-in mailbox", svc: NewOutlookService("grant", "token"), want: "/me"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.svc.restoreUserBase()
			if err != nil || !strings.HasSuffix(got, tt.want) {
				t.Fatalf("base=%q err=%v want suffix %q", got, err, tt.want)
			}
		})
	}
}

func TestMicrosoftManualKeysNeedTenantCheck(t *testing.T) {
	t.Parallel()
	const tenantKey = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/inbox/m.json"
	tests := []struct {
		name string
		keys []string
		want bool
	}{
		{name: "tenant backup keys", keys: []string{tenantKey}, want: true},
		{name: "legacy flat key", keys: []string{"messages/1.json"}, want: false},
		{name: "mixed batch", keys: []string{tenantKey, "messages/1.json"}, want: false},
		{name: "empty", keys: nil, want: false},
		{name: "blank", keys: []string{"  "}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := microsoftManualKeysNeedTenantCheck(tt.keys); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

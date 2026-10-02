package google

import "testing"

func TestMediaBackupNeedsDelegation_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mailbox string
		holder  string
		want    bool
	}{
		{name: "same user", mailbox: "admin@contoso.com", holder: "admin@contoso.com", want: false},
		{name: "corporate employee", mailbox: "emp@contoso.com", holder: "admin@contoso.com", want: true},
		{name: "case insensitive", mailbox: "Emp@Contoso.com", holder: "admin@contoso.com", want: true},
		{name: "empty mailbox", mailbox: "", holder: "admin@contoso.com", want: false},
		{name: "me mailbox", mailbox: "me", holder: "admin@contoso.com", want: false},
		{name: "empty holder", mailbox: "emp@contoso.com", holder: "", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := MediaBackupNeedsDelegation(tt.mailbox, tt.holder); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestMediaBackupDelegationSubject_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mailbox   string
		holder    string
		wantSubj  string
		wantError bool
	}{
		{name: "employee subject", mailbox: "emp@contoso.com", holder: "admin@contoso.com", wantSubj: "emp@contoso.com"},
		{name: "mailbox me falls back to holder", mailbox: "me", holder: "admin@contoso.com", wantSubj: "admin@contoso.com"},
		{name: "both empty", mailbox: "", holder: "", wantError: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MediaBackupDelegationSubject(tt.mailbox, tt.holder)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.wantSubj {
				t.Fatalf("got %q want %q", got, tt.wantSubj)
			}
		})
	}
}

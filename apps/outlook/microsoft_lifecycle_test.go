package outlook

import (
	"errors"
	"testing"
)

func TestIsResourceMissingError(t *testing.T) {
	missing := []string{
		`graph users/x: HTTP 404: {"error":{"code":"Request_ResourceNotFound"}}`,
		`outlook mail delta http 404: {"error":{"code":"MailboxNotEnabledForRESTAPI"}}`,
		`outlook mail list http 404: {"error":{"code":"ErrorInvalidUser"}}`,
		`onedrive delta http 404: {"error":{"code":"ResourceNotFound","message":"User's mysite not found."}}`,
	}
	for _, m := range missing {
		if !IsResourceMissingError(errors.New(m)) {
			t.Errorf("not classified as missing: %s", m)
		}
	}
	for _, m := range []string{"", "outlook mail message http 404: item gone", "storx token not found", "http 429"} {
		if m != "" && IsResourceMissingError(errors.New(m)) {
			t.Errorf("wrongly classified as missing: %s", m)
		}
	}
	if IsResourceMissingError(nil) {
		t.Error("nil error classified as missing")
	}
}

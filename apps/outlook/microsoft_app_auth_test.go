package outlook

import "testing"

func TestAcquireMicrosoftAppOnlyTokenRequiresFields(t *testing.T) {
	_, err := AcquireMicrosoftAppOnlyToken(nil, "", "id", "secret")
	if err == nil {
		t.Fatal("expected error for missing tenant")
	}
}

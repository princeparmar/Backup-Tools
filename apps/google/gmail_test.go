package google

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/StorX2-0/Backup-Tools/logger"
	"google.golang.org/api/gmail/v1"
)

//go:embed test.mail
var gmailMessage string

func Test_createRawMessage(t *testing.T) {

	var message gmail.Message

	err := json.Unmarshal([]byte(gmailMessage), &message)
	if err != nil {
		t.Errorf("createRawMessage() error = %v", err)
		return
	}

	got, err := createRawMessage(&message)
	if err != nil {
		t.Errorf("createRawMessage() error = %v", err)
		return
	}

	logger.Info(got)
}

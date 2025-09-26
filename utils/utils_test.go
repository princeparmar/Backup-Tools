package utils

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/logger"
)

func TestRandStr(t *testing.T) {
	logger.Info(RandStringRunes(32))
}

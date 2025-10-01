package utils

import (
	"context"
	"testing"

	"github.com/StorX2-0/Backup-Tools/logger"
)

func TestRandStr(t *testing.T) {
	logger.Info(context.Background(), RandStringRunes(32))
}

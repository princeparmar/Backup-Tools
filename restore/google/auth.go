package googlestore

import (
	"context"
	"fmt"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/restore"
)

// MintAccessToken fills GoogleToken from the stored refresh token.
func MintAccessToken(ctx context.Context, d *restore.RestoreDeps) error {
	_ = ctx
	if d == nil {
		return fmt.Errorf("restore deps required")
	}
	if strings.TrimSpace(d.GoogleToken) != "" {
		return nil
	}
	rt := strings.TrimSpace(d.RefreshToken)
	if rt == "" {
		return fmt.Errorf("google refresh token missing")
	}
	tok, err := google.AuthTokenUsingRefreshToken(rt)
	if err != nil {
		return fmt.Errorf("google token refresh: %w", err)
	}
	d.GoogleToken = tok
	return nil
}

// RefreshAccessToken forces a new Google access token (401 retry path).
func RefreshAccessToken(ctx context.Context, d *restore.RestoreDeps) error {
	if d == nil {
		return fmt.Errorf("restore deps required")
	}
	d.GoogleToken = ""
	return MintAccessToken(ctx, d)
}

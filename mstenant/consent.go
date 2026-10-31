package mstenant

import (
	"context"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
)

var servicePrincipalFn = outlook.ServicePrincipalForApp

// AppToken returns the tenant app-only token and its granted application roles. Only the consent
// and capability checks use it directly; backup, browse and restore go through Resolve.
func AppToken(ctx context.Context, tenantID string) (string, []string, error) {
	return appOnlyTokenFn(ctx, normalize(tenantID))
}

// ServicePrincipal looks up the platform app's enterprise application in the token's tenant.
func ServicePrincipal(ctx context.Context, appToken string) (string, bool, error) {
	return servicePrincipalFn(ctx, appToken, strings.TrimSpace(utils.GetEnvWithKey("OUTLOOK_CLIENT_ID")))
}

// HandleAppTokenFailure applies tenant-wide consequences of an app-only token failure: an expired
// platform secret marks application links as errored; an unknown tenant becomes unavailable.
func HandleAppTokenFailure(database *db.PostgresDb, tenantID string, err error) {
	switch outlook.ClassifyTokenError(err) {
	case outlook.TokenErrorSecretExpired:
		_ = database.MicrosoftLinkRepo.MarkApplicationTokenError(err.Error())
	case outlook.TokenErrorTenantNotFound:
		MarkTenantUnavailable(database, tenantID, err.Error())
	}
}

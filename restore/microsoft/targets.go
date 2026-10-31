package microsoft

import (
	"context"
	"errors"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/restore"
)

// Graph seams (overridden in tests).
var (
	tenantOrganizationFn     = outlook.GetTenantOrganization
	findDirectoryUserFn      = outlook.FindDirectoryUser
	findOrganizationTargetFn = FindOrganizationTarget
	checkRestoreTargetFn     = outlook.CheckRestoreTarget
)

// OrganizationTenant is a tenant the user connected for organization backup. Any user of it can
// be a restore destination, written with the tenant's app-only token.
type OrganizationTenant struct {
	TenantID      string
	CredentialID  uint
	Name          string
	DefaultDomain string
	Domains       []string
	Token         string
}

// HasDomain reports whether domain is a verified domain of the tenant.
func (t OrganizationTenant) HasDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	for _, d := range t.Domains {
		if d == domain {
			return true
		}
	}
	return false
}

// ListOrganizationTenants returns the tenants the user connected in application mode, one per
// tenant, with a working app-only token. Tenants whose token can't be minted are left out.
func ListOrganizationTenants(ctx context.Context, store *db.PostgresDb, userID string) ([]OrganizationTenant, error) {
	creds, err := store.CredentialRepo.ListByUserID(userID, "")
	if err != nil {
		return nil, err
	}
	var out []OrganizationTenant
	seen := map[string]bool{}
	for i := range creds {
		cred := &creds[i]
		if !cred.IsMicrosoft() {
			continue
		}
		links, err := store.MicrosoftLinkRepo.ListByCredential(cred.ID)
		if err != nil {
			return nil, err
		}
		for j := range links {
			link := &links[j]
			tid := strings.ToLower(strings.TrimSpace(link.TenantID))
			if tid == "" || seen[tid] || !link.Connected() || !link.Application() {
				continue
			}
			tc, err := resolveTenantFn(ctx, store, mstenant.Request{
				UserID: userID, CredentialID: cred.ID, TenantID: tid, RequireApplication: true,
			})
			if err != nil {
				logger.Warn(ctx, "restore destination tenant skipped", logger.String("tenant_id", tid), logger.ErrorField(err))
				continue
			}
			seen[tid] = true
			t := OrganizationTenant{TenantID: tid, CredentialID: cred.ID, Token: tc.Token,
				Name: strings.TrimSpace(link.TenantName), DefaultDomain: strings.ToLower(strings.TrimSpace(link.DefaultDomain))}
			if org, err := tenantOrganizationFn(ctx, tc.Token); err == nil {
				t.Name, t.DefaultDomain, t.Domains = org.Name, org.DefaultDomain, org.Domains
			} else {
				logger.Warn(ctx, "restore destination tenant domains unavailable", logger.String("tenant_id", tid), logger.ErrorField(err))
			}
			if t.DefaultDomain == "" {
				t.DefaultDomain = strings.ToLower(restore.EmailDomain(cred.Email))
			}
			if !t.HasDomain(t.DefaultDomain) {
				t.Domains = append(t.Domains, t.DefaultDomain)
			}
			if t.Name == "" {
				t.Name = t.DefaultDomain
			}
			out = append(out, t)
		}
	}
	return out, nil
}

// FindOrganizationTarget finds the organization tenant that has a user with email (UPN or primary
// mail). Only tenants verifying the email's domain are searched, so guests never match.
func FindOrganizationTarget(ctx context.Context, store *db.PostgresDb, userID, email string) (*OrganizationTenant, *outlook.DirectoryUser, error) {
	tenants, err := ListOrganizationTenants(ctx, store, userID)
	if err != nil {
		return nil, nil, err
	}
	domain := restore.EmailDomain(email)
	for i := range tenants {
		if !tenants[i].HasDomain(domain) {
			continue
		}
		u, err := findDirectoryUserFn(ctx, tenants[i].Token, email)
		if errors.Is(err, outlook.ErrDirectoryUserNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		return &tenants[i], &u, nil
	}
	return nil, nil, nil
}

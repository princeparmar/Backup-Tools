package mstenant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// tenantAccessCheckTTL: skip live token/role probes on list when the link was checked this recently.
// Explicit refresh (GET ?refresh=1 or POST .../roles/refresh) always re-probes.
const tenantAccessCheckTTL = 10 * time.Minute

// tenantAccessCheckConcurrency caps parallel Graph/token probes while listing tenants.
const tenantAccessCheckConcurrency = 4

// Seams (overridden in tests).
var (
	signInTokenFn     = outlook.AuthTokenResponseForAccountDetection
	homeTokenFn       = outlook.AuthTokenResponseUsingRefreshToken
	discoverTenantsFn = outlook.DiscoverTenants
	meIdentityFn      = outlook.GraphMeIdentity
	readRolesFn       = outlook.ReadTenantRoles
)

// SignIn is the identity behind a delegated refresh token.
type SignIn struct {
	ObjectID     string
	HomeTenantID string
	Email        string
	IdentityType outlook.MicrosoftIdentityType
}

// SignInFromRefreshToken reads oid / tid / email from the sign-in's id_token.
func SignInFromRefreshToken(refreshToken string) (SignIn, error) {
	tok, err := signInTokenFn(refreshToken)
	if err != nil {
		return SignIn{}, err
	}
	claims, err := outlook.IdentityClaimsFromTokens(tok.IDToken, tok.AccessToken)
	if err != nil {
		return SignIn{}, err
	}
	return SignIn{
		ObjectID:     claims.ObjectID,
		HomeTenantID: claims.TenantID,
		Email:        claims.Email,
		IdentityType: outlook.IdentityTypeForTenant(claims.TenantID),
	}, nil
}

// HomeToken redeems the refresh token at /common (the home tenant). Only for validating a newly
// supplied refresh token and reading its granted scopes; tenant data access goes through Resolve.
func HomeToken(refreshToken string) (*outlook.TokenResponse, error) {
	return homeTokenFn(refreshToken)
}

// RefreshTokenReachesTenant reports whether the delegated refresh token can mint a token for tenantID.
func RefreshTokenReachesTenant(refreshToken, tenantID string) bool {
	tenantID = normalize(tenantID)
	if strings.TrimSpace(refreshToken) == "" || tenantID == "" {
		return false
	}
	tok, err := delegatedTokenFn(refreshToken, authorityFor(tenantID), "")
	if err != nil {
		return false
	}
	if outlook.IsMSATenant(tenantID) {
		return true
	}
	tid, err := outlook.TenantIDFromAccessToken(tok.AccessToken)
	return err == nil && normalize(tid) == tenantID
}

// UpsertCredentialFromSignIn stores the sign-in as the user's credential keyed by its home oid.
// accountID (MICROSOFT_ACCOUNT_ID) must match the token's oid.
func UpsertCredentialFromSignIn(database *db.PostgresDb, userID, accountID, projectID, refreshToken string) (*repo.GoogleBackupCredentialDB, error) {
	in, err := SignInFromRefreshToken(refreshToken)
	if err != nil {
		return nil, codeError(CodeSigninRequired, "Microsoft sign-in could not be verified: "+err.Error())
	}
	if a := normalize(accountID); a != "" && a != in.ObjectID {
		return nil, codeError(CodeCredentialForbidden, "MICROSOFT_ACCOUNT_ID does not match the signed-in Microsoft account")
	}
	// The sign-in's identity decides the stored label: personal only for consumer (MSA) accounts.
	// Admin/organization state is per tenant and comes from the access state, never from here.
	accountType := outlook.AccountTypeWorkAccount
	if in.IdentityType == outlook.MicrosoftIdentityPersonal {
		accountType = outlook.AccountTypePersonal
	}
	return database.CredentialRepo.UpsertMicrosoftAccount(repo.MicrosoftAccountUpsert{
		UserID:            userID,
		StorjProjectID:    projectID,
		ExternalAccountID: in.ObjectID,
		HomeTenantID:      in.HomeTenantID,
		Email:             in.Email,
		AccountType:       accountType,
		RefreshToken:      refreshToken,
	})
}

// AccountTenants is the GET /microsoft/accounts/tenants response.
type AccountTenants struct {
	CredentialID      uint          `json:"backup_credential_id"`
	ExternalAccountID string        `json:"external_account_id"`
	Email             string        `json:"email"`
	HomeTenantID      string        `json:"home_tenant_id"`
	IdentityType      string        `json:"identity_type"`
	DiscoveryStatus   string        `json:"discovery_status"`
	DiscoveryError    string        `json:"discovery_error,omitempty"`
	Tenants           []AccessState `json:"tenants"`
}

// RefreshAccountTenants discovers the tenants a sign-in can reach, checks a tenant-scoped token and
// the roles in each, and upserts the links. The home link always exists. Discovery failures keep
// the known links and report discovery_status instead of shrinking the list.
// When force is false, tenants probed within tenantAccessCheckTTL are not re-probed (still listed).
func RefreshAccountTenants(ctx context.Context, database *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, refreshToken string, force bool) (*AccountTenants, error) {
	refresh := strings.TrimSpace(refreshToken)
	if refresh == "" {
		refresh = strings.TrimSpace(cred.RefreshToken)
	}
	home := cred.HomeTenantID()
	if home == "" {
		return nil, codeError(CodeTenantNotLinked, "credential has no home tenant")
	}
	out := &AccountTenants{
		CredentialID:      cred.ID,
		ExternalAccountID: cred.ExternalAccountID,
		Email:             cred.Email,
		HomeTenantID:      home,
		IdentityType:      string(outlook.IdentityTypeForTenant(home)),
	}

	type found struct {
		name, domain string
	}
	tenants := map[string]found{home: {name: cred.TenantName}}
	order := []string{home}
	if outlook.IsMSATenant(home) {
		out.DiscoveryStatus = repo.MicrosoftDiscoveryComplete
	} else {
		res := discoverTenantsFn(ctx, refresh, home)
		out.DiscoveryStatus, out.DiscoveryError = res.Status, res.Error
		for _, t := range res.Tenants {
			if _, ok := tenants[t.TenantID]; !ok {
				order = append(order, t.TenantID)
			}
			tenants[t.TenantID] = found{name: t.DisplayName, domain: t.DefaultDomain}
		}
	}

	type probe struct {
		tid string
	}
	var toProbe []probe
	for _, tid := range order {
		f := tenants[tid]
		existing, _ := database.MicrosoftLinkRepo.Get(cred.ID, tid)
		d := repo.MicrosoftTenantDiscovery{
			TenantID: tid, TenantName: f.name, DefaultDomain: f.domain,
			Category: CategoryFor(home, tid, ""), HomeTenantID: home,
		}
		if tid == home {
			d.DiscoveryStatus = out.DiscoveryStatus
		}
		if existing != nil && strings.TrimSpace(existing.UserType) != "" {
			d.UserType = existing.UserType
			d.Category = CategoryFor(home, tid, existing.UserType)
		}
		if existing != nil {
			if oid := existing.ObjectIDValue(); oid != "" {
				d.ObjectID = oid
			}
		}
		if _, err := database.MicrosoftLinkRepo.UpsertDiscovered(cred.ID, d); err != nil {
			return nil, err
		}
		if force || !tenantAccessCheckFresh(existing) {
			toProbe = append(toProbe, probe{tid: tid})
		}
	}

	if len(toProbe) > 0 {
		workers := tenantAccessCheckConcurrency
		if workers > len(toProbe) {
			workers = len(toProbe)
		}
		jobs := make(chan string, len(toProbe))
		for _, p := range toProbe {
			jobs <- p.tid
		}
		close(jobs)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for tid := range jobs {
					CheckTenantAccess(ctx, database, cred, tid, refresh)
				}
			}()
		}
		wg.Wait()
	}

	states, err := AccessStates(database, cred)
	if err != nil {
		return nil, err
	}
	out.Tenants = states
	return out, nil
}

func tenantAccessCheckFresh(link *repo.MicrosoftAccountTenantDB) bool {
	if link == nil || link.CheckedAt == nil {
		return false
	}
	if time.Since(*link.CheckedAt) > tenantAccessCheckTTL {
		return false
	}
	switch link.TokenStatus {
	case repo.MicrosoftStatusWorking, repo.MicrosoftStatusError, repo.MicrosoftStatusSigninRequired:
		return true
	default:
		return false
	}
}

// CheckTenantAccess mints a delegated token at the tenant authority and re-reads the person's object
// and roles there. Failures are recorded on the link (token_status / role_status unknown); they
// never make the person an admin.
func CheckTenantAccess(ctx context.Context, database *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, tenantID, refreshToken string) {
	tenantID = normalize(tenantID)
	refresh := strings.TrimSpace(refreshToken)
	if refresh == "" {
		refresh = strings.TrimSpace(cred.RefreshToken)
	}
	links := database.MicrosoftLinkRepo
	if refresh == "" {
		_ = links.SaveTokenStatus(cred.ID, tenantID, repo.MicrosoftStatusSigninRequired, "refresh token missing")
		_ = links.SaveRoles(cred.ID, tenantID, nil, repo.MicrosoftStatusUnknown, false)
		return
	}
	tok, err := delegatedTokenFn(refresh, authorityFor(tenantID), "")
	if err != nil {
		_ = delegatedTokenError(database, cred.ID, tenantID, err)
		_ = links.SaveRoles(cred.ID, tenantID, nil, repo.MicrosoftStatusUnknown, false)
		return
	}
	if !outlook.IsMSATenant(tenantID) {
		if tid, terr := outlook.TenantIDFromAccessToken(tok.AccessToken); terr != nil || normalize(tid) != tenantID {
			_ = links.SaveTokenStatus(cred.ID, tenantID, repo.MicrosoftStatusError, "token was issued for another tenant")
			_ = links.SaveRoles(cred.ID, tenantID, nil, repo.MicrosoftStatusUnknown, false)
			return
		}
	}
	_ = links.SaveTokenStatus(cred.ID, tenantID, repo.MicrosoftStatusWorking, "")
	if t, _ := database.MicrosoftTenantRepo.Get(tenantID); t != nil && t.Availability != repo.MicrosoftTenantAvailable {
		_ = database.MicrosoftTenantRepo.SetAvailability(tenantID, repo.MicrosoftTenantAvailable)
	}

	if outlook.IsMSATenant(tenantID) {
		_ = links.SaveRoles(cred.ID, tenantID, nil, repo.MicrosoftStatusKnown, false)
		return
	}
	objectID := ""
	userType := ""
	if existing, _ := links.Get(cred.ID, tenantID); existing != nil {
		objectID = existing.ObjectIDValue()
		userType = strings.TrimSpace(existing.UserType)
	}
	if objectID == "" || userType == "" {
		me, merr := meIdentityFn(ctx, tok.AccessToken)
		if merr == nil && me.ObjectID != "" {
			objectID = me.ObjectID
			if me.UserType != "" {
				userType = me.UserType
			}
			home := cred.HomeTenantID()
			_, _ = links.UpsertDiscovered(cred.ID, repo.MicrosoftTenantDiscovery{
				TenantID: tenantID, ObjectID: me.ObjectID, UserType: me.UserType,
				Category: CategoryFor(home, tenantID, me.UserType), HomeTenantID: home,
			})
		}
	}
	roles := readRolesFn(ctx, tok.AccessToken, tok.IDToken, objectID)
	stored := make([]repo.MicrosoftRoleAssignment, 0, len(roles.Roles))
	for _, r := range roles.Roles {
		stored = append(stored, repo.MicrosoftRoleAssignment{TemplateID: r.TemplateID, Name: r.Name, Scope: r.Scope})
	}
	status := repo.MicrosoftStatusUnknown
	if roles.Status == outlook.RoleStatusKnown {
		status = repo.MicrosoftStatusKnown
	}
	_ = links.SaveRoles(cred.ID, tenantID, stored, status, roles.IsTenantWideAdmin())
}

// CategoryFor classifies tenantID for a sign-in whose home tenant is home. userType is the Graph
// userType in that tenant; when unknown ("") a foreign tenant is a guest.
func CategoryFor(home, tenantID, userType string) string {
	switch {
	case outlook.IsMSATenant(tenantID):
		return repo.MicrosoftTenantCategoryPersonal
	case tenantID == home:
		return repo.MicrosoftTenantCategoryHome
	case strings.EqualFold(userType, "Member"):
		return repo.MicrosoftTenantCategoryExternalMember
	default:
		return repo.MicrosoftTenantCategoryGuest
	}
}

// ConsentState is the tenant consent block of an access state.
type ConsentState struct {
	Status              string     `json:"status"`
	ServicePrincipal    bool       `json:"service_principal"`
	Effective           bool       `json:"effective"`
	ConsentedBy         string     `json:"consented_by,omitempty"`
	ConsentedAt         *time.Time `json:"consented_at,omitempty"`
	GrantedRoles        []string   `json:"granted_roles"`
	LastError           string     `json:"last_error,omitempty"`
	CapabilityCheckedAt *time.Time `json:"capability_checked_at,omitempty"`
}

// AccessState is what one sign-in can do in one tenant. Role, token and connection come from the
// link; consent, capabilities and availability come from the tenant. account_type is only a
// derived summary.
type AccessState struct {
	TenantID         string                                   `json:"tenant_id"`
	TenantName       string                                   `json:"tenant_name"`
	DefaultDomain    string                                   `json:"default_domain,omitempty"`
	Category         string                                   `json:"category"`
	IsHome           bool                                     `json:"is_home"`
	IdentityType     string                                   `json:"identity_type"`
	UserType         string                                   `json:"user_type,omitempty"`
	ObjectID         string                                   `json:"object_id,omitempty"`
	Roles            []repo.MicrosoftRoleAssignment           `json:"roles"`
	IsAdmin          bool                                     `json:"is_admin"`
	RoleStatus       string                                   `json:"role_status"`
	TokenStatus      string                                   `json:"token_status"`
	TokenError       string                                   `json:"token_error,omitempty"`
	ConnectionState  string                                   `json:"connection_state"`
	BackupMode       string                                   `json:"backup_mode"`
	AuthMode         string                                   `json:"auth_mode"`
	DiscoveryStatus  string                                   `json:"discovery_status,omitempty"`
	Availability     string                                   `json:"availability"`
	Consent          ConsentState                             `json:"consent"`
	Capabilities     map[string]bool                          `json:"capabilities"`
	CapabilityErrors map[string]repo.MicrosoftCapabilityError `json:"capability_errors"`
	// CanPersonalBackup: a delegated token works in this tenant.
	CanPersonalBackup bool `json:"can_personal_backup"`
	// CanOrganizationBackup: consent is effective and the tenant can list users.
	CanOrganizationBackup bool       `json:"can_organization_backup"`
	AccountType           string     `json:"account_type"`
	CheckedAt             *time.Time `json:"checked_at,omitempty"`
}

// BuildAccessState combines a link with its tenant row (tenant may be nil).
func BuildAccessState(link *repo.MicrosoftAccountTenantDB, tenant *repo.MicrosoftTenantDB) AccessState {
	s := AccessState{
		TenantID:         link.TenantID,
		TenantName:       link.TenantName,
		DefaultDomain:    link.DefaultDomain,
		Category:         link.Category,
		IsHome:           link.Category == repo.MicrosoftTenantCategoryHome || link.Category == repo.MicrosoftTenantCategoryPersonal,
		IdentityType:     string(outlook.IdentityTypeForTenant(link.HomeTenantID)),
		UserType:         link.UserType,
		ObjectID:         link.ObjectIDValue(),
		Roles:            link.RoleList(),
		IsAdmin:          link.IsAdmin && link.RoleStatus == repo.MicrosoftStatusKnown,
		RoleStatus:       link.RoleStatus,
		TokenStatus:      link.TokenStatus,
		TokenError:       link.TokenError,
		ConnectionState:  link.ConnectionState,
		BackupMode:       link.BackupMode,
		AuthMode:         link.AuthMode,
		DiscoveryStatus:  link.DiscoveryStatus,
		Availability:     repo.MicrosoftTenantAvailable,
		Consent:          ConsentState{Status: repo.MicrosoftConsentNotRequested, GrantedRoles: []string{}},
		Capabilities:     map[string]bool{},
		CapabilityErrors: map[string]repo.MicrosoftCapabilityError{},
		CheckedAt:        link.CheckedAt,
	}
	for _, name := range outlook.CapabilityOrder {
		s.Capabilities[name] = false
	}
	s.CanPersonalBackup = link.TokenStatus == repo.MicrosoftStatusWorking
	if tenant != nil {
		if s.TenantName == "" {
			s.TenantName = tenant.TenantName
		}
		if tenant.Availability != "" {
			s.Availability = tenant.Availability
		}
		effective := ConsentEffective(tenant)
		s.Consent = ConsentState{
			Status:              tenant.ConsentStatus,
			ServicePrincipal:    strings.TrimSpace(tenant.ServicePrincipalID) != "",
			Effective:           effective,
			ConsentedBy:         tenant.ConsentedBy,
			ConsentedAt:         tenant.ConsentedAt,
			GrantedRoles:        tenant.GrantedRoleList(),
			LastError:           tenant.LastError,
			CapabilityCheckedAt: tenant.LastProbeAt,
		}
		for name, v := range tenant.CapabilityMap() {
			s.Capabilities[name] = v && effective
		}
		s.CapabilityErrors = tenant.CapabilityErrorMap()
		s.CanOrganizationBackup = effective && s.Availability == repo.MicrosoftTenantAvailable && tenant.Capability(outlook.CapabilityListUsers)
	}
	if s.Availability != repo.MicrosoftTenantAvailable {
		s.CanPersonalBackup = false
	}
	switch {
	case outlook.IsMSATenant(link.TenantID):
		s.AccountType = outlook.AccountTypePersonal
	case s.CanOrganizationBackup:
		s.AccountType = outlook.AccountTypeAdminWorkspace
	default:
		s.AccountType = outlook.AccountTypeWorkAccount
	}
	return s
}

// AccessStates returns the access state of every link of a credential (home first).
func AccessStates(database *db.PostgresDb, cred *repo.GoogleBackupCredentialDB) ([]AccessState, error) {
	links, err := database.MicrosoftLinkRepo.ListByCredential(cred.ID)
	if err != nil {
		return nil, err
	}
	out := make([]AccessState, 0, len(links))
	for i := range links {
		var tenant *repo.MicrosoftTenantDB
		if !outlook.IsMSATenant(links[i].TenantID) {
			if tenant, err = database.MicrosoftTenantRepo.Get(links[i].TenantID); err != nil {
				return nil, err
			}
		}
		out = append(out, BuildAccessState(&links[i], tenant))
	}
	return out, nil
}

// TenantAccessState returns one link's access state.
func TenantAccessState(database *db.PostgresDb, credentialID uint, tenantID string) (*AccessState, error) {
	state, _, err := TenantAccessStateWithTenant(database, credentialID, tenantID)
	return state, err
}

// TenantAccessStateWithTenant is TenantAccessState plus the tenant row it was built from (nil for
// personal accounts or a tenant without a row).
func TenantAccessStateWithTenant(database *db.PostgresDb, credentialID uint, tenantID string) (*AccessState, *repo.MicrosoftTenantDB, error) {
	link, err := database.MicrosoftLinkRepo.Get(credentialID, tenantID)
	if err != nil {
		return nil, nil, err
	}
	if link == nil {
		return nil, nil, codeError(CodeTenantNotLinked, "this Microsoft account is not linked to tenant "+normalize(tenantID))
	}
	var tenant *repo.MicrosoftTenantDB
	if !outlook.IsMSATenant(link.TenantID) {
		if tenant, err = database.MicrosoftTenantRepo.Get(link.TenantID); err != nil {
			return nil, nil, err
		}
	}
	s := BuildAccessState(link, tenant)
	return &s, tenant, nil
}

// Connect connects the link with the user's backup mode. Organization mode authenticates with the
// tenant app-only token; personal mode with the sign-in's delegated token. Consent is checked when
// the link is used, not here, so admins can connect before consent completes.
func Connect(database *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, tenantID, backupMode string) (*AccessState, error) {
	tenantID = normalize(tenantID)
	switch strings.ToLower(strings.TrimSpace(backupMode)) {
	case repo.MicrosoftBackupModePersonal:
		backupMode = repo.MicrosoftBackupModePersonal
	case repo.MicrosoftBackupModeOrganization:
		if outlook.IsMSATenant(tenantID) {
			return nil, newError(400, "invalid_backup_mode", "personal Microsoft accounts support personal backup only")
		}
		backupMode = repo.MicrosoftBackupModeOrganization
	default:
		return nil, newError(400, "invalid_backup_mode", fmt.Sprintf("unsupported backup_mode %q (personal or organization)", backupMode))
	}
	link, err := database.MicrosoftLinkRepo.Get(cred.ID, tenantID)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, codeError(CodeTenantNotLinked, "this Microsoft account is not linked to tenant "+tenantID+"; list tenants first")
	}
	if err := database.MicrosoftLinkRepo.Connect(cred.ID, tenantID, backupMode, AuthModeFor(backupMode)); err != nil {
		return nil, err
	}
	return TenantAccessState(database, cred.ID, tenantID)
}

// AuthModeFor is the auth mode of a link connected in backupMode: organization backup uses the
// tenant app-only token, personal backup the sign-in's delegated token.
func AuthModeFor(backupMode string) string {
	if backupMode == repo.MicrosoftBackupModeOrganization {
		return repo.MicrosoftAuthModeApplication
	}
	return repo.MicrosoftAuthModeDelegated
}

// Disconnect disconnects the link and pauses this sign-in's jobs in the tenant. Backups are kept.
func Disconnect(database *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, tenantID string) (*AccessState, int, error) {
	tenantID = normalize(tenantID)
	link, err := database.MicrosoftLinkRepo.Get(cred.ID, tenantID)
	if err != nil {
		return nil, 0, err
	}
	if link == nil {
		return nil, 0, codeError(CodeTenantNotLinked, "this Microsoft account is not linked to tenant "+tenantID)
	}
	if err := database.MicrosoftLinkRepo.Disconnect(cred.ID, tenantID); err != nil {
		return nil, 0, err
	}
	paused := 0
	if database.CronJobRepo != nil {
		jobs, err := database.CronJobRepo.ListMicrosoftJobsByTenant(cred.ID, tenantID)
		if err != nil {
			return nil, 0, err
		}
		ids := make([]uint, 0, len(jobs))
		for _, j := range jobs {
			if j.Active {
				ids = append(ids, j.ID)
			}
		}
		if err := database.CronJobRepo.PauseJobs(ids, "Paused: Microsoft tenant disconnected; backups are kept"); err != nil {
			return nil, 0, err
		}
		paused = len(ids)
	}
	state, err := TenantAccessState(database, cred.ID, tenantID)
	return state, paused, err
}

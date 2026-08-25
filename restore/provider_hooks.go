package restore

import (
	"context"
	"fmt"
	"sync"

	"github.com/StorX2-0/Backup-Tools/db"
)

var (
	registryMu sync.Mutex

	mintGoogleTokenFn    func(ctx context.Context, d *RestoreDeps) error
	refreshGoogleTokenFn func(ctx context.Context, d *RestoreDeps) error

	mintMicrosoftTokenFn    func(ctx context.Context, d *RestoreDeps) error
	refreshMicrosoftTokenFn func(ctx context.Context, d *RestoreDeps) error
	evaluateMicrosoftReadyFn func(
		ctx context.Context,
		store *db.PostgresDb,
		out *ReadinessResult,
		userID, projectID, loginID, service, method, targetEmail string,
	) (*ReadinessResult, error)
)

// RegisterProcessor adds a restore-all processor (called from google/microsoft init).
func RegisterProcessor(p Processor) {
	if p == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	Registry[p.Method()] = p
}

// RegisterGoogleAuth wires Google token mint/refresh from restore/google.
func RegisterGoogleAuth(
	mint func(ctx context.Context, d *RestoreDeps) error,
	refresh func(ctx context.Context, d *RestoreDeps) error,
) {
	registryMu.Lock()
	defer registryMu.Unlock()
	mintGoogleTokenFn = mint
	refreshGoogleTokenFn = refresh
}

// RegisterMicrosoftAuth wires Microsoft token mint/refresh from restore/microsoft.
func RegisterMicrosoftAuth(
	mint func(ctx context.Context, d *RestoreDeps) error,
	refresh func(ctx context.Context, d *RestoreDeps) error,
) {
	registryMu.Lock()
	defer registryMu.Unlock()
	mintMicrosoftTokenFn = mint
	refreshMicrosoftTokenFn = refresh
}

// RegisterMicrosoftReadiness wires Microsoft prepare checks from restore/microsoft.
func RegisterMicrosoftReadiness(fn func(
	ctx context.Context,
	store *db.PostgresDb,
	out *ReadinessResult,
	userID, projectID, loginID, service, method, targetEmail string,
) (*ReadinessResult, error)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	evaluateMicrosoftReadyFn = fn
}

func (d *RestoreDeps) mintMicrosoftAccessToken(ctx context.Context) error {
	if mintMicrosoftTokenFn == nil {
		return fmt.Errorf("microsoft restore auth not registered")
	}
	return mintMicrosoftTokenFn(ctx, d)
}

// RefreshMicrosoftAccessToken forces a new Graph access token (401 retry path).
func (d *RestoreDeps) RefreshMicrosoftAccessToken(ctx context.Context) error {
	if refreshMicrosoftTokenFn == nil {
		return fmt.Errorf("microsoft restore auth not registered")
	}
	return refreshMicrosoftTokenFn(ctx, d)
}

func evaluateMicrosoftReadiness(
	ctx context.Context,
	store *db.PostgresDb,
	out *ReadinessResult,
	userID, projectID, loginID, service, method, targetEmail string,
) (*ReadinessResult, error) {
	if evaluateMicrosoftReadyFn == nil {
		return nil, fmt.Errorf("microsoft restore readiness not registered")
	}
	return evaluateMicrosoftReadyFn(ctx, store, out, userID, projectID, loginID, service, method, targetEmail)
}

func (d *RestoreDeps) mintGoogleAccessToken(ctx context.Context) error {
	if mintGoogleTokenFn == nil {
		return fmt.Errorf("google restore auth not registered")
	}
	return mintGoogleTokenFn(ctx, d)
}

// RefreshGoogleAccessToken forces a new Google access token (401 retry path).
func (d *RestoreDeps) RefreshGoogleAccessToken(ctx context.Context) error {
	if refreshGoogleTokenFn == nil {
		return fmt.Errorf("google restore auth not registered")
	}
	return refreshGoogleTokenFn(ctx, d)
}

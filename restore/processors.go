package restore

import (
	"context"
	"fmt"
)

// Processor restores one object key for a service.
type Processor interface {
	Method() string
	Config() ServiceConfig
	ShouldRestoreKey(objectKey string) bool
	Setup(ctx context.Context, deps *RestoreDeps) error
	RestoreKey(ctx context.Context, deps *RestoreDeps, objectKey string) error
	Cleanup(ctx context.Context, deps *RestoreDeps) error
}

// Registry maps cron method names to processors (filled by provider package init via RegisterProcessor).
var Registry = map[string]Processor{}

func ProcessorForMethod(method string) (Processor, error) {
	p, ok := Registry[method]
	if !ok {
		return nil, fmt.Errorf("unsupported restore method: %s", method)
	}
	return p, nil
}

package store

import (
	"context"
	"gorm.io/gorm"
)

// StartupHarnessOpen is compiled only into store.test, never a product binary.
func StartupHarnessOpen(ctx context.Context, path string, observer func(StageEvent), fault string, cancel context.CancelFunc) (*gorm.DB, error) {
	return openContext(ctx, path, observer, startupFaultConnector(fault, cancel, nil))
}

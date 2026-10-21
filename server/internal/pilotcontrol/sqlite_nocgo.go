//go:build !cgo

package pilotcontrol

import (
	"context"
	"errors"
)

// ErrCGODisabled indicates that the protected Pilot ledger is unavailable in this build.
var ErrCGODisabled = errors.New("pilot control ledger requires a CGO-enabled SQLite build")

// Store is an unavailable placeholder so non-CGO builds compile while failing closed.
type Store struct{}

// Open is unavailable when CGO is disabled; Pilot admission must remain off.
func Open(context.Context, string, int) (*Store, error) { return nil, ErrCGODisabled }

func (*Store) Close() error { return nil }

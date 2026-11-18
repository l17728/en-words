//go:build cgo && pilot_e2e

package pilotsupervisor

import (
	"errors"

	"github.com/mattn/go-sqlite3"
)

func runRootLedgerNativeSQLitePrimaryCode(err error) (int, bool) {
	var value sqlite3.Error
	if errors.As(err, &value) {
		return int(value.Code), true
	}
	var pointer *sqlite3.Error
	if errors.As(err, &pointer) && pointer != nil {
		return int(pointer.Code), true
	}
	return 0, false
}

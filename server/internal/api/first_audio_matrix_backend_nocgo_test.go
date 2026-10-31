//go:build !cgo

package api

import "errors"

func newFirstAudioGuardedBackend() (firstAudioBackend, error) {
	return nil, &firstAudioBackendFailure{stage: "cgo_disabled", err: errors.New("isolated first-audio SQLite backend requires cgo")}
}

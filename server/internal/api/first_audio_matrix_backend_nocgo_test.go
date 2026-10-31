//go:build !cgo

package api

import "errors"

func newFirstAudioGuardedBackend() (firstAudioBackend, error) {
	return nil, errors.New("isolated first-audio SQLite backend requires cgo")
}

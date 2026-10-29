//go:build cgo && !windows

package api

import "os"

func replaceFirstAudioResetManifest(source, target string) error {
	return os.Rename(source, target)
}

//go:build !windows

package speechagent

import "os"

func secureSpeechAgentPath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

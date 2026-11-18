package api

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
)

var firstAudioLoginUsernameRunes = regexp.MustCompile(`[^a-z0-9-]`)

// firstAudioSyntheticLogin derives browser-only fixture credentials from the
// per-run capability. The password is never returned by the JSONL protocol or
// written to artifacts; fixture setup persists only auth.Service's salted hash.
func firstAudioSyntheticLogin(capability []byte, runID string) (username, password string, err error) {
	if len(capability) < 32 || !firstAudioRunIDPattern.MatchString(runID) {
		return "", "", errors.New("invalid synthetic login identity")
	}
	suffix := firstAudioLoginUsernameRunes.ReplaceAllString(strings.ToLower(runID), "-")
	if len(suffix) > 32 {
		suffix = suffix[:32]
	}
	material := make([]byte, 0, len(capability)+len("\x00first_audio_login"))
	material = append(material, capability...)
	material = append(material, 0)
	material = append(material, "first_audio_login"...)
	digest := sha256.Sum256(material)
	return "first-audio-" + suffix, base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

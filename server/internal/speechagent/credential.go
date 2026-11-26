// Package speechagent contains the authenticated protocol shared by the TTS
// server endpoints and the resident worker.
package speechagent

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Capability is the immutable TTS permission tuple for one model service.
type Capability struct {
	ServiceID string `json:"service_id"`
	Model     string `json:"model"`
	Language  string `json:"language"`
	Voice     string `json:"voice"`
}

// NormalizeCapabilities validates, sorts, and deduplicates a capability set.
func NormalizeCapabilities(in []Capability) ([]Capability, error) {
	if len(in) == 0 || len(in) > 32 {
		return nil, errors.New("invalid speech agent capabilities")
	}
	seen := make(map[string]bool, len(in))
	out := make([]Capability, 0, len(in))
	for _, c := range in {
		c.ServiceID = strings.TrimSpace(c.ServiceID)
		c.Model = strings.TrimSpace(c.Model)
		c.Voice = strings.TrimSpace(c.Voice)
		if c.Language != "en" && c.Language != "zh" || !safeIdentifier(c.ServiceID, 64) || !safeIdentifier(c.Model, 128) || c.Voice == "" || len(c.Voice) > 64 {
			return nil, errors.New("invalid speech agent capabilities")
		}
		key := c.ServiceID + "\x00" + c.Model + "\x00" + c.Language + "\x00" + c.Voice
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	sortCapabilities(out)
	return out, nil
}

func safeIdentifier(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

func sortCapabilities(caps []Capability) {
	sort.Slice(caps, func(i, j int) bool {
		a, b := caps[i], caps[j]
		if a.ServiceID != b.ServiceID {
			return a.ServiceID < b.ServiceID
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Language != b.Language {
			return a.Language < b.Language
		}
		return a.Voice < b.Voice
	})
}

// EncodeCapabilities stores a canonical validated JSON set.
func EncodeCapabilities(caps []Capability) (string, error) {
	normalized, err := NormalizeCapabilities(caps)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", errors.New("invalid speech agent capabilities")
	}
	return string(raw), nil
}

const tokenBytes = 32

// NewToken returns a 256-bit URL-safe credential. Callers must show it once
// and persist only TokenHash(token).
func NewToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("speech agent credential generation failed")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// TokenHash is safe to store in SQLite instead of the bearer token.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// VerifyToken checks fixed-size hexadecimal digests with constant-time
// comparison. Malformed and empty credentials fail closed.
func VerifyToken(token, expectedHash string) bool {
	if strings.TrimSpace(token) == "" || len(expectedHash) != sha256.Size*2 {
		return false
	}
	expected, err := hex.DecodeString(expectedHash)
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	actual := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(actual[:], expected) == 1
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"enwords/internal/localmodel"
	"enwords/internal/speechagent"
	"enwords/internal/store"
	"gorm.io/gorm/clause"
)

const speechRuleVersion = "speech-v1"

var speechCacheLocks sync.Map

func speechCacheRoot() string {
	root := strings.TrimSpace(os.Getenv("ENWORDS_TTS_CACHE_DIR"))
	if root == "" {
		return filepath.Join("data", "cache", "tts")
	}
	return root
}

func speechCacheAssetPath(key string) string { return filepath.Join(speechCacheRoot(), key+".wav") }

// speechCacheBlobPath publishes immutable content-addressed bytes. A DB asset
// row is the only pointer that makes a blob a cache hit.
func speechCacheBlobPath(key, digest string) string {
	return filepath.Join(speechCacheRoot(), key+"."+digest+".wav")
}

func validSpeechCachePath(key, digest, path string) bool {
	if filepath.Clean(path) == filepath.Clean(speechCacheAssetPath(key)) {
		return true // Read-only compatibility for assets created before blob paths.
	}
	return digest != "" && filepath.Clean(path) == filepath.Clean(speechCacheBlobPath(key, digest))
}

func (a *App) speechCacheKey(text, lang string) string {
	serviceID, model, voice := a.speechTTSSelection(lang)
	return speechCacheKeyWith(text, lang, serviceID, model, voice)
}

func speechCacheKeyWith(text, lang, serviceID, model, voice string) string {
	if serviceID == "" {
		serviceID = "tts"
		if lang == "en" {
			serviceID = "tts-en"
		}
	}
	if model == "" {
		model = "default"
	}
	if voice == "" {
		voice = "default"
	}
	material := strings.Join([]string{lang, text, serviceID, model, voice, speechRuleVersion}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func (a *App) speechTTSSelection(lang string) (serviceID, model, voice string) {
	serviceID, model, voice = "tts-en", "default", "default"
	if lang != "en" {
		serviceID = "tts"
	}
	if a.Local != nil {
		profile := a.Local.Profile()
		if lang == "en" && profile.TTSEn != "" {
			serviceID = profile.TTSEn
		} else if lang != "en" && profile.TTSZh != "" {
			serviceID = profile.TTSZh
		}
		if service, ok := a.Local.Services[serviceID]; ok && service.Model != "" {
			model = service.Model
			voice = "default"
		}
		return
	}
	if a.DB == nil {
		return
	}
	var profile store.ModelProfile
	if a.DB.Migrator().HasTable(&store.ModelProfile{}) && a.DB.Where("active = ?", true).Order("id ASC").First(&profile).Error == nil {
		if lang == "en" && profile.TTSEnService != "" {
			serviceID = profile.TTSEnService
		} else if lang != "en" && profile.TTSZhService != "" {
			serviceID = profile.TTSZhService
		}
	}
	var agents []store.SpeechAgent
	if a.DB.Migrator().HasTable(&store.SpeechAgent{}) && a.DB.Where("enabled = ? AND revoked_at IS NULL", true).Find(&agents).Error == nil {
		if len(agents) == 0 {
			return
		}
		model, voice = "", ""
		models := map[string]bool{}
		voices := map[string]bool{}
		for _, registered := range agents {
			caps, err := speechagent.DecodeCapabilities(registered.CapabilitiesJSON)
			if err != nil {
				continue
			}
			for _, cap := range caps {
				if cap.ServiceID == serviceID && cap.Language == lang {
					models[cap.Model] = true
					voices[cap.Voice] = true
				}
			}
		}
		if len(models) == 1 {
			for selected := range models {
				model = selected
			}
		}
		if len(voices) == 1 {
			for selected := range voices {
				voice = selected
			}
		}
		if len(models) != 1 || len(voices) != 1 {
			model, voice = "", ""
		}
	}
	return
}

func (a *App) synthesizeCached(ctx context.Context, text, lang string) ([]byte, bool, error) {
	if a.Local == nil {
		return nil, false, errors.New("local TTS provider unavailable")
	}
	serviceID := a.Local.Profile().TTSEn
	if lang != "en" {
		serviceID = a.Local.Profile().TTSZh
	}
	if serviceID == "" {
		serviceID = "tts"
		if lang == "en" {
			serviceID = "tts-en"
		}
	}
	service := a.Local.Services[serviceID]
	key := a.speechCacheKey(text, lang)
	root := speechCacheRoot()
	if a.DB != nil {
		var asset store.SpeechAsset
		if a.DB.Where("cache_key = ? AND status = ?", key, "ready").First(&asset).Error == nil {
			if validSpeechCachePath(key, asset.SHA256, asset.FilePath) {
				if raw, err := os.ReadFile(asset.FilePath); err == nil {
					if validSpeechAudio(raw, asset.SizeBytes, asset.SHA256) {
						now := time.Now()
						a.DB.Model(&asset).Update("last_used_at", now)
						a.Log.InfoCtx(ctx, "speech", "speech_cache_hit", map[string]any{"cache_key": key, "language": lang, "bytes": len(raw)})
						return raw, true, nil
					}
				}
			}
			a.DB.Model(&asset).Where("status = ?", "ready").Update("status", "failed")
		}
	}
	lock, _ := speechCacheLocks.LoadOrStore(key, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	unlockFile, err := lockSpeechCache(ctx, root, key)
	if err != nil {
		return nil, false, err
	}
	defer unlockFile()
	return a.synthesizeSpeechCacheLocked(ctx, text, lang, serviceID, service, key, root)
}

func (a *App) synthesizeSpeechCacheLocked(ctx context.Context, text, lang, serviceID string, service localmodel.Service, key, root string) ([]byte, bool, error) {
	path := speechCacheAssetPath(key)
	if orphaned, _ := filepath.Glob(filepath.Join(root, "."+key+".tmp-*")); len(orphaned) > 0 {
		for _, stale := range orphaned {
			_ = os.Remove(stale)
		}
	}
	// Another process may have completed the same asset while this process
	// waited for the kernel lock. Re-read durable metadata before synthesizing.
	if a.DB != nil {
		var asset store.SpeechAsset
		if a.DB.Where("cache_key = ?", key).First(&asset).Error == nil {
			if validSpeechCachePath(key, asset.SHA256, asset.FilePath) {
				if asset.Status == "ready" {
					if raw, readErr := os.ReadFile(asset.FilePath); readErr == nil && validSpeechAudio(raw, asset.SizeBytes, asset.SHA256) {
						now := time.Now()
						a.DB.Model(&asset).Update("last_used_at", now)
						return raw, true, nil
					}
				}
				_ = os.Remove(asset.FilePath)
			}
			a.DB.Model(&asset).Where("status = ?", "ready").Update("status", "failed")
		}
	}
	if a.DB == nil {
		if raw, err := os.ReadFile(path); err == nil && validSpeechAudio(raw, 0, "") {
			return raw, true, nil
		}
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path) // Legacy unindexed cache is never trusted in persistent mode.
	}
	if a.Local == nil {
		return nil, false, errors.New("local TTS provider unavailable")
	}
	raw, err := a.Local.Synthesize(ctx, text, lang)
	if err != nil {
		return nil, false, err
	}
	if !validSpeechAudio(raw, 0, "") {
		return nil, false, errors.New("TTS provider returned invalid WAV audio")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return raw, false, nil
	}
	tmp, err := os.CreateTemp(root, "."+key+".tmp-*")
	if err != nil {
		return raw, false, nil
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return raw, false, nil
	}
	digest := audioSHA(raw)
	finalPath := path
	if a.DB != nil {
		finalPath = speechCacheBlobPath(key, digest)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// If a crash left an unindexed immutable blob with the exact digest we
		// just regenerated, it is safe to index only after this provider call.
		orphan, readErr := os.ReadFile(finalPath)
		if readErr != nil || !validSpeechAudio(orphan, int64(len(raw)), digest) || audioSHA(orphan) != digest {
			return raw, false, nil
		}
	}
	if a.DB != nil {
		asset := &store.SpeechAsset{CacheKey: key, Language: lang, SpeechText: text, Provider: serviceID, Model: service.Model, Voice: "default", RuleVersion: speechRuleVersion, FilePath: finalPath, SHA256: digest, SizeBytes: int64(len(raw)), Status: "ready", CreatedAt: time.Now(), LastUsedAt: time.Now()}
		_ = a.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "cache_key"}}, DoUpdates: clause.AssignmentColumns([]string{"language", "speech_text", "provider", "model", "voice", "rule_version", "file_path", "sha256", "size_bytes", "status", "last_used_at"})}).Create(asset).Error
	}
	a.Log.InfoCtx(ctx, "speech", "speech_cache_miss", map[string]any{"cache_key": key, "language": lang, "bytes": len(raw)})
	return raw, false, nil
}

func audioSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func validSpeechAudio(raw []byte, size int64, sha string) bool {
	if localmodel.ValidateWAV(raw) != nil {
		return false
	}
	if size > 0 && int64(len(raw)) != size {
		return false
	}
	return sha == "" || audioSHA(raw) == sha
}

func (a *App) refreshSpeechCache(ctx context.Context, text, lang string) ([]byte, error) {
	if a.Local == nil {
		return nil, errors.New("local TTS provider unavailable")
	}
	key := a.speechCacheKey(text, lang)
	root := speechCacheRoot()
	serviceID := a.Local.Profile().TTSEn
	if lang != "en" {
		serviceID = a.Local.Profile().TTSZh
	}
	if serviceID == "" {
		serviceID = "tts"
		if lang == "en" {
			serviceID = "tts-en"
		}
	}
	service := a.Local.Services[serviceID]
	lock, _ := speechCacheLocks.LoadOrStore(key, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	unlockFile, err := lockSpeechCache(ctx, root, key)
	if err != nil {
		return nil, err
	}
	defer unlockFile()
	if a.DB != nil {
		var assets []store.SpeechAsset
		a.DB.Where("cache_key = ?", key).Find(&assets)
		for _, asset := range assets {
			_ = os.Remove(asset.FilePath)
			_ = a.DB.Delete(&asset).Error
		}
		_ = a.DB.Model(&store.SpeechMaterialJob{}).Where("cache_key = ? AND status IN ?", key, []string{"pending", "running", "ready", "failed"}).Updates(map[string]any{"status": "canceled", "updated_at": time.Now()}).Error
	}
	_ = os.Remove(speechCacheAssetPath(key))
	raw, _, err := a.synthesizeSpeechCacheLocked(ctx, text, lang, serviceID, service, key, root)
	return raw, err
}

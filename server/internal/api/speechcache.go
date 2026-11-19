package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"enwords/internal/store"
)

const speechRuleVersion = "speech-v1"

var speechCacheLocks sync.Map

func (a *App) speechCacheKey(text, lang string) string {
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
	material := strings.Join([]string{lang, text, serviceID, service.Model, "default", speechRuleVersion}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func (a *App) synthesizeCached(ctx context.Context, text, lang string) ([]byte, bool, error) {
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
	root := filepath.Join("data", "cache", "tts")
	path := filepath.Join(root, key+".wav")
	if a.DB != nil {
		var asset store.SpeechAsset
		if a.DB.Where("cache_key = ? AND status = ?", key, "ready").First(&asset).Error == nil {
			if raw, err := os.ReadFile(asset.FilePath); err == nil {
				now := time.Now()
				a.DB.Model(&asset).Update("last_used_at", now)
				a.Log.InfoCtx(ctx, "speech", "speech_cache_hit", map[string]any{"cache_key": key, "language": lang, "bytes": len(raw)})
				return raw, true, nil
			}
		}
	}
	lock, _ := speechCacheLocks.LoadOrStore(key, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if raw, err := os.ReadFile(path); err == nil {
		if a.DB != nil {
			now := time.Now()
			asset := &store.SpeechAsset{CacheKey: key, Language: lang, SpeechText: text, Provider: serviceID, Model: service.Model, Voice: "default", RuleVersion: speechRuleVersion, FilePath: path, SHA256: audioSHA(raw), SizeBytes: int64(len(raw)), Status: "ready", CreatedAt: now, LastUsedAt: now}
			_ = a.DB.Where("cache_key = ?", key).FirstOrCreate(asset)
		}
		return raw, true, nil
	}
	raw, err := a.Local.Synthesize(ctx, text, lang)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return raw, false, nil
	}
	tmp := path + ".tmp-" + fmt.Sprint(time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o644); err == nil {
		_ = os.Rename(tmp, path)
	}
	if a.DB != nil {
		asset := &store.SpeechAsset{CacheKey: key, Language: lang, SpeechText: text, Provider: serviceID, Model: service.Model, Voice: "default", RuleVersion: speechRuleVersion, FilePath: path, SHA256: audioSHA(raw), SizeBytes: int64(len(raw)), Status: "ready", CreatedAt: time.Now(), LastUsedAt: time.Now()}
		_ = a.DB.Where("cache_key = ?", key).FirstOrCreate(asset)
	}
	a.Log.InfoCtx(ctx, "speech", "speech_cache_miss", map[string]any{"cache_key": key, "language": lang, "bytes": len(raw)})
	return raw, false, nil
}

func audioSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func (a *App) refreshSpeechCache(ctx context.Context, text, lang string) ([]byte, error) {
	if a.DB != nil {
		var assets []store.SpeechAsset
		a.DB.Where("speech_text = ? AND language = ?", text, lang).Find(&assets)
		for _, asset := range assets {
			_ = os.Remove(asset.FilePath)
			_ = a.DB.Delete(&asset).Error
		}
	}
	raw, _, err := a.synthesizeCached(ctx, text, lang)
	return raw, err
}

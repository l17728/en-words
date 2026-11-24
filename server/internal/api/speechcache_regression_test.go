package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/localmodel"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func minimalTestWAV() []byte {
	wav := make([]byte, 46)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 8000)
	binary.LittleEndian.PutUint32(wav[28:32], 16000)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], 2)
	wav[44], wav[45] = 0x01, 0x02
	return wav
}

func TestSpeechCacheRejectsTruncatedWAVContainer(t *testing.T) {
	if validSpeechAudio([]byte("RIFF\x04\x00\x00\x00WAVE"), 0, "") {
		t.Fatal("truncated RIFF header was accepted as a ready speech asset")
	}
	if !validSpeechAudio(minimalTestWAV(), int64(len(minimalTestWAV())), audioSHA(minimalTestWAV())) {
		t.Fatal("complete fixture WAV was rejected")
	}
}

func TestSpeechCacheSharesSingleSynthesisAcrossProcesses(t *testing.T) {
	if os.Getenv("ENWORDS_SPEECH_SYNTH_HELPER") == "1" {
		client, err := localmodel.New(os.Getenv("ENWORDS_SPEECH_SYNTH_CONSOLE"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Discover(context.Background()); err != nil {
			t.Fatal(err)
		}
		a := NewApp(scheduler.RealClock{}, nil)
		a.Local = client
		db, err := gorm.Open(sqlite.Open(os.Getenv("ENWORDS_SPEECH_SYNTH_DB")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		a.DB = db
		if _, _, err := a.synthesizeCached(context.Background(), "shared process fixture", "en"); err != nil {
			t.Fatal(err)
		}
		pool, _ := db.DB()
		_ = pool.Close()
		return
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, "shared-cache.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&store.SpeechAsset{}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var synthCalls atomic.Int32
	wav := minimalTestWAV()
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]any{
				{"id": "vl", "model": "vl-fixture", "capabilities": []string{"text"}},
				{"id": "asr", "model": "asr-fixture", "capabilities": []string{"asr"}},
				{"id": "tts", "model": "zh-fixture", "capabilities": []string{"tts"}},
				{"id": "tts-en", "model": "en-fixture", "capabilities": []string{"tts"}},
			}})
		case "/api/tts-en/speech":
			synthCalls.Add(1)
			time.Sleep(250 * time.Millisecond)
			_, _ = w.Write(wav)
		default:
			http.NotFound(w, r)
		}
	}))
	defer console.Close()
	runHelper := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSpeechCacheSharesSingleSynthesisAcrossProcesses$")
		cmd.Env = append(os.Environ(),
			"ENWORDS_SPEECH_SYNTH_HELPER=1",
			"ENWORDS_SPEECH_SYNTH_CONSOLE="+console.URL,
			"ENWORDS_SPEECH_SYNTH_DB="+dbPath,
			"ENWORDS_TTS_CACHE_DIR="+root,
		)
		return cmd
	}
	cmdA, cmdB := runHelper(), runHelper()
	var outputs [2][]byte
	var errors [2]error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); outputs[0], errors[0] = cmdA.Output() }()
	go func() { defer wait.Done(); outputs[1], errors[1] = cmdB.Output() }()
	wait.Wait()
	outA, errA := outputs[0], errors[0]
	outB, errB := outputs[1], errors[1]
	if errA != nil || errB != nil {
		t.Fatalf("cache synthesis helper failed: A=%v %s; B=%v %s", errA, outA, errB, outB)
	}
	if got := synthCalls.Load(); got != 1 {
		t.Fatalf("same cache key synthesized %d times across processes, want 1", got)
	}
	app := NewApp(scheduler.RealClock{}, nil)
	app.Local, _ = localmodel.New(console.URL, nil)
	if err := app.Local.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	key := app.speechCacheKey("shared process fixture", "en")
	var stored store.SpeechAsset
	if err := db.Where("cache_key = ?", key).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	asset, err := os.ReadFile(stored.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(filepath.Dir(stored.FilePath)) != filepath.Clean(root) || !validSpeechAudio(asset, int64(len(wav)), audioSHA(wav)) {
		t.Fatalf("shared cache file path or WAV bytes are invalid: %q", stored.FilePath)
	}
	if stored.Status != "ready" || stored.SHA256 != audioSHA(wav) || stored.SizeBytes != int64(len(wav)) {
		t.Fatalf("shared process asset metadata is inconsistent: %#v", stored)
	}
}

func TestSpeechCacheRegenerationReactivatesCorruptFailedAsset(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ENWORDS_TTS_CACHE_DIR", root)
	wav := minimalTestWAV()
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]any{
				{"id": "vl", "model": "vl-fixture", "capabilities": []string{"text"}},
				{"id": "asr", "model": "asr-fixture", "capabilities": []string{"asr"}},
				{"id": "tts", "model": "zh-fixture", "capabilities": []string{"tts"}},
				{"id": "tts-en", "model": "en-fixture", "capabilities": []string{"tts"}},
			}})
		case "/api/tts-en/speech":
			_, _ = w.Write(wav)
		default:
			http.NotFound(w, r)
		}
	}))
	defer console.Close()
	client, err := localmodel.New(console.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.SetProfile(localmodel.Profile{Name: "fixture", Text: "vl", ASR: "asr", TTSZh: "tts", TTSEn: "tts-en"}); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-cache.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	a := NewApp(scheduler.RealClock{}, nil)
	a.DB = db
	a.Local = client
	if err := db.AutoMigrate(&store.SpeechAsset{}); err != nil {
		t.Fatal(err)
	}
	key := a.speechCacheKey("retry fixture", "en")
	if err := db.Create(&store.SpeechAsset{CacheKey: key, Language: "en", SpeechText: "retry fixture", FilePath: filepath.Join(root, "missing.wav"), Status: "failed"}).Error; err != nil {
		t.Fatal(err)
	}
	item := speechMaterialItem{Key: key, Language: "en", Text: "retry fixture"}
	if a.speechMaterialCached(item) {
		t.Fatal("failed asset unexpectedly reported ready")
	}
	if _, _, err := a.synthesizeCached(context.Background(), item.Text, item.Language); err != nil {
		t.Fatalf("regenerate failed asset: %v", err)
	}
	if !a.speechMaterialCached(item) {
		t.Fatal("regenerated asset did not return to ready state")
	}
	var asset store.SpeechAsset
	if err := db.Where("cache_key = ?", key).First(&asset).Error; err != nil {
		t.Fatal(err)
	}
	if asset.Status != "ready" || asset.SHA256 != audioSHA(wav) || asset.SizeBytes != int64(len(wav)) {
		t.Fatalf("upsert left stale asset metadata: %#v", asset)
	}
}

func TestSpeechCacheUnavailableAndInvalidProviderOutput(t *testing.T) {
	t.Setenv("ENWORDS_TTS_CACHE_DIR", t.TempDir())
	a := NewApp(scheduler.RealClock{}, nil)
	if _, _, err := a.synthesizeCached(context.Background(), "cat", "en"); err == nil {
		t.Fatal("synthesis without a provider unexpectedly succeeded")
	}
	if _, err := a.refreshSpeechCache(context.Background(), "cat", "en"); err == nil {
		t.Fatal("refresh without a provider unexpectedly succeeded")
	}

	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]any{
				{"id": "vl", "model": "vl-fixture", "capabilities": []string{"text"}},
				{"id": "asr", "model": "asr-fixture", "capabilities": []string{"asr"}},
				{"id": "tts", "model": "zh-fixture", "capabilities": []string{"tts"}},
				{"id": "tts-en", "model": "en-fixture", "capabilities": []string{"tts"}},
			}})
			return
		}
		if r.URL.Path == "/api/tts-en/speech" {
			_, _ = w.Write([]byte("not-a-wave"))
			return
		}
		http.NotFound(w, r)
	}))
	defer console.Close()
	client, err := localmodel.New(console.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Local = client
	if _, _, err := a.synthesizeCached(context.Background(), "cat", "en"); err == nil || !strings.Contains(err.Error(), "invalid WAV") {
		t.Fatalf("invalid provider audio error = %v, want invalid WAV", err)
	}
}

func TestSpeechCacheRefreshReplacesExistingAsset(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ENWORDS_TTS_CACHE_DIR", root)
	var calls atomic.Int32
	wav := minimalTestWAV()
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]any{
				{"id": "vl", "model": "vl-fixture", "capabilities": []string{"text"}},
				{"id": "asr", "model": "asr-fixture", "capabilities": []string{"asr"}},
				{"id": "tts", "model": "zh-fixture", "capabilities": []string{"tts"}},
				{"id": "tts-en", "model": "en-fixture", "capabilities": []string{"tts"}},
			}})
			return
		}
		if r.URL.Path == "/api/tts-en/speech" {
			calls.Add(1)
			_, _ = w.Write(wav)
			return
		}
		http.NotFound(w, r)
	}))
	defer console.Close()
	client, err := localmodel.New(console.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := NewApp(scheduler.RealClock{}, nil)
	a.Local = client
	if _, hit, err := a.synthesizeCached(context.Background(), "refresh fixture", "en"); err != nil || hit {
		t.Fatalf("first synthesize = hit %v, err %v", hit, err)
	}
	if _, hit, err := a.synthesizeCached(context.Background(), "refresh fixture", "en"); err != nil || !hit {
		t.Fatalf("second synthesize = hit %v, err %v", hit, err)
	}
	if _, err := a.refreshSpeechCache(context.Background(), "refresh fixture", "en"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("TTS provider calls = %d, want initial generation plus forced refresh", got)
	}
}

func TestSpeechCacheNeverTreatsUnindexedBlobAsReady(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ENWORDS_TTS_CACHE_DIR", root)
	wav := minimalTestWAV()
	var calls atomic.Int32
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]any{
				{"id": "vl", "model": "vl-fixture", "capabilities": []string{"text"}},
				{"id": "asr", "model": "asr-fixture", "capabilities": []string{"asr"}},
				{"id": "tts", "model": "zh-fixture", "capabilities": []string{"tts"}},
				{"id": "tts-en", "model": "en-fixture", "capabilities": []string{"tts"}},
			}})
			return
		}
		if r.URL.Path == "/api/tts-en/speech" {
			calls.Add(1)
			_, _ = w.Write(wav)
			return
		}
		http.NotFound(w, r)
	}))
	defer console.Close()
	client, err := localmodel.New(console.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "orphan.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.AutoMigrate(&store.SpeechAsset{}); err != nil {
		t.Fatal(err)
	}
	a := NewApp(scheduler.RealClock{}, nil)
	a.DB = db
	a.Local = client
	key := a.speechCacheKey("orphan fixture", "en")
	orphan := speechCacheBlobPath(key, audioSHA(wav))
	if err := os.WriteFile(orphan, wav, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := a.synthesizeCached(context.Background(), "orphan fixture", "en"); err != nil || hit {
		t.Fatalf("orphan cache result = hit %v, err %v; want a provider call", hit, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("orphan blob was trusted without SQLite metadata; provider calls=%d", calls.Load())
	}
	var stored store.SpeechAsset
	if err := db.Where("cache_key = ?", key).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !validSpeechCachePath(key, stored.SHA256, stored.FilePath) || stored.Status != "ready" {
		t.Fatalf("regenerated orphan did not become a validated indexed asset: %#v", stored)
	}
	if err := os.WriteFile(stored.FilePath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := a.synthesizeCached(context.Background(), "orphan fixture", "en"); err != nil || hit {
		t.Fatalf("corrupt indexed asset should regenerate, hit=%v err=%v", hit, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("corrupt indexed asset did not trigger regeneration, calls=%d", calls.Load())
	}
	if err := db.Where("cache_key = ?", key).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "ready" || stored.SHA256 != audioSHA(wav) || !validSpeechCachePath(key, stored.SHA256, stored.FilePath) {
		t.Fatalf("corrupt indexed blob was not repaired: %#v", stored)
	}
}

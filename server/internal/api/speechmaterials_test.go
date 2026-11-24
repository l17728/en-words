package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enwords/internal/localmodel"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSpeechMaterialWorkerPersistsSuccessAndProviderFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		wantStatus string
	}{
		{name: "success", statusCode: http.StatusOK, wantStatus: "ready"},
		{name: "provider failure", statusCode: http.StatusBadGateway, wantStatus: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("ENWORDS_TTS_CACHE_DIR", root)
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
					if tc.statusCode != http.StatusOK {
						w.WriteHeader(tc.statusCode)
						return
					}
					_, _ = w.Write(minimalTestWAV())
					return
				}
				http.NotFound(w, r)
			}))
			defer console.Close()
			client, err := localmodel.New(console.URL, nil)
			require.NoError(t, err)
			require.NoError(t, client.Discover(context.Background()))
			db, err := gorm.Open(sqlite.Open(filepath.Join(root, "jobs.db")), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&store.SpeechAsset{}, &store.SpeechMaterialJob{}))
			pool, err := db.DB()
			require.NoError(t, err)
			defer pool.Close()
			app := NewApp(scheduler.RealClock{}, nil)
			app.Local = client
			app.DB = db
			job := &store.SpeechMaterialJob{CacheKey: "worker-" + tc.name, ManifestRevision: "revision", LessonID: 7, Language: "en", SpeechText: "cat", TraceID: "trace-worker", Status: "pending"}
			require.NoError(t, db.Create(job).Error)
			app.runSpeechMaterialJob(job.ID)
			var stored store.SpeechMaterialJob
			require.NoError(t, db.First(&stored, job.ID).Error)
			if stored.Status != tc.wantStatus || stored.Attempt != 1 {
				t.Fatalf("worker result = status %q attempt %d, want %q attempt 1", stored.Status, stored.Attempt, tc.wantStatus)
			}
			if tc.wantStatus == "failed" && (stored.ErrorCode != "synthesis_failed" || stored.ErrorSummary == "") {
				t.Fatalf("provider failure was not categorized: %#v", stored)
			}
		})
	}
}

func TestSpeechManifestRevisionStableAcrossInputOrder(t *testing.T) {
	a := []speechMaterialItem{{Key: "dog|zh", Language: "zh", Text: "dog"}, {Key: "cat|en", Language: "en", Text: "cat"}}
	b := []speechMaterialItem{{Key: "cat|en", Language: "en", Text: "cat"}, {Key: "dog|zh", Language: "zh", Text: "dog"}}
	if got, want := speechManifestRevision(a), speechManifestRevision(b); got != want {
		t.Fatalf("revision changed with ordering: %s != %s", got, want)
	}
}

func TestSpeechManifestRevisionChangesWithText(t *testing.T) {
	a := []speechMaterialItem{{Key: "cat|en", Language: "en", Text: "cat"}}
	b := []speechMaterialItem{{Key: "cat|en", Language: "en", Text: "cat."}}
	if speechManifestRevision(a) == speechManifestRevision(b) {
		t.Fatal("revision did not change")
	}
}

func TestLessonSpeechTextsMatchClassroomPlaybackNormalization(t *testing.T) {
	english, chinese := lessonSpeechTexts("sth.", []string{"n. 猫"})
	if english != "something" {
		t.Fatalf("English prewarm text = %q, want something", english)
	}
	if chinese != "猫" {
		t.Fatalf("Chinese prewarm text = %q, want 猫", chinese)
	}
	_, fallback := lessonSpeechTexts("cat", nil)
	if fallback != "cat" {
		t.Fatalf("missing meaning fallback = %q, want cat", fallback)
	}
}

func TestSpeechMaterialCacheHitIsScopedToProviderModelKey(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:speech-material-cache-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(scheduler.RealClock{}, nil)
	a.DB = db
	root := t.TempDir()
	t.Setenv("ENWORDS_TTS_CACHE_DIR", root)
	if err := a.DB.AutoMigrate(&store.SpeechAsset{}); err != nil {
		t.Fatal(err)
	}
	item := speechMaterialItem{Key: "profile-a-model-x", Language: "en", Text: "cat"}
	otherProfile := speechMaterialItem{Key: "profile-b-model-y", Language: "en", Text: "cat"}
	file := speechCacheAssetPath(otherProfile.Key)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := minimalTestWAV()
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&store.SpeechAsset{CacheKey: otherProfile.Key, SpeechText: item.Text, Language: item.Language, FilePath: file, SHA256: audioSHA(raw), SizeBytes: int64(len(raw)), Status: "ready", CreatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if a.speechMaterialCached(item) {
		t.Fatal("another profile's audio was incorrectly accepted as a cache hit")
	}
	if !a.speechMaterialCached(otherProfile) {
		t.Fatal("matching provider/model cache key should be ready")
	}
	if err := os.WriteFile(file, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a.speechMaterialCached(otherProfile) {
		t.Fatal("corrupt audio file was reported as ready")
	}
	var stored store.SpeechAsset
	if err := a.DB.Where("cache_key = ?", otherProfile.Key).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" {
		t.Fatalf("corrupt asset status = %q, want failed", stored.Status)
	}
}

func TestSpeechMaterialRetryResetsTerminalJobAndLease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:speech-material-retry-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(scheduler.RealClock{}, nil)
	a.DB = db
	if err := a.DB.AutoMigrate(&store.SpeechMaterialJob{}); err != nil {
		t.Fatal(err)
	}
	lease := time.Now().Add(time.Minute)
	job := &store.SpeechMaterialJob{CacheKey: "retry-key", ManifestRevision: "revision", Status: "failed", Attempt: 1, AttemptToken: "old-worker", LeaseUntil: &lease, ErrorCode: "synthesis_failed", ErrorSummary: "upstream unavailable"}
	if err := a.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	if !a.retrySpeechMaterialJob(job) {
		t.Fatal("failed material job was not reset for retry")
	}
	var stored store.SpeechMaterialJob
	if err := a.DB.First(&stored, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" || stored.AttemptToken != "" || stored.LeaseUntil != nil || stored.ErrorCode != "" || stored.ErrorSummary != "" {
		t.Fatalf("retry did not clear terminal state and stale lease: %#v", stored)
	}
}

func TestSpeechMaterialReadyJobIsRegeneratedWhenCacheIsMissing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:speech-material-ready-miss-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(scheduler.RealClock{}, nil)
	a.DB = db
	if err := a.DB.AutoMigrate(&store.SpeechMaterialJob{}); err != nil {
		t.Fatal(err)
	}
	job := &store.SpeechMaterialJob{CacheKey: "missing-file", ManifestRevision: "revision", Status: "ready", Attempt: 1}
	if err := a.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	if !a.retrySpeechMaterialJob(job) || job.Status != "pending" {
		t.Fatalf("ready job without a cache file was not made retryable: %#v", job)
	}
}

func TestSpeechMaterialProgressRecoveryReclaimsExpiredLease(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "speech-material-restart.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	firstApp := NewApp(scheduler.RealClock{}, nil)
	firstApp.DB = db
	if err := firstApp.DB.AutoMigrate(&store.SpeechMaterialJob{}); err != nil {
		t.Fatal(err)
	}
	lease := time.Now().UTC().Add(-time.Minute)
	job := &store.SpeechMaterialJob{CacheKey: "expired-key", ManifestRevision: "revision", LessonID: 99, Status: "running", Attempt: 1, AttemptToken: "dead-worker", LeaseUntil: &lease}
	if err := firstApp.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	restartedDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	restartedPool, err := restartedDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer restartedPool.Close()
	restartedApp := NewApp(scheduler.RealClock{}, nil)
	restartedApp.DB = restartedDB
	restartedApp.recoverExpiredSpeechMaterialJobs(job.LessonID, job.ManifestRevision)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var stored store.SpeechMaterialJob
		if err := restartedApp.DB.First(&stored, job.ID).Error; err == nil && stored.Status == "failed" && stored.Attempt == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var stored store.SpeechMaterialJob
	_ = restartedApp.DB.First(&stored, job.ID).Error
	t.Fatalf("expired lease was not reclaimed: %#v", stored)
}

func TestSpeechMaterialRecoveryDoesNotExpireFutureUTCLeaseInNonUTCZone(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-material-timezone.db")), &gorm.Config{})
	require.NoError(t, err)
	app := NewApp(scheduler.RealClock{}, nil)
	app.DB = db
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&store.SpeechMaterialJob{}))
	lease := time.Now().UTC().Add(2 * time.Minute)
	job := store.SpeechMaterialJob{CacheKey: "timezone-lease", ManifestRevision: "timezone-r1", LessonID: 91, Status: "running", Attempt: 1, AttemptToken: "valid-attempt", AgentID: "remote-agent", LeaseUntil: &lease, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	require.NoError(t, db.Create(&job).Error)
	app.recoverExpiredSpeechMaterialJobs(job.LessonID, job.ManifestRevision)
	var after store.SpeechMaterialJob
	require.NoError(t, db.First(&after, job.ID).Error)
	require.Equal(t, "running", after.Status, "UTC lease timestamps must not be compared lexically against local-time SQLite text")
	require.Equal(t, "valid-attempt", after.AttemptToken)
}

func TestSpeechMaterialFailureLogsRetainTraceAndSafeCategory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:speech-material-trace-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(scheduler.RealClock{}, nil)
	a.DB = db
	if err := a.DB.AutoMigrate(&store.SpeechMaterialJob{}); err != nil {
		t.Fatal(err)
	}
	job := &store.SpeechMaterialJob{CacheKey: "trace-key", ManifestRevision: "trace-revision", LessonID: 101, Language: "en", SpeechText: "secret fixture text", Status: "pending", TraceID: "trace-speech-123"}
	if err := a.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	a.SetLogger(obs.NewWithSink(&output))
	a.runSpeechMaterialJob(job.ID)
	log := output.String()
	for _, expected := range []string{"speech_material_job_started", "speech_material_failed", "trace-speech-123", "tts_unavailable"} {
		if !strings.Contains(log, expected) {
			t.Fatalf("speech logs missing %q: %s", expected, log)
		}
	}
	if strings.Contains(log, job.SpeechText) {
		t.Fatalf("speech log leaked input text: %s", log)
	}
}

func TestSpeechMaterialWorkerQueuedAtShutdownExitsWithoutClaim(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:speech-material-shutdown-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(scheduler.RealClock{}, nil)
	a.DB = db
	if err := a.DB.AutoMigrate(&store.SpeechMaterialJob{}); err != nil {
		t.Fatal(err)
	}
	job := &store.SpeechMaterialJob{CacheKey: "shutdown-key", ManifestRevision: "shutdown-revision", Status: "pending"}
	if err := a.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	a.speechMaterialSlots <- struct{}{}
	a.speechMaterialSlots <- struct{}{} // Keep the worker queued on admission until cancellation.
	a.startSpeechMaterialJob(job.ID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.StopSpeechMaterialJobs(ctx); err != nil {
		t.Fatalf("queued worker did not stop promptly: %v", err)
	}
	<-a.speechMaterialSlots
	<-a.speechMaterialSlots
	var stored store.SpeechMaterialJob
	if err := a.DB.First(&stored, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" || stored.Attempt != 0 {
		t.Fatalf("queued worker claimed a job after shutdown: %#v", stored)
	}
	a.startSpeechMaterialJob(job.ID)
	time.Sleep(20 * time.Millisecond)
	if err := a.DB.First(&stored, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Attempt != 0 {
		t.Fatalf("worker started after shutdown: %#v", stored)
	}
}

func TestSpeechCacheKernelLockSerializesAcrossProcesses(t *testing.T) {
	if os.Getenv("ENWORDS_SPEECH_LOCK_HELPER") == "1" {
		root := os.Getenv("ENWORDS_SPEECH_LOCK_ROOT")
		unlock, err := lockSpeechCache(context.Background(), root, "cross-process-fixture")
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if err := os.WriteFile(os.Getenv("ENWORDS_SPEECH_LOCK_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(os.Getenv("ENWORDS_SPEECH_LOCK_RELEASE")); err == nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("parent did not release lock helper")
	}

	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	release := filepath.Join(root, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSpeechCacheKernelLockSerializesAcrossProcesses$")
	cmd.Env = append(os.Environ(),
		"ENWORDS_SPEECH_LOCK_HELPER=1",
		"ENWORDS_SPEECH_LOCK_ROOT="+root,
		"ENWORDS_SPEECH_LOCK_READY="+ready,
		"ENWORDS_SPEECH_LOCK_RELEASE="+release,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(release, []byte("release"), 0o600)
		_ = cmd.Wait()
	}()
	readyDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(readyDeadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("helper never acquired cache lock: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if unlock, err := lockSpeechCache(ctx, root, "cross-process-fixture"); err == nil {
		unlock()
		t.Fatal("second process acquired the same cache key while owner held it")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting on held cache lock returned %v, want context deadline", err)
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(waitDeadline) {
		unlock, err := lockSpeechCache(context.Background(), root, "cross-process-fixture")
		if err == nil {
			unlock()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cache lock remained held after owner process exited")
}

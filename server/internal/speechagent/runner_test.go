package speechagent

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/localmodel"
	"github.com/stretchr/testify/require"
)

func TestRunnerRequiresVerifiedHTTPSAndLoopbackModelConsole(t *testing.T) {
	spool := t.TempDir()
	_, err := NewRunner(RunnerConfig{ServerOrigin: "http://words.example.test", ConsoleOrigin: "http://127.0.0.1:8090", SpoolDir: spool})
	require.Error(t, err)
	_, err = NewRunner(RunnerConfig{ServerOrigin: "https://words.example.test", ConsoleOrigin: "http://192.0.2.10:8090", SpoolDir: spool})
	require.Error(t, err)
	_, err = NewRunner(RunnerConfig{ServerOrigin: "https://user:pass@words.example.test", ConsoleOrigin: "http://127.0.0.1:8090", SpoolDir: spool})
	require.Error(t, err)
	runner, err := NewRunner(RunnerConfig{ServerOrigin: "https://words.example.test", ConsoleOrigin: "http://127.0.0.1:8090", SpoolDir: spool})
	require.NoError(t, err)
	require.NotNil(t, runner)
}

func TestRunnerHeartbeatStopsOnRevokedCredential(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	runner := &Runner{cfg: RunnerConfig{ServerOrigin: server.URL}, http: server.Client(), credential: runnerCredential{AgentID: "revoked", AgentToken: "old-token"}}
	err := runner.heartbeatLoop(context.Background())
	require.ErrorContains(t, err, "credential revoked or disabled")
	require.EqualValues(t, 1, calls.Load(), "revoked credentials must terminate heartbeat instead of retrying forever")
}

func TestRunnerSpoolUsesAtomicPrivateFiles(t *testing.T) {
	dir := t.TempDir()
	runner, err := NewRunner(RunnerConfig{ServerOrigin: "https://words.example.test", ConsoleOrigin: "http://localhost:8090", SpoolDir: dir})
	require.NoError(t, err)
	require.NoError(t, runner.writeAtomic(filepath.Join(dir, "active-claim.json"), runnerJournal{RequestID: "request-1", Claim: Claim{JobID: 7, AttemptToken: "secret-attempt"}}))
	raw, err := os.ReadFile(runner.journalPath())
	require.NoError(t, err)
	require.Contains(t, string(raw), "secret-attempt")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "atomic rename leaves no temporary spool artifacts")
	info, err := entries[0].Info()
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestNormalizeCapabilitiesRequiresVoiceAndSupportedLanguage(t *testing.T) {
	_, err := NormalizeCapabilities([]Capability{{ServiceID: "tts-en", Model: "model", Language: "fr", Voice: "default"}})
	require.Error(t, err)
	_, err = NormalizeCapabilities([]Capability{{ServiceID: "tts-en", Model: "model", Language: "en"}})
	require.Error(t, err)
	caps, err := NormalizeCapabilities([]Capability{{ServiceID: "tts-en", Model: "voice-model", Language: "en", Voice: "teacher"}})
	require.NoError(t, err)
	require.Equal(t, "teacher", caps[0].Voice)
}

func runnerTestWAV() []byte {
	wav := make([]byte, 46)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 16000)
	binary.LittleEndian.PutUint32(wav[28:32], 32000)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], 2)
	wav[44], wav[45] = 1, 0
	return wav
}

func TestRunnerRecoversDurableSpoolWithoutResynthesizingAfterLostReceipt(t *testing.T) {
	var synthesisCalls, reservationCalls, resultCalls atomic.Int32
	wav := runnerTestWAV()
	consoleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tts-en/speech" {
			http.NotFound(w, r)
			return
		}
		synthesisCalls.Add(1)
		_, _ = w.Write(wav)
	}))
	defer consoleServer.Close()
	cap := Capability{ServiceID: "tts-en", Model: "model-v1", Language: "en", Voice: "default"}
	claim := Claim{JobID: 8, ManifestRevision: "manifest-r1", AttemptToken: "attempt-r1", LeaseUntil: time.Now().Add(time.Minute), Language: "en", SpeechText: "teacher", ServiceID: "tts-en", Model: "model-v1", Voice: "default"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Trace-ID", "trace-agent-test")
		switch r.URL.Path {
		case "/api/v1/speech-agent/next":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "", "data": claim})
		case "/api/v1/speech-agent/jobs/8/lease":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "", "data": map[string]any{"lease_until": time.Now().Add(time.Minute)}})
		case "/api/v1/speech-agent/jobs/8/reservations":
			reservationCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "", "data": map[string]any{"reservation_id": "reservation-r1", "max_bytes": 10 << 20, "expires_at": time.Now().Add(time.Minute)}})
		case "/api/v1/speech-agent/jobs/8/result":
			if r.Header.Get("X-Speech-Upload-Reservation") != "reservation-r1" || r.Header.Get("X-Speech-Attempt-Token") != "attempt-r1" {
				http.Error(w, "bad fencing", http.StatusConflict)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(body, wav) {
				http.Error(w, "bad audio", http.StatusBadRequest)
				return
			}
			if resultCalls.Add(1) == 1 {
				http.Error(w, "receipt response was lost after commit", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "", "data": map[string]any{"accepted": true}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := server.Client()
	spool := t.TempDir()
	cfg := RunnerConfig{ServerOrigin: server.URL, ConsoleOrigin: consoleServer.URL, SpoolDir: spool, AgentToken: "agent-secret"}
	r1, err := NewRunner(cfg)
	require.NoError(t, err)
	r1.http = client
	r1.credential = runnerCredential{AgentID: "agent-1", AgentToken: "agent-secret"}
	r1.capabilities = []Capability{cap}
	r1.console.Services["tts-en"] = localmodel.Service{ID: "tts-en", Model: "model-v1", Capabilities: []string{"tts"}}
	err = r1.processJournal(context.Background())
	require.Error(t, err, "the first receipt response is intentionally lost")
	require.FileExists(t, r1.audioPath())
	require.FileExists(t, r1.journalPath())
	r2, err := NewRunner(cfg)
	require.NoError(t, err)
	r2.http = client
	r2.credential = r1.credential
	r2.capabilities = []Capability{cap}
	r2.console.Services["tts-en"] = localmodel.Service{ID: "tts-en", Model: "model-v1", Capabilities: []string{"tts"}}
	require.NoError(t, r2.processJournal(context.Background()))
	require.EqualValues(t, 1, synthesisCalls.Load(), "restart must upload the durable WAV instead of synthesizing again")
	require.EqualValues(t, 1, reservationCalls.Load(), "restart must reuse the durable reservation id")
	require.EqualValues(t, 2, resultCalls.Load(), "the lost receipt is retried with the same attempt and bytes")
	_, err = os.Stat(r2.audioPath())
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(r2.journalPath())
	require.True(t, os.IsNotExist(err))
}

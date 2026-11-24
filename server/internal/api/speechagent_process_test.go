package api

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/scheduler"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestTwoResidentProcessesCompleteIsolatedCourseMaterialOverTLS(t *testing.T) {
	t.Setenv("ENWORDS_TTS_CACHE_DIR", t.TempDir())
	app, _, lessonID, _ := setupRoom(t)
	app.Clock = scheduler.RealClock{}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "resident-e2e.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	app.DB = db
	require.NoError(t, db.AutoMigrate(&store.User{}))
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "admin"))
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "sfin"))
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.StopSpeechMaterialJobs(ctx)
		_ = pool.Close()
	})
	adminToken, _ := loginGet(t, app, "admin", "adminpw")
	studentToken, _ := loginGet(t, app, "sfin", "pw")

	var synthesisCalls atomic.Int32
	wav := minimalTestWAV()
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]any{
				{"id": "tts", "model": "model-v1", "capabilities": []string{"tts"}},
				{"id": "tts-en", "model": "model-v1", "capabilities": []string{"tts"}},
			}})
		case "/api/tts/speech", "/api/tts-en/speech":
			var input struct {
				Model string `json:"model"`
				Voice string `json:"voice"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "model-v1" || input.Voice != "default" {
				http.Error(w, "unapproved model request", http.StatusBadRequest)
				return
			}
			synthesisCalls.Add(1)
			_, _ = w.Write(wav)
		default:
			http.NotFound(w, r)
		}
	}))
	defer console.Close()

	server := httptest.NewTLSServer(app.Router())
	defer server.Close()
	client := server.Client()
	enrollmentBody := `{"ttl_seconds":300,"capabilities":[{"service_id":"tts","model":"model-v1","language":"zh","voice":"default"},{"service_id":"tts-en","model":"model-v1","language":"en","voice":"default"}]}`
	enrollmentResp := speechAgentTLSRequest(t, client, http.MethodPost, server.URL+"/api/v1/admin/speech-agents/enrollments", adminToken, enrollmentBody)
	require.Equal(t, http.StatusCreated, enrollmentResp.StatusCode, responseBodyForTest(t, enrollmentResp))
	enrollmentData := speechAgentTLSData(t, enrollmentResp)
	enrollmentToken := enrollmentData["enrollment_token"].(string)

	moduleDir, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	goExe, err := exec.LookPath("go")
	if err != nil && runtime.GOOS == "windows" {
		goExe = `C:\Program Files\Go\bin\go.exe`
		_, err = os.Stat(goExe)
	}
	require.NoError(t, err, "Go toolchain required for the isolated resident executable")
	workerPath := filepath.Join(t.TempDir(), "speech-agent"+map[bool]string{true: ".exe", false: ""}[runtime.GOOS == "windows"])
	build := exec.Command(goExe, "build", "-o", workerPath, "./cmd/speech-agent")
	build.Dir = moduleDir
	buildOutput, err := build.CombinedOutput()
	require.NoError(t, err, string(buildOutput))
	caFile := filepath.Join(t.TempDir(), "test-server-ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))

	spool1, spool2 := filepath.Join(t.TempDir(), "agent-one"), filepath.Join(t.TempDir(), "agent-two")
	var workerLogs []*os.File
	var workerLogPaths []string
	startWorker := func(spool, token string) *exec.Cmd {
		_ = os.MkdirAll(spool, 0o700)
		args := []string{"-server", server.URL, "-console", console.URL, "-spool", spool, "-ca-file", caFile, "-name", "isolated-runner"}
		cmd := exec.Command(workerPath, args...)
		cmd.Env = append(os.Environ(), "ENWORDS_SPEECH_ENROLLMENT_TOKEN="+token)
		logPath := spool + ".log"
		logFile, _ := os.Create(logPath)
		workerLogs = append(workerLogs, logFile)
		workerLogPaths = append(workerLogPaths, logPath)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		return cmd
	}
	worker1 := startWorker(spool1, enrollmentToken)
	require.NoError(t, worker1.Start())
	workers := []*exec.Cmd{worker1}
	t.Cleanup(func() {
		for _, worker := range workers {
			if worker.Process != nil {
				_ = worker.Process.Kill()
				_ = worker.Wait()
			}
		}
		for _, logFile := range workerLogs {
			_ = logFile.Close()
		}
	})
	var credentialBytes []byte
	credentialDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(credentialDeadline) {
		credentialBytes, err = os.ReadFile(filepath.Join(spool1, "agent-credential.json"))
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		logBytes, _ := os.ReadFile(spool1 + ".log")
		t.Fatalf("worker did not persist enrolled credentials: %v; log=%s", err, logBytes)
	}
	var agent store.SpeechAgent
	require.NoError(t, db.Order("created_at DESC").First(&agent).Error)
	var credential struct {
		AgentID    string `json:"agent_id"`
		AgentToken string `json:"agent_token"`
	}
	require.NoError(t, json.Unmarshal(credentialBytes, &credential))
	require.NotEmpty(t, credential.AgentToken)
	require.Equal(t, agent.ID, credential.AgentID)
	require.NoError(t, os.MkdirAll(spool2, 0o700))
	capabilities, err := os.ReadFile(filepath.Join(spool1, "approved-capabilities.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(spool2, "approved-capabilities.json"), capabilities, 0o600))
	worker2 := startWorker(spool2, "")
	worker2.Env = append(os.Environ(), "ENWORDS_SPEECH_AGENT_TOKEN="+credential.AgentToken)
	require.NoError(t, worker2.Start())
	workers = append(workers, worker2)

	ensureResp := speechAgentTLSRequest(t, client, http.MethodPost, server.URL+"/api/v1/classroom/lessons/"+itoa64(lessonID)+"/speech-materials/ensure", studentToken, `{}`)
	require.Equal(t, http.StatusOK, ensureResp.StatusCode)
	ensure := speechAgentTLSData(t, ensureResp)
	total := int(ensure["total"].(float64))
	require.Greater(t, total, 0)
	var final map[string]any
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		resp := speechAgentTLSRequest(t, client, http.MethodGet, server.URL+"/api/v1/classroom/lessons/"+itoa64(lessonID)+"/speech-materials/progress", studentToken, "")
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		data := speechAgentTLSData(t, resp)
		final = data
		if data["can_start"] == true {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if final == nil || final["can_start"] != true {
		var jobs []store.SpeechMaterialJob
		_ = db.Find(&jobs).Error
		logs := make([]string, 0, len(workerLogPaths))
		for _, path := range workerLogPaths {
			b, _ := os.ReadFile(path)
			logs = append(logs, string(b))
		}
		t.Fatalf("resident workers failed to complete assets; progress=%#v jobs=%#v logs=%s", final, jobs, strings.Join(logs, "\n--- worker ---\n"))
	}
	require.Equal(t, float64(total), final["ready"])
	var readyJobs, receipts int64
	require.NoError(t, db.Model(&store.SpeechMaterialJob{}).Where("status = ?", "ready").Count(&readyJobs).Error)
	var uniqueJobs int64
	require.NoError(t, db.Model(&store.SpeechMaterialJob{}).Where("manifest_revision = ?", ensure["manifest_revision"]).Count(&uniqueJobs).Error)
	require.EqualValues(t, uniqueJobs, synthesisCalls.Load(), "shared duplicate text must synthesize once and competing processes must not duplicate a claimed job")
	require.EqualValues(t, uniqueJobs, readyJobs)
	require.NoError(t, db.Model(&store.SpeechAgentResultReceipt{}).Where("accepted = ?", true).Count(&receipts).Error)
	require.EqualValues(t, uniqueJobs, receipts)
	entryResp := speechAgentTLSRequest(t, client, http.MethodPost, server.URL+"/api/v1/classroom/lessons/"+itoa64(lessonID)+"/enter", studentToken, `{}`)
	require.Equal(t, http.StatusOK, entryResp.StatusCode)
	entry := speechAgentTLSData(t, entryResp)
	require.NotEmpty(t, entry["ticket"])

}

func speechAgentTLSRequest(t *testing.T, client *http.Client, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func speechAgentTLSData(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var payload Resp
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.Equal(t, 0, payload.Code, payload.Msg)
	data, ok := payload.Data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected response data type %T", payload.Data)
	}
	return data
}

func responseBodyForTest(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	return string(body)
}

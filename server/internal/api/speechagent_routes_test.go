package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enwords/internal/obs"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func speechAgentRequest(t *testing.T, app *App, method, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if strings.HasPrefix(path, "/api/v1/speech-agent/") {
		req.TLS = &tls.ConnectionState{}
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := httptest.NewRecorder()
	app.Router().ServeHTTP(resp, req)
	return resp
}

func speechAgentTestWAV() []byte {
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

func speechAgentResponseData(t *testing.T, resp *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload Resp
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &payload))
	require.Equal(t, 0, payload.Code, payload.Msg)
	data, ok := payload.Data.(map[string]any)
	if !ok {
		t.Fatalf("response data is %T, want object", payload.Data)
	}
	return data
}

type gatedUploadBody struct {
	data    []byte
	reader  *bytes.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *gatedUploadBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.release; b.reader = bytes.NewReader(b.data) })
	return b.reader.Read(p)
}
func (*gatedUploadBody) Close() error { return nil }

func TestSpeechAgentHTTPEnrollmentClaimLeaseRotationAndRevocation(t *testing.T) {
	t.Setenv("ENWORDS_TTS_CACHE_DIR", t.TempDir())
	app, _, lessonID, _ := setupRoom(t)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-agent-api.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	app.DB = db
	require.NoError(t, db.AutoMigrate(&store.User{}, &store.SpeechMaterialManifest{}, &store.SpeechMaterialJob{}))
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
	var logs bytes.Buffer
	app.SetLogger(obs.NewWithSink(&logs))
	caps := `[{"service_id":"tts-en","model":"model-v1","language":"en","voice":"default"}]`
	createBody := `{"capabilities":` + caps + `,"ttl_seconds":300}`
	adminPath := "/api/v1/admin/speech-agents/enrollments"
	created := speechAgentRequest(t, app, http.MethodPost, adminPath, adminToken, createBody)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	enrollmentData := speechAgentResponseData(t, created)
	enrollmentToken := enrollmentData["enrollment_token"].(string)
	badEnrollmentBody := speechAgentRequest(t, app, http.MethodPost, adminPath, adminToken, `{`)
	require.Equal(t, http.StatusBadRequest, badEnrollmentBody.Code)
	badCapabilities := speechAgentRequest(t, app, http.MethodPost, adminPath, adminToken, `{"ttl_seconds":300,"capabilities":[{}]}`)
	require.Equal(t, http.StatusBadRequest, badCapabilities.Code)
	forbidden := speechAgentRequest(t, app, http.MethodPost, adminPath, studentToken, createBody)
	require.Equal(t, http.StatusOK, forbidden.Code)
	require.NotContains(t, logs.String(), enrollmentToken, "enrollment credentials must never enter logs")

	enroll := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/enroll", "", `{"enrollment_token":"`+enrollmentToken+`","display_name":"desktop-test"}`)
	require.Equal(t, http.StatusCreated, enroll.Code, enroll.Body.String())
	enrolled := speechAgentResponseData(t, enroll)
	agentID := enrolled["agent_id"].(string)
	agentToken := enrolled["agent_token"].(string)
	listed := speechAgentRequest(t, app, http.MethodGet, "/api/v1/admin/speech-agents", adminToken, "")
	require.Equal(t, http.StatusOK, listed.Code)
	listedPayload := doJSON(t, listed)
	listedAgents, ok := listedPayload.Data.([]any)
	require.True(t, ok)
	require.Len(t, listedAgents, 1)
	listedAgent, ok := listedAgents[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, agentID, listedAgent["id"])
	require.Equal(t, "desktop-test", listedAgent["display_name"])
	require.Equal(t, true, listedAgent["enabled"])
	replay := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/enroll", "", `{"enrollment_token":"`+enrollmentToken+`","display_name":"replay"}`)
	require.Equal(t, http.StatusUnauthorized, replay.Code)
	require.NotContains(t, logs.String(), agentToken, "agent credential must never enter logs")

	heartbeat := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/heartbeat", agentToken, `{}`)
	require.Equal(t, http.StatusOK, heartbeat.Code, heartbeat.Body.String())
	job := store.SpeechMaterialJob{CacheKey: "remote-task", ManifestRevision: "manifest-1", LessonID: lessonID, Language: "en", SpeechText: "cat", ServiceID: "tts-en", Model: "model-v1", Voice: "default", Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, db.Create(&job).Error)
	require.NoError(t, db.Create(&store.SpeechMaterialManifest{LessonID: lessonID, Revision: "manifest-1", UpdatedAt: time.Now().UTC()}).Error)
	wrong := store.SpeechMaterialJob{CacheKey: "unapproved-model", ManifestRevision: "manifest-1", LessonID: lessonID, Language: "en", SpeechText: "dog", ServiceID: "tts-en", Model: "model-v2", Voice: "default", Status: "pending", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&wrong).Error)
	nextPath := "/api/v1/speech-agent/next"
	claimResp := speechAgentRequest(t, app, http.MethodPost, nextPath, agentToken, `{"request_id":"request-1"}`)
	require.Equal(t, http.StatusOK, claimResp.Code, claimResp.Body.String())
	claim := speechAgentResponseData(t, claimResp)
	firstToken := claim["attempt_token"].(string)
	require.Equal(t, float64(job.ID), claim["job_id"])
	duplicate := speechAgentRequest(t, app, http.MethodPost, nextPath, agentToken, `{"request_id":"request-1"}`)
	replayed := speechAgentResponseData(t, duplicate)
	require.Equal(t, firstToken, replayed["attempt_token"])
	require.Equal(t, claim["job_id"], replayed["job_id"])
	lease := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/lease", agentToken, `{"attempt_token":"`+firstToken+`"}`)
	require.Equal(t, http.StatusOK, lease.Code, lease.Body.String())
	wrongLease := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/lease", agentToken, `{"attempt_token":"stale-attempt"}`)
	require.Equal(t, http.StatusConflict, wrongLease.Code)
	badJobID := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/jobs/not-a-number/lease", agentToken, `{"attempt_token":"`+firstToken+`"}`)
	require.Equal(t, http.StatusBadRequest, badJobID.Code)
	var wrongState store.SpeechMaterialJob
	require.NoError(t, db.First(&wrongState, wrong.ID).Error)
	require.Equal(t, "pending", wrongState.Status, "unapproved model task must not be claimed")

	rotated := speechAgentRequest(t, app, http.MethodPost, "/api/v1/admin/speech-agents/"+agentID+"/rotate", adminToken, `{}`)
	missingRotation := speechAgentRequest(t, app, http.MethodPost, "/api/v1/admin/speech-agents/missing/rotate", adminToken, `{}`)
	require.Equal(t, http.StatusNotFound, missingRotation.Code)
	newToken := speechAgentResponseData(t, rotated)["agent_token"].(string)
	oldHeartbeat := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/heartbeat", agentToken, `{}`)
	require.Equal(t, http.StatusUnauthorized, oldHeartbeat.Code)
	newHeartbeat := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/heartbeat", newToken, `{}`)
	require.Equal(t, http.StatusOK, newHeartbeat.Code)
	revoked := speechAgentRequest(t, app, http.MethodPost, "/api/v1/admin/speech-agents/"+agentID+"/revoke", adminToken, `{}`)
	require.Equal(t, http.StatusOK, revoked.Code)
	missingRevocation := speechAgentRequest(t, app, http.MethodPost, "/api/v1/admin/speech-agents/missing/revoke", adminToken, `{}`)
	require.Equal(t, http.StatusNotFound, missingRevocation.Code)
	require.Equal(t, http.StatusUnauthorized, speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/heartbeat", newToken, `{}`).Code)
	var reset store.SpeechMaterialJob
	require.NoError(t, db.First(&reset, job.ID).Error)
	require.Equal(t, "pending", reset.Status, "revocation must fence and requeue in-flight work")
}

func TestSpeechAgentHTTPRequiresTLSBeforeAuthentication(t *testing.T) {
	app, _, _, _ := setupRoom(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/speech-agent/heartbeat", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.Router().ServeHTTP(resp, req)
	require.Equal(t, http.StatusUpgradeRequired, resp.Code)
}

func TestSpeechAgentHTTPUploadReservationValidationAndIdempotentReceipt(t *testing.T) {
	t.Setenv("ENWORDS_TTS_CACHE_DIR", t.TempDir())
	app, _, lessonID, _ := setupRoom(t)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-agent-upload.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	app.DB = db
	require.NoError(t, db.AutoMigrate(&store.User{}))
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "admin"))
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.StopSpeechMaterialJobs(ctx)
		_ = pool.Close()
	})
	adminToken, _ := loginGet(t, app, "admin", "adminpw")
	created := speechAgentRequest(t, app, http.MethodPost, "/api/v1/admin/speech-agents/enrollments", adminToken,
		`{"ttl_seconds":300,"capabilities":[{"service_id":"tts-en","model":"model-v1","language":"en","voice":"default"}]}`)
	enrollment := speechAgentResponseData(t, created)["enrollment_token"].(string)
	enrolledResp := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/enroll", "", `{"enrollment_token":"`+enrollment+`","display_name":"upload-test"}`)
	agent := speechAgentResponseData(t, enrolledResp)
	agentID, agentToken := agent["agent_id"].(string), agent["agent_token"].(string)
	job := store.SpeechMaterialJob{CacheKey: "upload-cache-key", ManifestRevision: "manifest-v1", LessonID: lessonID, Language: "en", SpeechText: "cat", ServiceID: "tts-en", Model: "model-v1", Voice: "default", Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, db.Create(&job).Error)
	require.NoError(t, db.Create(&store.SpeechMaterialManifest{LessonID: lessonID, Revision: "manifest-v1", UpdatedAt: time.Now().UTC()}).Error)
	claimResp := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/next", agentToken, `{"request_id":"upload-claim"}`)
	claim := speechAgentResponseData(t, claimResp)
	attempt := claim["attempt_token"].(string)
	reservationResp := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/reservations", agentToken, `{"attempt_token":"`+attempt+`"}`)
	require.Equal(t, http.StatusCreated, reservationResp.Code, reservationResp.Body.String())
	firstReservation := speechAgentResponseData(t, reservationResp)["reservation_id"].(string)
	secondReservationResp := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/reservations", agentToken, `{"attempt_token":"`+attempt+`"}`)
	require.Equal(t, http.StatusCreated, secondReservationResp.Code, secondReservationResp.Body.String())
	reservation := speechAgentResponseData(t, secondReservationResp)["reservation_id"].(string)
	thirdReservationResp := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/reservations", agentToken, `{"attempt_token":"`+attempt+`"}`)
	require.Equal(t, http.StatusTooManyRequests, thirdReservationResp.Code, "per-agent in-flight upload limit must be atomic")
	bad := httptest.NewRequest(http.MethodPut, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/result", strings.NewReader("not wav"))
	bad.TLS = &tls.ConnectionState{}
	bad.Header.Set("Authorization", "Bearer "+agentToken)
	bad.Header.Set("Content-Type", "audio/wav")
	bad.Header.Set("X-Speech-Upload-Reservation", reservation)
	bad.Header.Set("X-Speech-Attempt-Token", attempt)
	badResp := httptest.NewRecorder()
	app.Router().ServeHTTP(badResp, bad)
	require.Equal(t, http.StatusBadRequest, badResp.Code)
	var unchanged store.SpeechMaterialJob
	require.NoError(t, db.First(&unchanged, job.ID).Error)
	require.Equal(t, "running", unchanged.Status, "invalid audio cannot ready the job")
	var active store.SpeechUploadReservation
	require.NoError(t, db.Where("id = ?", reservation).First(&active).Error)
	require.Equal(t, "rejected", active.Status, "invalid upload must release its slot")

	reservation = firstReservation
	put := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/result", bytes.NewReader(speechAgentTestWAV()))
		req.TLS = &tls.ConnectionState{}
		req.Header.Set("Authorization", "Bearer "+agentToken)
		req.Header.Set("Content-Type", "audio/wav")
		req.Header.Set("X-Speech-Upload-Reservation", reservation)
		req.Header.Set("X-Speech-Attempt-Token", attempt)
		response := httptest.NewRecorder()
		app.Router().ServeHTTP(response, req)
		return response
	}
	accepted := put()
	require.Equal(t, http.StatusOK, accepted.Code, accepted.Body.String())
	var ready store.SpeechMaterialJob
	require.NoError(t, db.First(&ready, job.ID).Error)
	require.Equal(t, "ready", ready.Status)
	var asset store.SpeechAsset
	require.NoError(t, db.Where("cache_key = ?", job.CacheKey).First(&asset).Error)
	require.Equal(t, int64(len(speechAgentTestWAV())), asset.SizeBytes)
	require.True(t, validSpeechMaterialPathForTest(t, asset))
	var receiptCount int64
	require.NoError(t, db.Model(&store.SpeechAgentResultReceipt{}).Where("job_id = ? AND attempt_token = ? AND accepted = ?", job.ID, attempt, true).Count(&receiptCount).Error)
	require.EqualValues(t, 1, receiptCount)
	gate := &gatedUploadBody{data: speechAgentTestWAV(), started: make(chan struct{}), release: make(chan struct{})}
	firstReplayRequest := httptest.NewRequest(http.MethodPut, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/result", gate)
	firstReplayRequest.TLS = &tls.ConnectionState{}
	firstReplayRequest.Header.Set("Authorization", "Bearer "+agentToken)
	firstReplayRequest.Header.Set("Content-Type", "audio/wav")
	firstReplayRequest.Header.Set("X-Speech-Upload-Reservation", reservation)
	firstReplayRequest.Header.Set("X-Speech-Attempt-Token", attempt)
	firstReplayResponse := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() { app.Router().ServeHTTP(firstReplayResponse, firstReplayRequest); close(finished) }()
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("replay body read did not start")
	}
	secondBody := bytes.NewReader(speechAgentTestWAV())
	secondRequest := httptest.NewRequest(http.MethodPut, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/result", secondBody)
	secondRequest.TLS = &tls.ConnectionState{}
	secondRequest.Header.Set("Authorization", "Bearer "+agentToken)
	secondRequest.Header.Set("Content-Type", "audio/wav")
	secondRequest.Header.Set("X-Speech-Upload-Reservation", reservation)
	secondRequest.Header.Set("X-Speech-Attempt-Token", attempt)
	secondResponse := httptest.NewRecorder()
	app.Router().ServeHTTP(secondResponse, secondRequest)
	require.Equal(t, http.StatusConflict, secondResponse.Code, "same reservation cannot admit overlapping body reads")
	require.Equal(t, len(speechAgentTestWAV()), secondBody.Len(), "rejected concurrent replay must not consume or buffer its body")
	var replayReservation store.SpeechUploadReservation
	require.NoError(t, db.First(&replayReservation, "id = ?", reservation).Error)
	require.Equal(t, "uploading", replayReservation.Status)
	close(gate.release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("replay request did not finish after body was released")
	}
	require.Equal(t, http.StatusOK, firstReplayResponse.Code, firstReplayResponse.Body.String(), "same bytes and attempt must return the original result receipt")
	require.NotContains(t, firstReplayResponse.Body.String(), agentToken)
	var replayCount int64
	require.NoError(t, db.Model(&store.SpeechAgentResultReceipt{}).Where("job_id = ? AND attempt_token = ?", job.ID, attempt).Count(&replayCount).Error)
	require.EqualValues(t, 1, replayCount)
	var reserved int64
	require.NoError(t, db.Model(&store.SpeechUploadReservation{}).Where("agent_id = ? AND status = ?", agentID, "active").Count(&reserved).Error)
	require.Zero(t, reserved, "accepted and replayed uploads must release reservations")
}

func TestSpeechAgentUploadReservationsBoundAggregateInflightBytes(t *testing.T) {
	t.Setenv("ENWORDS_TTS_CACHE_DIR", t.TempDir())
	app, _, lessonID, _ := setupRoom(t)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-agent-global-upload.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	app.DB = db
	require.NoError(t, db.AutoMigrate(&store.User{}, &store.SpeechMaterialManifest{}, &store.SpeechMaterialJob{}))
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "admin"))
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.StopSpeechMaterialJobs(context.Background()); _ = pool.Close() })
	adminToken, _ := loginGet(t, app, "admin", "adminpw")
	const revision = "aggregate-reservation-r1"
	require.NoError(t, db.Create(&store.SpeechMaterialManifest{LessonID: lessonID, Revision: revision, UpdatedAt: time.Now().UTC()}).Error)
	for i := 0; i < 4; i++ {
		job := store.SpeechMaterialJob{CacheKey: "aggregate-" + itoa64(int64(i)), ManifestRevision: revision, LessonID: lessonID, Language: "en", SpeechText: "cat", ServiceID: "tts-en", Model: "model-v1", Voice: "default", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		require.NoError(t, db.Create(&job).Error)
	}
	statuses := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		created := speechAgentRequest(t, app, http.MethodPost, "/api/v1/admin/speech-agents/enrollments", adminToken, `{"ttl_seconds":300,"capabilities":[{"service_id":"tts-en","model":"model-v1","language":"en","voice":"default"}]}`)
		enrollment := speechAgentResponseData(t, created)["enrollment_token"].(string)
		enrolled := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/enroll", "", `{"enrollment_token":"`+enrollment+`","display_name":"aggregate-test"}`)
		agentToken := speechAgentResponseData(t, enrolled)["agent_token"].(string)
		claimResp := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/next", agentToken, `{"request_id":"aggregate-claim-`+itoa64(int64(i))+`"}`)
		claim := speechAgentResponseData(t, claimResp)
		reservation := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/jobs/"+itoa64(int64(claim["job_id"].(float64)))+"/reservations", agentToken, `{"attempt_token":"`+claim["attempt_token"].(string)+`"}`)
		statuses = append(statuses, reservation.Code)
	}
	require.Equal(t, []int{http.StatusCreated, http.StatusCreated, http.StatusCreated, http.StatusTooManyRequests}, statuses,
		"shared SQLite reservations must bound the aggregate body memory across distinct agents")
	var reserved int64
	require.NoError(t, db.Model(&store.SpeechUploadReservation{}).Where("status = ?", "active").Select("COALESCE(SUM(reserved_bytes),0)").Scan(&reserved).Error)
	require.LessOrEqual(t, reserved, speechUploadInFlightByteLimit)
}

func TestSpeechAgentUploadExpiredLeaseDoesNotCommitAfterSlowBody(t *testing.T) {
	t.Setenv("ENWORDS_TTS_CACHE_DIR", t.TempDir())
	app, clock, lessonID, _ := setupRoom(t)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-agent-expired-upload.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	app.DB = db
	require.NoError(t, db.AutoMigrate(&store.User{}, &store.SpeechMaterialManifest{}, &store.SpeechMaterialJob{}))
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "admin"))
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.StopSpeechMaterialJobs(context.Background()); _ = pool.Close() })
	adminToken, _ := loginGet(t, app, "admin", "adminpw")
	created := speechAgentRequest(t, app, http.MethodPost, "/api/v1/admin/speech-agents/enrollments", adminToken,
		`{"ttl_seconds":300,"capabilities":[{"service_id":"tts-en","model":"model-v1","language":"en","voice":"default"}]}`)
	enrollment := speechAgentResponseData(t, created)["enrollment_token"].(string)
	enrolled := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/enroll", "", `{"enrollment_token":"`+enrollment+`","display_name":"lease-test"}`)
	agentToken := speechAgentResponseData(t, enrolled)["agent_token"].(string)
	revision := "slow-upload-r1"
	require.NoError(t, db.Create(&store.SpeechMaterialManifest{LessonID: lessonID, Revision: revision, UpdatedAt: clock.Now().UTC()}).Error)
	job := store.SpeechMaterialJob{CacheKey: "slow-upload-key", ManifestRevision: revision, LessonID: lessonID, Language: "en", SpeechText: "cat", ServiceID: "tts-en", Model: "model-v1", Voice: "default", Status: "pending", CreatedAt: clock.Now().UTC(), UpdatedAt: clock.Now().UTC()}
	require.NoError(t, db.Create(&job).Error)
	claimResp := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/next", agentToken, `{"request_id":"slow-upload-claim"}`)
	claim := speechAgentResponseData(t, claimResp)
	attempt := claim["attempt_token"].(string)
	reserve := speechAgentRequest(t, app, http.MethodPost, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/reservations", agentToken, `{"attempt_token":"`+attempt+`"}`)
	reservation := speechAgentResponseData(t, reserve)["reservation_id"].(string)
	gate := &gatedUploadBody{data: speechAgentTestWAV(), started: make(chan struct{}), release: make(chan struct{})}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/speech-agent/jobs/"+itoa64(job.ID)+"/result", gate)
	req.TLS = &tls.ConnectionState{}
	req.Header.Set("Authorization", "Bearer "+agentToken)
	req.Header.Set("Content-Type", "audio/wav")
	req.Header.Set("X-Speech-Upload-Reservation", reservation)
	req.Header.Set("X-Speech-Attempt-Token", attempt)
	response := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() { app.Router().ServeHTTP(response, req); close(finished) }()
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload body read did not start")
	}
	clock.Advance(61 * time.Second)
	close(gate.release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not finish after body was released")
	}
	require.Equal(t, http.StatusConflict, response.Code, "lease expiry during body read must be checked at commit time")
	var stored store.SpeechMaterialJob
	require.NoError(t, db.First(&stored, job.ID).Error)
	require.Equal(t, "running", stored.Status)
	var assets int64
	require.NoError(t, db.Model(&store.SpeechAsset{}).Where("cache_key = ?", job.CacheKey).Count(&assets).Error)
	require.Zero(t, assets, "late audio cannot become a cached ready asset")
}

func validSpeechMaterialPathForTest(t *testing.T, asset store.SpeechAsset) bool {
	t.Helper()
	data, err := os.ReadFile(asset.FilePath)
	require.NoError(t, err)
	return validSpeechAudio(data, asset.SizeBytes, asset.SHA256)
}

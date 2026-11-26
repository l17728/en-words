package speechagent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"enwords/internal/store"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCredentialIsOneTimeHighEntropyAndStoredAsHash(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 40 {
		t.Fatalf("token too short: %d chars", len(token))
	}
	hash := TokenHash(token)
	if hash == token || !VerifyToken(token, hash) {
		t.Fatal("generated token did not verify against its stored hash")
	}
	if VerifyToken(token+"x", hash) || VerifyToken("", hash) || VerifyToken(token, "bad") {
		t.Fatal("invalid credential was accepted")
	}
}

func TestEnrollmentClaimLeaseAndRevocationLifecycle(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-agent.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.AutoMigrate(&store.SpeechAgent{}, &store.SpeechAgentEnrollment{}, &store.SpeechAgentClaimRequest{}, &store.SpeechAgentResultReceipt{}, &store.SpeechUploadReservation{}, &store.SpeechAgentRateWindow{}, &store.SpeechMaterialJob{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	svc := Service{DB: db, Now: func() time.Time { return now }}
	caps := []Capability{{ServiceID: "tts-en", Model: "model-v1", Language: "en", Voice: "default"}}
	enrollment, err := svc.CreateEnrollment(context.Background(), 7, caps, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	agent, token, err := svc.ExchangeEnrollment(context.Background(), enrollment, "Trusted desktop")
	if err != nil {
		t.Fatal(err)
	}
	if agent.TokenHash == token || !VerifyToken(token, agent.TokenHash) {
		t.Fatal("agent credential was not stored as a one-way hash")
	}
	if _, _, err := svc.ExchangeEnrollment(context.Background(), enrollment, "replay"); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("single-use enrollment replay error = %v", err)
	}
	if _, err := svc.Authenticate(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	job := store.SpeechMaterialJob{CacheKey: "agent-job", ManifestRevision: "revision-1", Language: "en", SpeechText: "cat", ServiceID: "tts-en", Model: "model-v1", Voice: "default", Status: "pending", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.SpeechMaterialJob{CacheKey: "wrong-model", ManifestRevision: "revision-1", Language: "en", SpeechText: "dog", ServiceID: "tts-en", Model: "other", Voice: "default", Status: "pending", CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.Claim(context.Background(), agent, "poll-1")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.JobID != job.ID || claimed.Model != "model-v1" {
		t.Fatalf("claim escaped approved model capability: %#v", claimed)
	}
	replayed, err := svc.Claim(context.Background(), agent, "poll-1")
	if err != nil || replayed.AttemptToken != claimed.AttemptToken {
		t.Fatalf("claim response retry did not return same attempt: %#v err=%v", replayed, err)
	}
	authenticated, err := svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := svc.Renew(context.Background(), authenticated, claimed.JobID, claimed.AttemptToken)
	if err != nil || !lease.After(now.Add(59*time.Second)) {
		t.Fatalf("lease renewal = %v err=%v", lease, err)
	}
	now = lease.Add(time.Second)
	if _, err := svc.Claim(context.Background(), agent, "poll-1"); !errors.Is(err, ErrClaimExpired) {
		t.Fatalf("expired claim retry = %v, want claim_expired", err)
	}
	reclaimed, err := svc.Claim(context.Background(), agent, "poll-2")
	if err != nil || reclaimed.JobID != job.ID || reclaimed.AttemptToken == claimed.AttemptToken {
		t.Fatalf("expired task was not fenced and reclaimed: %#v err=%v", reclaimed, err)
	}
	if err := db.Create(&store.SpeechUploadReservation{ID: "active-upload", AgentID: agent.ID, JobID: job.ID, AttemptToken: reclaimed.AttemptToken, ReservedBytes: 1024, ExpiresAt: now.Add(time.Minute), Status: "active", CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(context.Background(), agent.ID); err != nil {
		t.Fatal(err)
	}
	var reservation store.SpeechUploadReservation
	if err := db.Where("id = ?", "active-upload").First(&reservation).Error; err != nil || reservation.Status != "rejected" {
		t.Fatalf("revocation did not release active upload reservation: %#v err=%v", reservation, err)
	}
	if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, ErrAgentUnauthorized) {
		t.Fatalf("revoked agent authenticated: %v", err)
	}
	if _, err := svc.Renew(context.Background(), authenticated, claimed.JobID, claimed.AttemptToken); !errors.Is(err, ErrAgentUnauthorized) {
		t.Fatalf("revoked agent renewed a lease: %v", err)
	}
}

func TestCapabilitiesAreCanonicalBoundedAndRejectInjection(t *testing.T) {
	caps, err := NormalizeCapabilities([]Capability{
		{ServiceID: "tts-en", Model: "model-b", Language: "en", Voice: "default"},
		{ServiceID: "tts-en", Model: "model-a", Language: "en", Voice: "default"},
		{ServiceID: "tts-en", Model: "model-a", Language: "en", Voice: "default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 2 || caps[0].Model != "model-a" || caps[1].Model != "model-b" {
		t.Fatalf("capabilities were not sorted and deduplicated: %#v", caps)
	}
	if _, err := NormalizeCapabilities([]Capability{{ServiceID: "https://evil", Model: "m", Language: "en", Voice: "v"}}); err == nil {
		t.Fatal("arbitrary service URL was accepted as a capability")
	}
	if _, err := NormalizeCapabilities([]Capability{{ServiceID: "tts", Model: "m", Language: "fr", Voice: "v"}}); err == nil {
		t.Fatal("unsupported language was accepted")
	}
	first, err := EncodeCapabilities(caps)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeCapabilities([]Capability{caps[1], caps[0]})
	if err != nil || first != second {
		t.Fatalf("capabilities JSON is not canonical: %q vs %q, err=%v", first, second, err)
	}
}

func TestRotateTokenFencesAuthenticatedSnapshotAndRequeuesActiveWork(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-agent-rotate.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.AutoMigrate(&store.SpeechAgent{}, &store.SpeechAgentEnrollment{}, &store.SpeechAgentClaimRequest{}, &store.SpeechAgentResultReceipt{}, &store.SpeechUploadReservation{}, &store.SpeechAgentRateWindow{}, &store.SpeechMaterialJob{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	svc := Service{DB: db, Now: func() time.Time { return now }}
	enrollment, err := svc.CreateEnrollment(context.Background(), 1, []Capability{{ServiceID: "tts", Model: "m1", Language: "en", Voice: "default"}}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	agent, oldToken, err := svc.ExchangeEnrollment(context.Background(), enrollment, "test runner")
	if err != nil {
		t.Fatal(err)
	}
	job := store.SpeechMaterialJob{CacheKey: "rotate-key", ServiceID: "tts", Model: "m1", Language: "en", Voice: "default", Status: "pending", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	claim, err := svc.Claim(context.Background(), agent, "rotate-claim")
	if err != nil {
		t.Fatal(err)
	}
	lease := now.Add(time.Minute)
	if err := db.Model(&job).Updates(map[string]any{"lease_until": lease}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&store.SpeechUploadReservation{ID: "rotate-reservation", AgentID: agent.ID, JobID: job.ID, AttemptToken: claim.AttemptToken, ReservedBytes: 1024, ExpiresAt: lease, Status: "active", CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	oldIdentity, err := svc.Authenticate(context.Background(), oldToken)
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := svc.RotateToken(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if AgentIdentityCurrent(db, oldIdentity) {
		t.Fatal("pre-rotation authenticated identity remained current")
	}
	if _, err := svc.Renew(context.Background(), oldIdentity, job.ID, claim.AttemptToken); !errors.Is(err, ErrAgentUnauthorized) {
		t.Fatalf("pre-rotation request renewed lease: %v", err)
	}
	var stored store.SpeechMaterialJob
	if err := db.First(&stored, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" || stored.AttemptToken != "" {
		t.Fatalf("rotation did not fence active attempt: %#v", stored)
	}
	var reservation store.SpeechUploadReservation
	if err := db.First(&reservation, "id = ?", "rotate-reservation").Error; err != nil {
		t.Fatal(err)
	}
	if reservation.Status != "rejected" {
		t.Fatalf("rotation did not release upload reservation: %#v", reservation)
	}
	if _, err := svc.Authenticate(context.Background(), oldToken); !errors.Is(err, ErrAgentUnauthorized) {
		t.Fatalf("old token still authenticates: %v", err)
	}
	if _, err := svc.Authenticate(context.Background(), newToken); err != nil {
		t.Fatalf("new token did not authenticate: %v", err)
	}
}

func TestCleanupExpiredMetadataRetainsOnlyLiveProtocolRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-agent-cleanup.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.AutoMigrate(&store.SpeechAgentEnrollment{}, &store.SpeechAgentClaimRequest{}, &store.SpeechAgentResultReceipt{}, &store.SpeechAgentRateWindow{}, &store.SpeechUploadReservation{}, &store.SpeechMaterialJob{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	old := now.Add(-100 * 24 * time.Hour)
	activeExpiry := now.Add(time.Minute)
	activeLease := now.Add(time.Hour)
	activeJob := store.SpeechMaterialJob{CacheKey: "active", LessonID: 1, ManifestRevision: "r1", Status: "running", AttemptToken: "live-attempt", LeaseUntil: &activeLease}
	if err := db.Create(&activeJob).Error; err != nil {
		t.Fatal(err)
	}
	rows := []any{
		&store.SpeechAgentEnrollment{ID: "old-enrollment", TokenHash: "hash-old", CapabilitiesJSON: "[]", ExpiresAt: old, CreatedBy: 1, CreatedAt: old},
		&store.SpeechAgentClaimRequest{AgentID: "agent", RequestID: "old-request", JobID: 1, CreatedAt: old, UpdatedAt: old},
		&store.SpeechAgentClaimRequest{AgentID: "agent", RequestID: "live-request", JobID: activeJob.ID, AttemptToken: "live-attempt", CreatedAt: old, UpdatedAt: old},
		&store.SpeechAgentResultReceipt{JobID: 1, AttemptToken: "old-attempt", AgentID: "agent", Digest: "digest", Accepted: true, CreatedAt: old},
		&store.SpeechAgentRateWindow{AgentID: "agent", WindowAt: now.Add(-72 * time.Hour), Count: 1, UpdatedAt: old},
		&store.SpeechUploadReservation{ID: "expired-upload", AgentID: "agent", JobID: 1, AttemptToken: "old-attempt", ExpiresAt: now.Add(-time.Minute), Status: "active", CreatedAt: old},
		&store.SpeechUploadReservation{ID: "live-upload", AgentID: "agent", JobID: 2, AttemptToken: "current", ReservedBytes: 1024, ExpiresAt: activeExpiry, Status: "active", CreatedAt: now},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return CleanupExpiredMetadata(tx, now) }); err != nil {
		t.Fatal(err)
	}
	for _, table := range []any{&store.SpeechAgentEnrollment{}, &store.SpeechAgentResultReceipt{}, &store.SpeechAgentRateWindow{}} {
		var n int64
		if err := db.Model(table).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("expired metadata remains in %T: count=%d err=%v", table, n, err)
		}
	}
	var liveClaim store.SpeechAgentClaimRequest
	if err := db.Where("request_id = ?", "live-request").First(&liveClaim).Error; err != nil {
		t.Fatalf("active claim receipt was removed: %v", err)
	}
	var expiredClaim store.SpeechAgentClaimRequest
	if err := db.Where("request_id = ?", "old-request").First(&expiredClaim).Error; err == nil {
		t.Fatal("expired claim receipt was retained")
	}
	var reservations []store.SpeechUploadReservation
	if err := db.Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 1 || reservations[0].ID != "live-upload" {
		t.Fatalf("cleanup removed live reservation or retained expired one: %#v", reservations)
	}
}

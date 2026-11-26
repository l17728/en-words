package store

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSpeechAgentProtocolTablesEnforceIdempotencyKeys(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "agents.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&SpeechAgent{}, &SpeechAgentEnrollment{}, &SpeechAgentClaimRequest{}, &SpeechAgentResultReceipt{}, &SpeechUploadReservation{}, &SpeechAgentRateWindow{}, &SpeechMaterialJob{}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	agent := SpeechAgent{ID: "agent-1", TokenHash: "hash-1", Enabled: true}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	claim := SpeechAgentClaimRequest{AgentID: agent.ID, RequestID: "request-1", JobID: 1, AttemptToken: "attempt-1", CreatedAt: time.Now()}
	if err := db.Create(&claim).Error; err != nil {
		t.Fatal(err)
	}
	duplicateClaim := SpeechAgentClaimRequest{AgentID: agent.ID, RequestID: claim.RequestID, JobID: 2, AttemptToken: "attempt-2"}
	if err := db.Create(&duplicateClaim).Error; err == nil {
		t.Fatal("duplicate agent request ID was accepted")
	}
	receipt := SpeechAgentResultReceipt{JobID: claim.JobID, AttemptToken: claim.AttemptToken, AgentID: agent.ID, Digest: "sha256-a", Accepted: true}
	if err := db.Create(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&SpeechAgentResultReceipt{JobID: receipt.JobID, AttemptToken: receipt.AttemptToken, AgentID: agent.ID, Digest: "sha256-b", Accepted: true}).Error; err == nil {
		t.Fatal("duplicate result receipt for one attempt was accepted")
	}
	window := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&SpeechAgentRateWindow{AgentID: agent.ID, WindowAt: window, Count: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&SpeechAgentRateWindow{AgentID: agent.ID, WindowAt: window, Count: 2}).Error; err == nil {
		t.Fatal("duplicate agent rate window was accepted")
	}
}

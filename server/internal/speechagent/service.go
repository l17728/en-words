package speechagent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"enwords/internal/store"
	"gorm.io/gorm"
)

var (
	ErrEnrollmentInvalid = errors.New("speech agent enrollment invalid")
	ErrAgentUnauthorized = errors.New("speech agent unauthorized")
	ErrClaimExpired      = errors.New("speech agent claim expired")
	ErrNoClaimAvailable  = errors.New("speech agent no task available")
)

const (
	enrollmentMinTTL = time.Minute
	enrollmentMaxTTL = 15 * time.Minute
	claimLease       = 60 * time.Second
)

type Service struct {
	DB  *gorm.DB
	Now func() time.Time
}

type Claim struct {
	JobID            int64     `json:"job_id"`
	ManifestRevision string    `json:"manifest_revision"`
	AttemptToken     string    `json:"attempt_token"`
	LeaseUntil       time.Time `json:"lease_until"`
	Language         string    `json:"language"`
	SpeechText       string    `json:"speech_text"`
	ServiceID        string    `json:"service_id"`
	Model            string    `json:"model"`
	Voice            string    `json:"voice"`
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) CreateEnrollment(ctx context.Context, adminID int64, capabilities []Capability, ttl time.Duration) (string, error) {
	if s.DB == nil || adminID <= 0 || ttl < enrollmentMinTTL || ttl > enrollmentMaxTTL {
		return "", errors.New("invalid speech agent enrollment request")
	}
	capsJSON, err := EncodeCapabilities(capabilities)
	if err != nil {
		return "", err
	}
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	id, err := NewToken()
	if err != nil {
		return "", err
	}
	row := store.SpeechAgentEnrollment{ID: id, TokenHash: TokenHash(token), CapabilitiesJSON: capsJSON, ExpiresAt: s.now().Add(ttl), CreatedBy: adminID, CreatedAt: s.now()}
	if err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := CleanupExpiredMetadata(tx, s.now()); err != nil {
			return err
		}
		return tx.Create(&row).Error
	}); err != nil {
		return "", errors.New("speech agent enrollment unavailable")
	}
	return token, nil
}

func (s Service) ExchangeEnrollment(ctx context.Context, token, displayName string) (store.SpeechAgent, string, error) {
	if s.DB == nil || strings.TrimSpace(displayName) == "" || len(strings.TrimSpace(displayName)) > 100 {
		return store.SpeechAgent{}, "", ErrEnrollmentInvalid
	}
	newToken, err := NewToken()
	if err != nil {
		return store.SpeechAgent{}, "", err
	}
	agentID, err := NewToken()
	if err != nil {
		return store.SpeechAgent{}, "", err
	}
	agentID = agentID[:22]
	now := s.now()
	var enrolled store.SpeechAgent
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := CleanupExpiredMetadata(tx, now); err != nil {
			return err
		}
		var e store.SpeechAgentEnrollment
		if err := tx.Where("token_hash = ? AND consumed_at IS NULL AND expires_at > ?", TokenHash(token), now).First(&e).Error; err != nil {
			return ErrEnrollmentInvalid
		}
		if _, err := DecodeCapabilities(e.CapabilitiesJSON); err != nil {
			return ErrEnrollmentInvalid
		}
		consumed := tx.Model(&store.SpeechAgentEnrollment{}).Where("id = ? AND consumed_at IS NULL AND expires_at > ?", e.ID, now).Update("consumed_at", now)
		if consumed.Error != nil || consumed.RowsAffected != 1 {
			return ErrEnrollmentInvalid
		}
		enrolled = store.SpeechAgent{ID: agentID, DisplayName: strings.TrimSpace(displayName), TokenHash: TokenHash(newToken), CapabilitiesJSON: e.CapabilitiesJSON, Enabled: true, CreatedAt: now, UpdatedAt: now}
		return tx.Create(&enrolled).Error
	})
	if err != nil {
		return store.SpeechAgent{}, "", err
	}
	return enrolled, newToken, nil
}

func DecodeCapabilities(raw string) ([]Capability, error) {
	var caps []Capability
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		return nil, errors.New("invalid speech agent capabilities")
	}
	canonical, err := NormalizeCapabilities(caps)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func (s Service) Authenticate(ctx context.Context, token string) (store.SpeechAgent, error) {
	if s.DB == nil || token == "" {
		return store.SpeechAgent{}, ErrAgentUnauthorized
	}
	var a store.SpeechAgent
	if err := s.DB.WithContext(ctx).Where("token_hash = ? AND enabled = ? AND revoked_at IS NULL", TokenHash(token), true).First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.SpeechAgent{}, ErrAgentUnauthorized
		}
		return store.SpeechAgent{}, err
	}
	if !VerifyToken(token, a.TokenHash) {
		return store.SpeechAgent{}, ErrAgentUnauthorized
	}
	return a, nil
}

func (s Service) Revoke(ctx context.Context, id string) error {
	if s.DB == nil || id == "" {
		return ErrAgentUnauthorized
	}
	now := s.now()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&store.SpeechAgent{}).Where("id = ? AND revoked_at IS NULL", id).Updates(map[string]any{"enabled": false, "revoked_at": now, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrAgentUnauthorized
		}
		if err := tx.Model(&store.SpeechMaterialJob{}).Where("agent_id = ? AND status = ?", id, "running").Updates(map[string]any{"status": "pending", "agent_id": "", "attempt_token": "", "lease_until": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&store.SpeechUploadReservation{}).Where("agent_id = ? AND status = ?", id, "active").Updates(map[string]any{"status": "rejected"}).Error
	})
}

func (s Service) RotateToken(ctx context.Context, id string) (string, error) {
	if s.DB == nil || id == "" {
		return "", ErrAgentUnauthorized
	}
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&store.SpeechAgent{}).Where("id = ? AND enabled = ? AND revoked_at IS NULL", id, true).Updates(map[string]any{"token_hash": TokenHash(token), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAgentUnauthorized
		}
		if err := tx.Model(&store.SpeechMaterialJob{}).Where("agent_id = ? AND status = ?", id, "running").Updates(map[string]any{"status": "pending", "agent_id": "", "attempt_token": "", "lease_until": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&store.SpeechUploadReservation{}).Where("agent_id = ? AND status = ?", id, "active").Updates(map[string]any{"status": "rejected"}).Error
	})
	if errors.Is(err, ErrAgentUnauthorized) {
		return "", err
	}
	if err != nil {
		return "", errors.New("speech agent token rotation failed")
	}
	return token, nil
}

// AgentIdentityCurrent rechecks the token identity captured by auth middleware.
// It fences requests authenticated before a concurrent token rotation.
func AgentIdentityCurrent(tx *gorm.DB, authenticated store.SpeechAgent) bool {
	return VerifyAgentIdentity(tx, authenticated) == nil
}

func VerifyAgentIdentity(tx *gorm.DB, authenticated store.SpeechAgent) error {
	if tx == nil || authenticated.ID == "" || len(authenticated.TokenHash) != sha256.Size*2 {
		return ErrAgentUnauthorized
	}
	var current store.SpeechAgent
	if err := tx.Where("id = ? AND enabled = ? AND revoked_at IS NULL", authenticated.ID, true).First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAgentUnauthorized
		}
		return err
	}
	if len(current.TokenHash) != len(authenticated.TokenHash) {
		return ErrAgentUnauthorized
	}
	if subtle.ConstantTimeCompare([]byte(current.TokenHash), []byte(authenticated.TokenHash)) != 1 {
		return ErrAgentUnauthorized
	}
	return nil
}

func (s Service) Claim(ctx context.Context, agent store.SpeechAgent, requestID string) (Claim, error) {
	if s.DB == nil || requestID == "" || len(requestID) > 100 {
		return Claim{}, ErrAgentUnauthorized
	}
	caps, err := DecodeCapabilities(agent.CapabilitiesJSON)
	if err != nil {
		return Claim{}, ErrAgentUnauthorized
	}
	now := s.now()
	var claim Claim
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := CleanupExpiredMetadata(tx, now); err != nil {
			return err
		}
		if err := VerifyAgentIdentity(tx, agent); err != nil {
			return err
		}
		var old store.SpeechAgentClaimRequest
		oldErr := tx.Where("agent_id = ? AND request_id = ?", agent.ID, requestID).First(&old).Error
		if oldErr == nil {
			if old.LeaseUntil == nil || !now.Before(*old.LeaseUntil) {
				return ErrClaimExpired
			}
			var job store.SpeechMaterialJob
			if err := tx.Where("id = ? AND status = ? AND attempt_token = ? AND agent_id = ?", old.JobID, "running", old.AttemptToken, agent.ID).First(&job).Error; err != nil {
				return ErrClaimExpired
			}
			if !ManifestCurrent(tx, job) {
				return ErrClaimExpired
			}
			claim = claimFrom(job)
			return nil
		}
		if !errors.Is(oldErr, gorm.ErrRecordNotFound) {
			return oldErr
		}
		// Recover abandoned work during the next claim. Recovery must not rely
		// on a student polling the progress endpoint after the worker vanished.
		if err := tx.Model(&store.SpeechMaterialJob{}).Where("status = ? AND lease_until IS NOT NULL AND lease_until <= ?", "running", now).Updates(map[string]any{"status": "pending", "agent_id": "", "attempt_token": "", "lease_until": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		var pending []store.SpeechMaterialJob
		if err := tx.Where("status = ? AND attempt < ?", "pending", 20).Order("created_at,id").Limit(100).Find(&pending).Error; err != nil {
			return err
		}
		for _, job := range pending {
			if !ManifestCurrent(tx, job) {
				continue
			}
			if !hasCapability(caps, job.ServiceID, job.Model, job.Language, job.Voice) {
				continue
			}
			token, err := NewToken()
			if err != nil {
				return err
			}
			lease := now.Add(claimLease)
			result := tx.Model(&store.SpeechMaterialJob{}).Where("id = ? AND status = ?", job.ID, "pending").Updates(map[string]any{"status": "running", "agent_id": agent.ID, "attempt_token": token, "lease_until": lease, "attempt": gorm.Expr("attempt + 1"), "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				continue
			}
			job.Status, job.AgentID, job.AttemptToken, job.LeaseUntil = "running", agent.ID, token, &lease
			job.Attempt++
			receipt := store.SpeechAgentClaimRequest{AgentID: agent.ID, RequestID: requestID, JobID: job.ID, AttemptToken: token, LeaseUntil: &lease, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&receipt).Error; err != nil {
				return err
			}
			claim = claimFrom(job)
			return nil
		}
		return ErrNoClaimAvailable
	})
	return claim, err
}

// CleanupExpiredMetadata bounds protocol bookkeeping without touching active
// jobs, accepted cache assets, or reservations that still have a live lease.
func CleanupExpiredMetadata(tx *gorm.DB, now time.Time) error {
	cutoff30 := now.Add(-30 * 24 * time.Hour)
	cutoff48 := now.Add(-48 * time.Hour)
	cutoff90 := now.Add(-90 * 24 * time.Hour)
	for _, op := range []func() error{
		func() error {
			return tx.Where("created_at < ?", cutoff30).Delete(&store.SpeechAgentResultReceipt{}).Error
		},
		func() error {
			// Claim receipts are idempotency keys. Keep them while their matching
			// job attempt is still live, even if metadata timestamps are old.
			if !tx.Migrator().HasTable(&store.SpeechMaterialJob{}) {
				return tx.Where("updated_at < ?", cutoff30).Delete(&store.SpeechAgentClaimRequest{}).Error
			}
			return tx.Where(`updated_at < ? AND NOT EXISTS (
				SELECT 1 FROM speech_material_jobs j
				WHERE j.id = speech_agent_claim_requests.job_id
				  AND j.attempt_token = speech_agent_claim_requests.attempt_token
				  AND j.status = 'running' AND j.lease_until > ?
			)`, cutoff30, now).Delete(&store.SpeechAgentClaimRequest{}).Error
		},
		func() error { return tx.Where("window_at < ?", cutoff48).Delete(&store.SpeechAgentRateWindow{}).Error },
		func() error { return tx.Where("expires_at < ?", cutoff90).Delete(&store.SpeechAgentEnrollment{}).Error },
		func() error { return tx.Where("expires_at < ?", now).Delete(&store.SpeechUploadReservation{}).Error },
	} {
		if err := op(); err != nil {
			return err
		}
	}
	return nil
}

func hasCapability(caps []Capability, serviceID, model, lang, voice string) bool {
	for _, c := range caps {
		if c.ServiceID == serviceID && c.Model == model && c.Language == lang && c.Voice == voice {
			return true
		}
	}
	return false
}

func claimFrom(job store.SpeechMaterialJob) Claim {
	if job.Voice == "" {
		job.Voice = "default"
	}
	lease := time.Time{}
	if job.LeaseUntil != nil {
		lease = *job.LeaseUntil
	}
	return Claim{JobID: job.ID, ManifestRevision: job.ManifestRevision, AttemptToken: job.AttemptToken, LeaseUntil: lease, Language: job.Language, SpeechText: job.SpeechText, ServiceID: job.ServiceID, Model: job.Model, Voice: job.Voice}
}

func (s Service) Renew(ctx context.Context, agent store.SpeechAgent, jobID int64, attempt string) (time.Time, error) {
	if s.DB == nil || agent.ID == "" || jobID <= 0 || attempt == "" {
		return time.Time{}, ErrAgentUnauthorized
	}
	now := s.now()
	next := now.Add(claimLease)
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := VerifyAgentIdentity(tx, agent); err != nil {
			return err
		}
		var job store.SpeechMaterialJob
		if err := tx.Where("id = ? AND agent_id = ? AND status = ? AND attempt_token = ? AND lease_until > ?", jobID, agent.ID, "running", attempt, now).First(&job).Error; err != nil {
			return ErrClaimExpired
		}
		if !ManifestCurrent(tx, job) {
			return ErrClaimExpired
		}
		result := tx.Model(&store.SpeechMaterialJob{}).Where("id = ? AND agent_id = ? AND status = ? AND attempt_token = ? AND lease_until > ?", jobID, agent.ID, "running", attempt, now).Updates(map[string]any{"lease_until": next, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrClaimExpired
		}
		updated := tx.Model(&store.SpeechAgentClaimRequest{}).Where("agent_id = ? AND attempt_token = ?", agent.ID, attempt).Update("lease_until", next)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrClaimExpired
		}
		return nil
	})
	return next, err
}

func ManifestCurrent(tx *gorm.DB, job store.SpeechMaterialJob) bool {
	if job.LessonID <= 0 {
		return true
	} // Legacy/unit fixtures without classroom manifests.
	var manifest store.SpeechMaterialManifest
	if err := tx.Where("lesson_id = ?", job.LessonID).First(&manifest).Error; err != nil {
		return false
	}
	return manifest.Revision == job.ManifestRevision
}

func ValidateRequestID(id string) error {
	if id == "" || len(id) > 100 || strings.TrimSpace(id) != id {
		return fmt.Errorf("invalid request id")
	}
	return nil
}

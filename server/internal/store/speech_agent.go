package store

import "time"

// SpeechAgent is a registered, revocable out-of-process TTS worker. Secrets
// are stored only as hashes; CapabilitiesJSON is the server-approved set.
type SpeechAgent struct {
	ID               string `gorm:"primaryKey"`
	DisplayName      string
	TokenHash        string `gorm:"uniqueIndex"`
	CapabilitiesJSON string
	Enabled          bool `gorm:"index"`
	LastSeenAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	RevokedAt        *time.Time
}

// SpeechAgentEnrollment is an administrator-issued, single-use enrollment
// credential bound to one service instance and an approved capability set.
type SpeechAgentEnrollment struct {
	ID               string `gorm:"primaryKey"`
	TokenHash        string `gorm:"uniqueIndex"`
	CapabilitiesJSON string
	ExpiresAt        time.Time `gorm:"index"`
	ConsumedAt       *time.Time
	CreatedBy        int64
	CreatedAt        time.Time
}

// SpeechAgentClaimRequest makes a lost next-response idempotent per agent.
type SpeechAgentClaimRequest struct {
	ID           int64  `gorm:"primaryKey"`
	AgentID      string `gorm:"uniqueIndex:idx_speech_claim_request"`
	RequestID    string `gorm:"uniqueIndex:idx_speech_claim_request"`
	JobID        int64  `gorm:"index"`
	AttemptToken string
	LeaseUntil   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SpeechAgentResultReceipt fences and idempotently records accepted audio.
type SpeechAgentResultReceipt struct {
	ID           int64  `gorm:"primaryKey"`
	JobID        int64  `gorm:"uniqueIndex:idx_speech_result_attempt"`
	AttemptToken string `gorm:"uniqueIndex:idx_speech_result_attempt"`
	AgentID      string `gorm:"index"`
	Digest       string
	Accepted     bool
	CreatedAt    time.Time
}

// SpeechUploadReservation durably accounts for an in-flight upload across
// processes and allows stale staging data to be reclaimed after a crash.
type SpeechUploadReservation struct {
	ID            string `gorm:"primaryKey"`
	AgentID       string `gorm:"index:idx_speech_upload_agent_expiry"`
	JobID         int64  `gorm:"index"`
	AttemptToken  string
	ReservedBytes int64
	ExpiresAt     time.Time `gorm:"index:idx_speech_upload_agent_expiry"`
	Status        string    `gorm:"index"`
	CreatedAt     time.Time
}

// SpeechAgentRateWindow is the cross-process request counter for a fixed
// agent/minute window.
type SpeechAgentRateWindow struct {
	ID        int64     `gorm:"primaryKey"`
	AgentID   string    `gorm:"uniqueIndex:idx_speech_rate_window"`
	WindowAt  time.Time `gorm:"uniqueIndex:idx_speech_rate_window"`
	Count     int
	UpdatedAt time.Time
}

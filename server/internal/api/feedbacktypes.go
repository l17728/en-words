package api

import "time"

type FeedbackSubmission struct {
	ParentSubmissionID string `json:"parent_submission_id"`
	SubmissionID       string `json:"submission_id"`
	Text               string `json:"text"`
	Channel            string `json:"channel"`
}
type FeedbackSubmissionError struct {
	Code         int
	ErrorCode    string
	Retryable    bool
	SubmissionID string
}

func (e *FeedbackSubmissionError) Error() string { return e.ErrorCode }

type FeedbackIntentResult struct {
	Index          int    `json:"index"`
	Kind           string `json:"kind"`
	Status         string `json:"status"`
	ResultCode     string `json:"result_code"`
	Reply          string `json:"reply"`
	Retryable      bool   `json:"retryable"`
	RefreshPending bool   `json:"refresh_pending"`
	Actual         any    `json:"actual"`
}
type FeedbackRecord struct {
	ParentSubmissionID    string                 `json:"parent_submission_id"`
	ConversationID        string                 `json:"conversation_id"`
	ClarificationAnswered bool                   `json:"clarification_answered"`
	ID                    int64                  `json:"id"`
	StudentID             int64                  `json:"student_id"`
	Text                  string                 `json:"text"`
	Status                string                 `json:"status"`
	Intents               []string               `json:"intents"`
	Reply                 string                 `json:"reply"`
	CreatedAt             time.Time              `json:"created_at"`
	UpdatedAt             time.Time              `json:"updated_at"`
	Version               int64                  `json:"version"`
	SubmissionID          string                 `json:"submission_id"`
	TaskID                string                 `json:"task_id"`
	Results               []FeedbackIntentResult `json:"results"`
	Retryable             bool                   `json:"retryable"`
	RefreshPending        bool                   `json:"refresh_pending"`
	Idempotency           string                 `json:"idempotency"`
	RetryAfterMs          int                    `json:"retry_after_ms"`
	ErrorCode             string                 `json:"error_code"`
}
type FeedbackAdminTodo struct {
	ID          int64     `json:"id"`
	TaskID      string    `json:"task_id"`
	IntentIndex int       `json:"intent_index"`
	StudentID   int64     `json:"student_id"`
	Text        string    `json:"text"`
	Status      string    `json:"status"`
	Reply       string    `json:"reply"`
	CreatedAt   time.Time `json:"created_at"`
	Legacy      bool      `json:"legacy"`
}

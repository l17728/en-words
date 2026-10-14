package api

import (
	"bytes"
	"context"
	"encoding/json"
	"enwords/internal/auth"
	"enwords/internal/store"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// Decode bounded JSON without silently accepting trailing values or malformed UTF-8.
func bindFeedbackJSON(c *gin.Context, out any, allowEmpty bool) error {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10))
	if err != nil {
		return err
	}
	raw = bytes.TrimSpace(raw)
	if allowEmpty && len(raw) == 0 {
		return nil
	}
	if len(raw) == 0 || raw[0] != '{' || !utf8.Valid(raw) {
		return errors.New("invalid_json")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range []string{"submission_id", "text", "channel", "parent_submission_id"} {
		if value, exists := fields[key]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("invalid_json")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(out); err != nil {
		return err
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return errors.New("trailing_json")
	}
	return nil
}

// Recheck persisted account state so a token cannot outlive an administrator's
// disable action in a different App instance. A missing persistent identity
// cannot authenticate using a stale in-memory copy.
func (a *App) activeAccount(ctx context.Context, id int64, role auth.Role, username string) bool {
	u, err := a.Auth.GetUserByID(id)
	if err != nil || u.Status != "active" || u.Role != role || (username != "" && u.Username != username) {
		return false
	}
	if a.DB != nil {
		var row store.User
		err = a.DB.WithContext(ctx).First(&row, id).Error
		if err == nil {
			return row.Status == "active" && row.Role == string(role) && row.Username == u.Username
		}
		return false
	}
	return true
}

func feedbackError(err error) gin.H {
	var typed *FeedbackSubmissionError
	if errors.As(err, &typed) {
		return gin.H{"code": typed.Code, "msg": typed.ErrorCode, "error_code": typed.ErrorCode, "submission_id": typed.SubmissionID, "retryable": typed.Retryable}
	}
	return gin.H{"code": 500, "msg": "反馈暂时不可用，请查询提交状态", "error_code": "feedback_unavailable", "retryable": true}
}

func (a *App) respondFeedbackError(c *gin.Context, err error) {
	response := feedbackError(err)
	a.Log.InfoCtx(c.Request.Context(), "feedback", "feedback_request_rejected", map[string]any{
		"student_id": claimsOf(c).UID, "route": c.FullPath(), "code": response["code"],
		"result_code": response["error_code"], "retryable": response["retryable"],
	})
	c.JSON(http.StatusOK, response)
}

func validVoiceText(text string) bool {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 2100 {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func (a *App) registerFeedbackRoutes(g *gin.RouterGroup) {
	g.POST("", func(c *gin.Context) {
		var in FeedbackSubmission
		if bindFeedbackJSON(c, &in, false) != nil {
			a.respondFeedbackError(c, &FeedbackSubmissionError{Code: 400, ErrorCode: "invalid_json"})
			return
		}
		rec, err := a.SubmitFeedback(c.Request.Context(), claimsOf(c).UID, in)
		if err != nil {
			a.respondFeedbackError(c, err)
			return
		}
		c.JSON(200, ok(rec))
	})
	g.GET("/mine", func(c *gin.Context) {
		rows, err := a.FeedbacksContext(c.Request.Context(), claimsOf(c).UID)
		if err != nil {
			a.respondFeedbackError(c, err)
			return
		}
		c.JSON(200, ok(rows))
	})
	g.GET("/submissions/:id", func(c *gin.Context) {
		rec, err := a.FeedbackSubmissionByID(c.Request.Context(), claimsOf(c).UID, c.Param("id"))
		if err != nil {
			a.respondFeedbackError(c, err)
			return
		}
		c.JSON(200, ok(rec))
	})
	g.POST("/submissions/:id/retry", func(c *gin.Context) {
		var in struct{}
		if bindFeedbackJSON(c, &in, true) != nil {
			a.respondFeedbackError(c, &FeedbackSubmissionError{Code: 400, ErrorCode: "invalid_retry", SubmissionID: c.Param("id")})
			return
		}
		rec, err := a.RetryFeedbackSubmission(c.Request.Context(), claimsOf(c).UID, c.Param("id"))
		if err != nil {
			a.respondFeedbackError(c, err)
			return
		}
		c.JSON(200, ok(rec))
	})
}

func (a *App) registerNotificationRoutes(g *gin.RouterGroup) {
	g.GET("/notifications", func(c *gin.Context) {
		rows, err := a.NotificationsContext(c.Request.Context(), claimsOf(c).UID)
		if err != nil {
			a.respondFeedbackError(c, err)
			return
		}
		c.JSON(200, ok(rows))
	})
	g.POST("/notifications/read", func(c *gin.Context) {
		if err := a.MarkNotificationReadContext(c.Request.Context(), claimsOf(c).UID, 0); err != nil {
			a.respondFeedbackError(c, err)
			return
		}
		c.JSON(200, ok("read"))
	})
}

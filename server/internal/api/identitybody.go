package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const identityBodyLimit = 16 << 10

// Identity endpoints accept one complete small JSON document. This is not a
// global request limit: imports and audio retain their own contracts.
func (a *App) readIdentityBody(c *gin.Context, out any) bool {
	reject := func(status int, reason string) bool {
		c.Header("Connection", "close")
		c.Request.Close = true
		a.Log.ErrorCtx(c.Request.Context(), "auth", "identity_body_rejected", map[string]any{
			"route": c.FullPath(), "reason": reason, "limit_bytes": identityBodyLimit})
		c.AbortWithStatusJSON(status, fail(status, "请求内容无效或接收超时"))
		return false
	}
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		// Direct Router/ResponseRecorder tests have no network connection.
		// net/http servers always attach LocalAddrContextKey, including httptest.Server.
		if !errors.Is(err, http.ErrNotSupported) || c.Request.Context().Value(http.LocalAddrContextKey) != nil {
			return reject(http.StatusServiceUnavailable, "deadline_unavailable")
		}
	}
	if c.Request.ContentLength > identityBodyLimit {
		return reject(http.StatusRequestEntityTooLarge, "body_too_large")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, identityBodyLimit)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var large *http.MaxBytesError
		var timeout net.Error
		if errors.As(err, &large) {
			return reject(http.StatusRequestEntityTooLarge, "body_too_large")
		}
		if errors.As(err, &timeout) && timeout.Timeout() {
			return reject(http.StatusRequestTimeout, "body_timeout")
		}
		return reject(http.StatusBadRequest, "body_read_failed")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, out) != nil {
		return reject(http.StatusBadRequest, "invalid_json")
	}
	// Only a fully consumed valid document can safely reuse this connection.
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		if !errors.Is(err, http.ErrNotSupported) || c.Request.Context().Value(http.LocalAddrContextKey) != nil {
			return reject(http.StatusServiceUnavailable, "deadline_reset_failed")
		}
	}
	return true
}

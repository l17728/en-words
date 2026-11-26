package api

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"enwords/internal/auth"
	"enwords/internal/obs"
	"enwords/internal/speechagent"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const speechAgentContextKey = "speech_agent"

func limitSpeechAgentJSON(c *gin.Context, limit int64) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
}

func (a *App) speechAgentService() (speechagent.Service, error) {
	if a.DB == nil {
		return speechagent.Service{}, errors.New("speech agent persistence unavailable")
	}
	a.speechAgentSchemaOnce.Do(func() {
		a.speechAgentSchemaErr = a.DB.AutoMigrate(&store.SpeechAsset{}, &store.SpeechMaterialJob{}, &store.SpeechMaterialManifest{}, &store.SpeechAgent{}, &store.SpeechAgentEnrollment{}, &store.SpeechAgentClaimRequest{}, &store.SpeechAgentResultReceipt{}, &store.SpeechUploadReservation{}, &store.SpeechAgentRateWindow{})
	})
	if a.speechAgentSchemaErr != nil {
		return speechagent.Service{}, errors.New("speech agent persistence unavailable")
	}
	return speechagent.Service{DB: a.DB, Now: a.Clock.Now}, nil
}

func (a *App) registerSpeechAgentRoutes(authed, v *gin.RouterGroup) {
	admin := authed.Group("/admin", a.roleMW(auth.RoleAdmin))
	admin.POST("/speech-agents/enrollments", a.createSpeechAgentEnrollment)
	admin.GET("/speech-agents", a.listSpeechAgents)
	admin.POST("/speech-agents/:id/revoke", a.revokeSpeechAgent)
	admin.POST("/speech-agents/:id/rotate", a.rotateSpeechAgent)

	v.POST("/speech-agent/enroll", a.speechAgentTLSOnly(), a.exchangeSpeechAgentEnrollment)
	agent := v.Group("/speech-agent", a.speechAgentTLSOnly(), a.speechAgentAuthMW())
	agent.POST("/heartbeat", a.speechAgentHeartbeat)
	agent.POST("/next", a.speechAgentNext)
	agent.POST("/jobs/:id/lease", a.speechAgentRenewLease)
	agent.POST("/jobs/:id/reservations", a.speechAgentCreateUploadReservation)
	agent.PUT("/jobs/:id/result", a.speechAgentUploadResult)
}

func (a *App) speechAgentTLSOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		secure := c.Request.TLS != nil
		if !secure && strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
			ip := net.ParseIP(host)
			secure = err == nil && ip != nil && ip.IsLoopback()
		}
		if !secure {
			c.AbortWithStatusJSON(http.StatusUpgradeRequired, fail(http.StatusUpgradeRequired, "HTTPS is required for speech agent access"))
			return
		}
		c.Next()
	}
}

func (a *App) createSpeechAgentEnrollment(c *gin.Context) {
	limitSpeechAgentJSON(c, 16<<10)
	var in struct {
		Capabilities []speechagent.Capability `json:"capabilities"`
		TTLSeconds   int                      `json:"ttl_seconds"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(http.StatusBadRequest, fail(errBadRequest, "invalid enrollment request"))
		return
	}
	if in.TTLSeconds == 0 {
		in.TTLSeconds = 300
	}
	svc, err := a.speechAgentService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent service unavailable"))
		return
	}
	token, err := svc.CreateEnrollment(c.Request.Context(), claimsOf(c).UID, in.Capabilities, time.Duration(in.TTLSeconds)*time.Second)
	if err != nil {
		c.JSON(http.StatusBadRequest, fail(errBadRequest, "invalid enrollment request"))
		return
	}
	a.Log.InfoCtx(c.Request.Context(), "speech_agent", "enrollment_created", map[string]any{"operator": claimsOf(c).UID, "capability_count": len(in.Capabilities), "ttl_seconds": in.TTLSeconds})
	c.JSON(http.StatusCreated, ok(gin.H{"enrollment_token": token, "expires_in_seconds": in.TTLSeconds}))
}

func (a *App) listSpeechAgents(c *gin.Context) {
	svc, err := a.speechAgentService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent service unavailable"))
		return
	}
	var rows []store.SpeechAgent
	if err := svc.DB.Order("created_at DESC,id").Find(&rows).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent list unavailable"))
		return
	}
	type agentView struct {
		ID           string                   `json:"id"`
		DisplayName  string                   `json:"display_name"`
		Capabilities []speechagent.Capability `json:"capabilities"`
		Enabled      bool                     `json:"enabled"`
		LastSeenAt   *time.Time               `json:"last_seen_at,omitempty"`
		CreatedAt    time.Time                `json:"created_at"`
		RevokedAt    *time.Time               `json:"revoked_at,omitempty"`
	}
	views := make([]agentView, 0, len(rows))
	for _, row := range rows {
		caps, _ := speechagent.DecodeCapabilities(row.CapabilitiesJSON)
		views = append(views, agentView{ID: row.ID, DisplayName: row.DisplayName, Capabilities: caps, Enabled: row.Enabled, LastSeenAt: row.LastSeenAt, CreatedAt: row.CreatedAt, RevokedAt: row.RevokedAt})
	}
	c.JSON(http.StatusOK, ok(views))
}

func (a *App) revokeSpeechAgent(c *gin.Context) {
	svc, err := a.speechAgentService()
	if err == nil {
		err = svc.Revoke(c.Request.Context(), c.Param("id"))
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, speechagent.ErrAgentUnauthorized) {
			status = http.StatusNotFound
		}
		c.JSON(status, fail(status, "speech agent revoke failed"))
		return
	}
	a.Log.InfoCtx(c.Request.Context(), "speech_agent", "agent_revoked", map[string]any{"operator": claimsOf(c).UID, "agent_id": c.Param("id")})
	c.JSON(http.StatusOK, ok(gin.H{"revoked": true}))
}

func (a *App) rotateSpeechAgent(c *gin.Context) {
	svc, err := a.speechAgentService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent service unavailable"))
		return
	}
	token, err := svc.RotateToken(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, fail(http.StatusNotFound, "speech agent token rotation failed"))
		return
	}
	a.Log.InfoCtx(c.Request.Context(), "speech_agent", "agent_token_rotated", map[string]any{"operator": claimsOf(c).UID, "agent_id": c.Param("id")})
	c.JSON(http.StatusOK, ok(gin.H{"agent_token": token}))
}

func (a *App) exchangeSpeechAgentEnrollment(c *gin.Context) {
	limitSpeechAgentJSON(c, 4<<10)
	var in struct {
		EnrollmentToken string `json:"enrollment_token"`
		DisplayName     string `json:"display_name"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(http.StatusBadRequest, fail(errBadRequest, "invalid enrollment"))
		return
	}
	svc, err := a.speechAgentService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent service unavailable"))
		return
	}
	agent, token, err := svc.ExchangeEnrollment(c.Request.Context(), in.EnrollmentToken, in.DisplayName)
	if err != nil {
		c.JSON(http.StatusUnauthorized, fail(http.StatusUnauthorized, "enrollment rejected"))
		return
	}
	caps, _ := speechagent.DecodeCapabilities(agent.CapabilitiesJSON)
	a.Log.InfoCtx(c.Request.Context(), "speech_agent", "agent_enrolled", map[string]any{"agent_id": agent.ID, "capability_count": len(caps)})
	c.JSON(http.StatusCreated, ok(gin.H{"agent_id": agent.ID, "agent_token": token, "capabilities": caps}))
}

func (a *App) speechAgentAuthMW() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || len(header) > 256 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, fail(http.StatusUnauthorized, "speech agent unauthorized"))
			return
		}
		svc, err := a.speechAgentService()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent service unavailable"))
			return
		}
		agent, err := svc.Authenticate(c.Request.Context(), strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, speechagent.ErrAgentUnauthorized) {
				status = http.StatusUnauthorized
			}
			c.AbortWithStatusJSON(status, fail(status, "speech agent authentication unavailable"))
			return
		}
		c.Set(speechAgentContextKey, agent)
		c.Next()
	}
}

func speechAgentFrom(c *gin.Context) store.SpeechAgent {
	if value, ok := c.Get(speechAgentContextKey); ok {
		if agent, ok := value.(store.SpeechAgent); ok {
			return agent
		}
	}
	return store.SpeechAgent{}
}

func (a *App) speechAgentHeartbeat(c *gin.Context) {
	limitSpeechAgentJSON(c, 1<<10)
	agent := speechAgentFrom(c)
	svc, err := a.speechAgentService()
	if err == nil {
		now := a.Clock.Now()
		err = svc.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			result := tx.Model(&store.SpeechAgent{}).Where("id = ? AND token_hash = ? AND enabled = ? AND revoked_at IS NULL", agent.ID, agent.TokenHash, true).Update("last_seen_at", now)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return speechagent.ErrAgentUnauthorized
			}
			return nil
		})
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, speechagent.ErrAgentUnauthorized) {
			status = http.StatusUnauthorized
		}
		c.JSON(status, fail(status, "speech agent heartbeat rejected"))
		return
	}
	c.JSON(http.StatusOK, ok(gin.H{"server_time": a.Clock.Now(), "agent_id": agent.ID}))
}

func (a *App) speechAgentNext(c *gin.Context) {
	limitSpeechAgentJSON(c, 1<<10)
	var in struct {
		RequestID string `json:"request_id"`
	}
	if c.ShouldBindJSON(&in) != nil || speechagent.ValidateRequestID(in.RequestID) != nil {
		c.JSON(http.StatusBadRequest, fail(errBadRequest, "invalid request id"))
		return
	}
	svc, err := a.speechAgentService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent service unavailable"))
		return
	}
	claim, err := svc.Claim(c.Request.Context(), speechAgentFrom(c), in.RequestID)
	if errors.Is(err, speechagent.ErrNoClaimAvailable) {
		c.Status(http.StatusNoContent)
		return
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, speechagent.ErrAgentUnauthorized) {
			status = http.StatusUnauthorized
		} else if errors.Is(err, speechagent.ErrClaimExpired) {
			status = http.StatusConflict
		}
		c.JSON(status, fail(status, "speech task claim rejected"))
		return
	}
	trace := obs.TraceID(c.Request.Context())
	a.Log.InfoCtx(c.Request.Context(), "speech_agent", "task_claimed", map[string]any{"agent_id": speechAgentFrom(c).ID, "job_id": claim.JobID, "trace_id": trace})
	c.JSON(http.StatusOK, ok(claim))
}

func (a *App) speechAgentRenewLease(c *gin.Context) {
	limitSpeechAgentJSON(c, 1<<10)
	var in struct {
		AttemptToken string `json:"attempt_token"`
	}
	if c.ShouldBindJSON(&in) != nil || in.AttemptToken == "" {
		c.JSON(http.StatusBadRequest, fail(errBadRequest, "invalid lease request"))
		return
	}
	jobID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || jobID <= 0 {
		c.JSON(http.StatusBadRequest, fail(errBadRequest, "invalid job id"))
		return
	}
	svc, err := a.speechAgentService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent service unavailable"))
		return
	}
	lease, err := svc.Renew(c.Request.Context(), speechAgentFrom(c), jobID, in.AttemptToken)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, speechagent.ErrAgentUnauthorized) {
			status = http.StatusUnauthorized
		} else if errors.Is(err, speechagent.ErrClaimExpired) {
			status = http.StatusConflict
		}
		c.JSON(status, fail(status, "speech task lease rejected"))
		return
	}
	c.JSON(http.StatusOK, ok(gin.H{"lease_until": lease}))
}

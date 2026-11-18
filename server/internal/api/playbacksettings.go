package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"enwords/internal/config"
	"enwords/internal/obs"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Playback settings are a deliberately narrow user capability, not a general
// configuration or Agent tool endpoint.
func decodePlaybackSettings(body io.Reader) (config.PlaybackSettings, bool, error) {
	var values config.PlaybackSettings
	raw, err := io.ReadAll(io.LimitReader(body, 4097))
	if err != nil || len(raw) > 4096 {
		return values, false, fmt.Errorf("invalid playback settings")
	}
	allowed := map[string]bool{
		"ttsEnRepeat": true, "ttsZhRepeat": true, "ttsIntervalMs": true,
		"ttsZhIntervalMs": true, "ttsSwitchIntervalMs": true, "showWordLeadMs": true,
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return values, false, fmt.Errorf("playback settings must be an object")
	}
	fields := make(map[string]json.RawMessage, 6)
	for d.More() {
		token, tokenErr := d.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok || !allowed[key] {
			return values, false, fmt.Errorf("invalid playback settings")
		}
		if _, duplicate := fields[key]; duplicate {
			return values, false, fmt.Errorf("invalid playback settings")
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return values, false, fmt.Errorf("playback settings must be non-null integers")
		}
		fields[key] = value
	}
	if end, tokenErr := d.Token(); tokenErr != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return values, false, fmt.Errorf("invalid playback settings")
	}
	legacyKeys := []string{"ttsEnRepeat", "ttsZhRepeat", "ttsIntervalMs", "ttsZhIntervalMs", "ttsSwitchIntervalMs"}
	hasLead := fields["showWordLeadMs"] != nil
	if len(fields) != len(legacyKeys) && !(len(fields) == len(legacyKeys)+1 && hasLead) {
		return values, false, fmt.Errorf("submit all playback settings")
	}
	for _, key := range legacyKeys {
		var number int
		value, exists := fields[key]
		if !exists || json.Unmarshal(value, &number) != nil {
			return values, false, fmt.Errorf("playback settings must be non-null integers")
		}
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return values, false, fmt.Errorf("invalid playback settings")
	}
	validation, _ := config.NewStore(nil, nil)
	if hasLead {
		err = validation.SetPlayback("validation", 0, true, values, "validate playback")
	} else {
		err = validation.SetPlaybackLegacy("validation", 0, true, values, "validate playback")
	}
	if err != nil {
		return values, false, err
	}
	return values, hasLead, nil
}

func (a *App) registerPlaybackRoutes(group *gin.RouterGroup, global bool) {
	scope := "student"
	if global {
		scope = "global"
	}
	studentID := func(c *gin.Context) int64 {
		if global {
			return 0
		}
		return claimsOf(c).UID
	}
	response := func(settings config.PlaybackSettings, pending bool) gin.H {
		return gin.H{"settings": settings, "scope": scope, "apply_from": "next_lesson", "refresh_pending": pending}
	}
	logFailure := func(c *gin.Context, code string) {
		a.Log.ErrorCtx(c.Request.Context(), "config", "playback_settings_failed", map[string]any{
			"student_id": studentID(c), "scope": scope, "operation": c.Request.Method, "error_code": code,
		})
	}
	group.GET("/playback-settings", func(c *gin.Context) {
		if len(c.Request.URL.Query()) != 0 {
			logFailure(c, "invalid_query")
			c.JSON(http.StatusOK, fail(errBadRequest, "playback settings do not accept query parameters"))
			return
		}
		sid := studentID(c)
		if a.DB == nil {
			c.JSON(http.StatusOK, ok(response(a.Cfg.Playback(sid), false)))
			return
		}
		a.agentWriteMu.Lock()
		defer a.agentWriteMu.Unlock()
		var settings config.PlaybackSettings
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			cfg, err := config.NewStore(store.NewConfigPersister(tx), nil)
			if err != nil {
				return err
			}
			settings = cfg.Playback(sid)
			return nil
		})
		if err != nil {
			logFailure(c, "read_failed")
			c.JSON(http.StatusOK, fail(500, "unable to read playback settings; retry shortly"))
			return
		}
		pending := a.Cfg.Playback(sid) != settings
		if pending {
			pending = a.Cfg.Reload() != nil || a.Cfg.Playback(sid) != settings
		}
		c.JSON(http.StatusOK, ok(response(settings, pending)))
	})
	group.POST("/playback-settings", func(c *gin.Context) {
		if len(c.Request.URL.Query()) != 0 {
			logFailure(c, "invalid_query")
			c.JSON(http.StatusOK, fail(errBadRequest, "playback settings do not accept query parameters"))
			return
		}
		values, hasLead, err := decodePlaybackSettings(c.Request.Body)
		if err != nil {
			logFailure(c, "invalid_settings")
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		ctx := c.Request.Context()
		sid := studentID(c)
		claims := claimsOf(c)
		a.agentWriteMu.Lock()
		defer a.agentWriteMu.Unlock()
		if err := ctx.Err(); err != nil {
			logFailure(c, "request_cancelled")
			c.JSON(http.StatusOK, fail(500, "request cancelled; settings were not saved"))
			return
		}
		var summary []byte
		if hasLead {
			summary, _ = json.Marshal(values)
		} else {
			summary, _ = json.Marshal(struct {
				TtsEnRepeat         int `json:"ttsEnRepeat"`
				TtsZhRepeat         int `json:"ttsZhRepeat"`
				TtsIntervalMs       int `json:"ttsIntervalMs"`
				TtsZhIntervalMs     int `json:"ttsZhIntervalMs"`
				TtsSwitchIntervalMs int `json:"ttsSwitchIntervalMs"`
			}{values.TtsEnRepeat, values.TtsZhRepeat, values.TtsIntervalMs, values.TtsZhIntervalMs, values.TtsSwitchIntervalMs})
		}
		write := func(cfg *config.Store) error {
			if hasLead {
				return cfg.SetPlayback(string(claims.Role), sid, global, values, "user saved playback settings")
			}
			return cfg.SetPlaybackLegacy(string(claims.Role), sid, global, values, "user saved playback settings")
		}
		if a.DB != nil {
			err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				cfg, err := config.NewStore(store.NewConfigPersister(tx), nil)
				if err != nil {
					return err
				}
				if err := write(cfg); err != nil {
					return err
				}
				return tx.Create(&store.AuditLog{TS: a.Clock.Now(), ActorID: claims.Username, ActorRole: string(claims.Role), Tool: "config.playback_set", Risk: "mid", OK: true, Result: scope, Args: string(summary), TraceID: obs.TraceID(ctx)}).Error
			})
		} else {
			err = write(a.Cfg)
		}
		if err != nil {
			logFailure(c, "save_failed")
			c.JSON(http.StatusOK, fail(500, "settings not saved; retry shortly"))
			return
		}
		pending := false
		if a.DB != nil {
			pending = a.Cfg.Reload() != nil
		}
		settings := a.Cfg.Playback(sid)
		a.Log.InfoCtx(ctx, "config", "playback_settings_saved", map[string]any{"student_id": sid, "scope": scope, "settings": settings, "refresh_pending": pending})
		c.JSON(http.StatusOK, ok(response(settings, pending)))
	})
}

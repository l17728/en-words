package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"enwords/internal/config"
	"enwords/internal/obs"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Playback settings are a deliberately narrow user capability, not a general
// configuration or Agent tool endpoint.
func decodePlaybackSettings(body io.Reader) (config.PlaybackSettings, error) {
	var values config.PlaybackSettings
	raw, err := io.ReadAll(io.LimitReader(body, 4097))
	if err != nil || len(raw) > 4096 {
		return values, fmt.Errorf("设置内容无效")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return values, fmt.Errorf("设置必须为JSON对象")
	}
	keys := []string{"ttsEnRepeat", "ttsZhRepeat", "ttsIntervalMs", "ttsZhIntervalMs", "ttsSwitchIntervalMs"}
	if len(fields) != len(keys) {
		return values, fmt.Errorf("请提交完整的五项朗读设置")
	}
	for _, key := range keys {
		value, ok := fields[key]
		var number int
		if !ok || strings.TrimSpace(string(value)) == "null" || json.Unmarshal(value, &number) != nil {
			return values, fmt.Errorf("朗读设置必须为整数，且不能为空")
		}
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return values, fmt.Errorf("设置内容无效")
	}
	validation, _ := config.NewStore(nil, nil)
	if err := validation.SetPlayback("validation", 0, true, values, "validate playback"); err != nil {
		return values, err
	}
	return values, nil
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
			c.JSON(http.StatusOK, fail(errBadRequest, "朗读设置不接受查询参数"))
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
			c.JSON(http.StatusOK, fail(500, "无法读取朗读设置，请稍后重试"))
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
			c.JSON(http.StatusOK, fail(errBadRequest, "朗读设置不接受查询参数"))
			return
		}
		values, err := decodePlaybackSettings(c.Request.Body)
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
			c.JSON(http.StatusOK, fail(500, "请求已取消，设置未保存"))
			return
		}
		summary, _ := json.Marshal(values)
		write := func(cfg *config.Store) error {
			return cfg.SetPlayback(string(claims.Role), sid, global, values, "用户保存朗读设置")
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
			c.JSON(http.StatusOK, fail(500, "朗读设置未保存，请稍后重试"))
			return
		}
		pending := false
		if a.DB != nil {
			pending = a.Cfg.Reload() != nil
		}
		a.Log.InfoCtx(ctx, "config", "playback_settings_saved", map[string]any{"student_id": sid, "scope": scope, "settings": values, "refresh_pending": pending})
		c.JSON(http.StatusOK, ok(response(values, pending)))
	})
}

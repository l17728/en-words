package api

import (
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"enwords/internal/auth"
	"enwords/internal/localmodel"
	"enwords/internal/speechagent"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func defaultModelProfile() store.ModelProfile {
	return store.ModelProfile{Name: "default", TextService: "vl", ASRService: "asr", TTSZhService: "tts", TTSEnService: "tts-en", Active: true}
}

func (a *App) ensureDefaultModelProfile() (store.ModelProfile, error) {
	p := defaultModelProfile()
	if a.DB == nil {
		return p, nil
	}
	var row store.ModelProfile
	if err := a.DB.Where("name = ?", p.Name).First(&row).Error; err != nil {
		if err := a.DB.Create(&p).Error; err != nil {
			return p, err
		}
		return p, nil
	}
	if row.TextService == "" {
		id, created, active := row.ID, row.CreatedAt, row.Active
		row = p
		row.ID, row.CreatedAt, row.Active = id, created, active
		if err := a.DB.Save(&row).Error; err != nil {
			return row, err
		}
	}
	if !row.Active {
		var active store.ModelProfile
		err := a.DB.Where("active = ?", true).Order("id ASC").First(&active).Error
		if err == nil {
			return active, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return row, err
		}
		row.Active = true
		if err := a.DB.Save(&row).Error; err != nil {
			return row, err
		}
	}
	return row, nil
}

func (a *App) applyModelProfile(row store.ModelProfile) error {
	if a.Local == nil {
		return nil
	}
	return a.Local.SetProfile(localmodel.Profile{Name: row.Name, Text: row.TextService, ASR: row.ASRService, TTSZh: row.TTSZhService, TTSEn: row.TTSEnService})
}

func (a *App) registerModelProfileRoutes(g *gin.RouterGroup) {
	admin := g.Group("/admin", a.roleMW(auth.RoleAdmin))
	admin.GET("/model-services", func(c *gin.Context) {
		servicesByKey := map[string]localmodel.Service{}
		if a.Local != nil {
			for _, s := range a.Local.Services {
				servicesByKey[s.ID+"\x00"+s.Model] = s
			}
		}
		if a.DB != nil && a.DB.Migrator().HasTable(&store.SpeechAgent{}) {
			var agents []store.SpeechAgent
			if a.DB.Where("enabled = ? AND revoked_at IS NULL", true).Find(&agents).Error == nil {
				for _, agent := range agents {
					caps, err := speechagent.DecodeCapabilities(agent.CapabilitiesJSON)
					if err != nil {
						continue
					}
					for _, cap := range caps {
						service := servicesByKey[cap.ServiceID+"\x00"+cap.Model]
						service.ID, service.Model = cap.ServiceID, cap.Model
						capName := "tts"
						if !slices.Contains(service.Capabilities, capName) {
							service.Capabilities = append(service.Capabilities, capName)
						}
						servicesByKey[cap.ServiceID+"\x00"+cap.Model] = service
					}
				}
			}
		}
		services := make([]localmodel.Service, 0, len(servicesByKey))
		for _, service := range servicesByKey {
			services = append(services, service)
		}
		sort.Slice(services, func(i, j int) bool {
			if services[i].ID != services[j].ID {
				return services[i].ID < services[j].ID
			}
			return services[i].Model < services[j].Model
		})
		c.JSON(http.StatusOK, ok(services))
	})
	admin.GET("/model-profiles", func(c *gin.Context) {
		if a.DB == nil {
			p := defaultModelProfile()
			_ = a.applyModelProfile(p)
			c.JSON(http.StatusOK, ok([]store.ModelProfile{p}))
			return
		}
		p, err := a.ensureDefaultModelProfile()
		if err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "model profile unavailable"))
			return
		}
		var rows []store.ModelProfile
		if err := a.DB.Order("active DESC, name ASC").Find(&rows).Error; err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "model profile unavailable"))
			return
		}
		if len(rows) == 0 {
			rows = []store.ModelProfile{p}
		}
		var active store.ModelProfile
		for _, row := range rows {
			if row.Active {
				active = row
				break
			}
		}
		if active.ID != 0 {
			_ = a.applyModelProfile(active)
		}
		c.JSON(http.StatusOK, ok(rows))
	})
	admin.POST("/model-profiles", func(c *gin.Context) {
		var in store.ModelProfile
		if c.ShouldBindJSON(&in) != nil || strings.TrimSpace(in.Name) == "" {
			c.JSON(http.StatusOK, fail(errBadRequest, "invalid model profile"))
			return
		}
		if a.Local != nil {
			if err := a.Local.SetProfile(localmodel.Profile{Name: in.Name, Text: in.TextService, ASR: in.ASRService, TTSZh: in.TTSZhService, TTSEn: in.TTSEnService}); err != nil {
				c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
				return
			}
		}
		if a.DB == nil {
			c.JSON(http.StatusOK, ok(in))
			return
		}
		var row store.ModelProfile
		if err := a.DB.Where("name = ?", in.Name).First(&row).Error; err == nil {
			in.ID = row.ID
			in.CreatedAt = row.CreatedAt
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "model profile unavailable"))
			return
		}
		in.UpdatedAt = row.UpdatedAt
		if err := a.DB.Save(&in).Error; err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "model profile save failed"))
			return
		}
		a.Log.InfoCtx(c.Request.Context(), "admin", "model_profile_saved", map[string]any{"profile_id": in.ID, "name": in.Name, "operator": claimsOf(c).UID})
		c.JSON(http.StatusOK, ok(in))
	})
	admin.POST("/model-profiles/:id/apply", func(c *gin.Context) {
		if a.DB == nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "persistence unavailable"))
			return
		}
		var row store.ModelProfile
		if err := a.DB.First(&row, c.Param("id")).Error; err != nil {
			c.JSON(http.StatusOK, fail(errNotFound, "model profile not found"))
			return
		}
		if err := a.applyModelProfile(row); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&store.ModelProfile{}).Where("1 = 1").Update("active", false).Error; err != nil {
				return err
			}
			return tx.Model(&store.ModelProfile{}).Where("id = ?", row.ID).Update("active", true).Error
		}); err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "model profile apply failed"))
			return
		}
		a.Log.InfoCtx(c.Request.Context(), "admin", "model_profile_applied", map[string]any{"profile_id": row.ID, "name": row.Name, "operator": claimsOf(c).UID})
		c.JSON(http.StatusOK, ok(row))
	})
}

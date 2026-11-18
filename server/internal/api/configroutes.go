package api

import (
	"bytes"
	"encoding/json"
	"enwords/internal/auth"
	"enwords/internal/config"
	"enwords/internal/store"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"reflect"
	"strconv"
)

func decodeConfigBody(c *gin.Context, out any) error {
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, 8193))
	if err != nil || len(data) > 8192 {
		return fmt.Errorf("invalid configuration input")
	}
	// encoding/json accepts duplicate and case-folded struct fields. Reject
	// these before binding so the mutation has one unambiguous target/value.
	allowed := map[string]bool{}
	typ := reflect.TypeOf(out).Elem()
	for i := 0; i < typ.NumField(); i++ {
		allowed[typ.Field(i).Tag.Get("json")] = true
	}
	keys := json.NewDecoder(bytes.NewReader(data))
	start, err := keys.Token()
	if err != nil || start != json.Delim('{') {
		return fmt.Errorf("invalid configuration input")
	}
	seen := map[string]bool{}
	for keys.More() {
		tok, err := keys.Token()
		key, valid := tok.(string)
		if err != nil || !valid || !allowed[key] || seen[key] {
			return fmt.Errorf("invalid configuration input")
		}
		seen[key] = true
		var value json.RawMessage
		if keys.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("invalid configuration input")
		}
	}
	if end, err := keys.Token(); err != nil || end != json.Delim('}') {
		return fmt.Errorf("invalid configuration input")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("invalid configuration input")
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid configuration input")
	}
	return nil
}
func (a *App) configTarget(c *gin.Context, sid int64) bool {
	if sid == 0 {
		return true
	}
	if sid < 0 {
		return false
	}
	if a.DB != nil {
		var u store.User
		if err := a.DB.WithContext(c.Request.Context()).First(&u, sid).Error; err != nil {
			return false
		}
		return u.Role == string(auth.RoleStudent)
	}
	u, err := a.Auth.GetUserByID(sid)
	return err == nil && u.Role == auth.RoleStudent
}
func (a *App) registerEffectiveConfigRoutes(g *gin.RouterGroup) {
	g.GET("", func(c *gin.Context) {
		out := gin.H{}
		err := a.readCurrentConfig(c.Request.Context(), func(cfg *config.Store) error {
			for _, k := range []string{"wordsPerLesson", "groupSize", "ttsEnRepeat", "repeatTimes", "quizOldWords", "overdueHours", "goalMode", "ladder", "sessionTimeoutMin"} {
				switch k {
				case "goalMode":
					out[k] = cfg.String(0, k)
				case "ladder":
					out[k] = cfg.Ladder(0)
				default:
					out[k] = cfg.Int(0, k)
				}
			}
			return nil
		})
		if err != nil {
			c.JSON(200, fail(503, "配置暂不可读取"))
			return
		}
		c.JSON(200, ok(out))
	})
	g.GET("/effective", func(c *gin.Context) {
		var sid int64
		query := c.Request.URL.Query()
		if values, present := query["studentId"]; present {
			if len(values) != 1 || values[0] == "" {
				c.JSON(200, fail(400, "无效学生目标"))
				return
			}
			var err error
			sid, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || sid < 0 {
				c.JSON(200, fail(400, "无效学生目标"))
				return
			}
		}
		if !a.configTarget(c, sid) {
			c.JSON(200, fail(400, "无效学生目标"))
			return
		}
		out, err := a.EffectiveConfiguration(c.Request.Context(), sid)
		if err != nil {
			c.JSON(200, fail(503, "配置暂不可读取"))
			return
		}
		c.JSON(200, ok(out))
	})
	g.POST("/set", func(c *gin.Context) {
		var in struct {
			Scope     *string         `json:"scope"`
			StudentID *int64          `json:"studentId"`
			Key       *string         `json:"key"`
			Value     json.RawMessage `json:"value"`
			Reason    *string         `json:"reason"`
		}
		if decodeConfigBody(c, &in) != nil || in.Scope == nil || in.Key == nil || in.Reason == nil || len(in.Value) == 0 || string(in.Value) == "null" {
			c.JSON(200, fail(400, "配置输入不完整"))
			return
		}
		sid := int64(0)
		if in.StudentID != nil {
			sid = *in.StudentID
		}
		if (*in.Scope == "student" && sid <= 0) || (*in.Scope == "global" && sid != 0) || !a.configTarget(c, sid) {
			c.JSON(200, fail(400, "无效学生目标"))
			return
		}
		var value any
		if json.Unmarshal(in.Value, &value) != nil {
			c.JSON(200, fail(400, "配置值无效"))
			return
		}
		m := config.Mutation{Operation: "set", Actor: claimsOf(c).Username, Scope: config.Scope(*in.Scope), StudentID: sid, Key: *in.Key, Value: value, Reason: *in.Reason}
		a.respondConfigMutation(c, m)
	})
	g.DELETE("/override", func(c *gin.Context) {
		var in struct {
			StudentID *int64  `json:"studentId"`
			Key       *string `json:"key"`
		}
		if decodeConfigBody(c, &in) != nil || in.StudentID == nil || *in.StudentID <= 0 || in.Key == nil || !a.configTarget(c, *in.StudentID) {
			c.JSON(200, fail(400, "配置输入不完整"))
			return
		}
		a.respondConfigMutation(c, config.Mutation{Operation: "delete_override", Actor: claimsOf(c).Username, Scope: config.ScopeStudent, StudentID: *in.StudentID, Key: *in.Key, Reason: "删除覆盖，回落继承"})
	})
}
func (a *App) respondConfigMutation(c *gin.Context, m config.Mutation) {
	if err := config.ValidateMutation(m); err != nil {
		c.JSON(200, fail(400, "无效配置目标或参数"))
		return
	}
	out, err := a.ChangeConfig(c.Request.Context(), m)
	if err != nil {
		c.JSON(200, fail(503, "配置未保存，请刷新核对后重试"))
		return
	}
	c.JSON(200, ok(out))
}

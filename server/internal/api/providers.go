package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"enwords/internal/auth"
	"enwords/internal/feedback"
	"enwords/internal/judge"
	"enwords/internal/localmodel"
	"enwords/internal/obs"
	"github.com/gin-gonic/gin"
)

func (a *App) EnableLocalProviders(ctx context.Context) error {
	c, err := localmodel.New(os.Getenv("ENWORDS_MODEL_CONSOLE"), a.Log)
	if err != nil {
		return err
	}
	if err = c.Discover(ctx); err != nil {
		return err
	}
	// Verify structured output before accepting traffic; audio is independently optional.
	var ping struct {
		OK bool `json:"ok"`
	}
	if err = c.ChatJSON(ctx, "Return only JSON.", `Return {"ok":true}.`, &ping); err != nil {
		return err
	}
	if !ping.OK {
		return fmtProviderHealth
	}
	a.Local = c
	a.Judge.LLM = judge.NewZhipu(c)
	a.FB.SetLLM(feedback.NewZhipuParser(c))
	return nil
}

var fmtProviderHealth = &providerHealthError{}

type providerHealthError struct{}

func (*providerHealthError) Error() string { return "model console: structured health check failed" }

func (a *App) traceMW() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		ctx := obs.WithTrace(c.Request.Context(), obs.NewTraceID())
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Trace-ID", obs.TraceID(ctx))
		c.Next()
		a.Log.InfoCtx(ctx, "http", "request_completed", map[string]any{"method": c.Request.Method, "route": c.FullPath(), "status": c.Writer.Status(), "duration_ms": time.Since(start).Milliseconds()})
	}
}

func (a *App) ownLesson(c *gin.Context, id int64) bool {
	l, err := a.Sched.Lesson(id)
	if err != nil || l.StudentID != claimsOf(c).UID {
		c.AbortWithStatusJSON(http.StatusOK, fail(errForbidden, "lesson unavailable"))
		return false
	}
	return true
}

func (a *App) registerProviderRoutes(g *gin.RouterGroup) {
	g.GET("/providers", func(c *gin.Context) {
		local := a.Local != nil
		c.JSON(200, ok(gin.H{"local": local, "asr": local && a.Local.Has("asr", "asr"), "tts_zh": local && a.Local.Has("tts", "tts"), "tts_en": false}))
	})
	g.POST("/classroom/lessons/:id/transcribe", a.roleMW(auth.RoleStudent), func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		if !a.ownLesson(c, id) {
			return
		}
		if a.Local == nil {
			c.JSON(503, fail(503, "本机语音识别不可用，请使用文字输入"))
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, localmodel.MaxAudio+(64<<10))
		if err := c.Request.ParseMultipartForm(localmodel.MaxAudio); err != nil {
			c.JSON(400, fail(400, "音频过大或格式错误"))
			return
		}
		defer c.Request.MultipartForm.RemoveAll()
		f, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(400, fail(400, "缺少音频"))
			return
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, localmodel.MaxAudio+1))
		if err != nil || len(raw) > localmodel.MaxAudio {
			c.JSON(400, fail(400, "音频过大"))
			return
		}
		lang := c.PostForm("language")
		if lang != "en" && lang != "zh" {
			c.JSON(400, fail(400, "language 必须为 en 或 zh"))
			return
		}
		// Do not accept user-controlled paths as an upstream filename.
		name := "recording.webm"
		if header.Header.Get("Content-Type") == "audio/wav" {
			name = "recording.wav"
		}
		text, err := a.Local.Transcribe(c.Request.Context(), raw, name, lang)
		if err != nil {
			c.JSON(503, fail(503, "语音识别失败，请使用文字输入"))
			return
		}
		a.Log.InfoCtx(c.Request.Context(), "speech", "audio_transcribed", map[string]any{"lesson_id": id, "student_id": claimsOf(c).UID, "text_length": len([]rune(text))})
		c.JSON(200, ok(gin.H{"text": sanitizeText(text)}))
	})
	g.POST("/speech/synthesize", func(c *gin.Context) {
		if a.Local == nil {
			c.JSON(503, fail(503, "本机语音合成不可用"))
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		var in struct {
			Text string `json:"text"`
			Lang string `json:"lang"`
		}
		if c.ShouldBindJSON(&in) != nil || in.Lang != "zh" || len([]rune(in.Text)) > 300 {
			c.JSON(400, fail(400, "仅支持短中文朗读"))
			return
		}
		raw, err := a.Local.Synthesize(c.Request.Context(), in.Text, in.Lang)
		if err != nil {
			c.JSON(503, fail(503, "语音合成失败"))
			return
		}
		c.Data(200, "audio/wav", raw)
	})
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type RecentLessonStats struct {
	LessonID     int64     `json:"lesson_id"`
	BookID       int64     `json:"book_id"`
	FinishedAt   time.Time `json:"finished_at"`
	TotalWords   int       `json:"total_words"`
	FirstTryRate float64   `json:"first_try_rate"`
}
type StudentStats struct {
	Learned         int                 `json:"learned"`
	Graduated       int                 `json:"graduated"`
	Remaining       int                 `json:"remaining"`
	PlacementKnown  int                 `json:"placement_known"`
	FinishedLessons int                 `json:"finished_lessons"`
	StreakDays      int                 `json:"streak_days"`
	Timezone        string              `json:"timezone"`
	RecentLessons   []RecentLessonStats `json:"recent_lessons"`
}

func (a *App) StudentStatistics(ctx context.Context, sid int64) (*StudentStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := a.Clock.Now()
	var result *StudentStats
	read := func(svc *scheduler.Service, report func(int64) (*classroom.Result, error)) error {
		cards, lessons, err := svc.StatisticsData(sid)
		if err != nil {
			return err
		}
		out := &StudentStats{Timezone: "Asia/Shanghai", RecentLessons: make([]RecentLessonStats, 0)}
		for _, card := range cards {
			switch card.Status {
			case scheduler.StatusNew:
				out.Remaining++
			case scheduler.StatusLearning:
				out.Learned++
			case scheduler.StatusGraduated:
				out.Learned++
				out.Graduated++
			case scheduler.StatusMastered:
				out.PlacementKnown++
			}
		}
		zone := time.FixedZone("Asia/Shanghai", 8*60*60)
		days := map[string]bool{}
		finished := make([]*scheduler.Lesson, 0)
		for _, lesson := range lessons {
			if lesson.Status != scheduler.LessonFinishedStr || lesson.FinishedAt == nil || lesson.FinishedAt.After(now) {
				continue
			}
			finished = append(finished, lesson)
			days[lesson.FinishedAt.In(zone).Format("2006-01-02")] = true
		}
		out.FinishedLessons = len(finished)
		day := now.In(zone)
		if !days[day.Format("2006-01-02")] {
			day = day.AddDate(0, 0, -1)
		}
		for days[day.Format("2006-01-02")] {
			out.StreakDays++
			day = day.AddDate(0, 0, -1)
		}
		sort.Slice(finished, func(i, j int) bool {
			if finished[i].FinishedAt.Equal(*finished[j].FinishedAt) {
				return finished[i].ID > finished[j].ID
			}
			return finished[i].FinishedAt.After(*finished[j].FinishedAt)
		})
		if len(finished) > 20 {
			finished = finished[:20]
		}
		for _, lesson := range finished {
			res, err := report(lesson.ID)
			if err != nil {
				return err
			}
			if res == nil || res.Report.TotalWords <= 0 || math.IsNaN(res.Report.FirstTryRate) || math.IsInf(res.Report.FirstTryRate, 0) || res.Report.FirstTryRate < 0 || res.Report.FirstTryRate > 1 {
				return fmt.Errorf("invalid statistics report")
			}
			out.RecentLessons = append(out.RecentLessons, RecentLessonStats{LessonID: lesson.ID, BookID: lesson.BookID, FinishedAt: *lesson.FinishedAt, TotalWords: res.Report.TotalWords, FirstTryRate: res.Report.FirstTryRate})
		}
		result = out
		return nil
	}
	var err error
	if a.DB != nil {
		err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return read(scheduler.NewService(store.NewSchedulerRepo(tx), a.Cfg, a.Clock), func(id int64) (*classroom.Result, error) {
				var row store.RuntimeRecord
				if err := tx.Where("key=?", fmt.Sprintf("report/%d", id)).First(&row).Error; err != nil {
					return nil, err
				}
				var res classroom.Result
				if err := json.Unmarshal([]byte(row.Value), &res); err != nil {
					return nil, err
				}
				return &res, nil
			})
		})
	} else {
		err = read(a.Sched, func(id int64) (*classroom.Result, error) {
			res, ok := a.Report(id)
			if !ok {
				return nil, fmt.Errorf("statistics report missing")
			}
			return res, nil
		})
	}
	if err != nil {
		a.Log.ErrorCtx(ctx, "stats", "stats_load_failed", map[string]any{"student_id": sid, "error_code": "statistics_unavailable"})
		return nil, err
	}
	a.Log.InfoCtx(ctx, "stats", "stats_loaded", map[string]any{"student_id": sid, "finished_lessons": result.FinishedLessons, "learned": result.Learned})
	return result, nil
}

func (a *App) registerStatsRoutes(student *gin.RouterGroup) {
	student.GET("/stats", func(c *gin.Context) {
		if len(c.Request.URL.Query()) != 0 {
			c.JSON(http.StatusOK, fail(errBadRequest, "统计接口不接受查询参数"))
			return
		}
		result, err := a.StudentStatistics(c.Request.Context(), claimsOf(c).UID)
		if err != nil {
			c.JSON(http.StatusOK, fail(500, "学习统计暂时不可用，请稍后重试"))
			return
		}
		c.JSON(http.StatusOK, ok(result))
	})
}

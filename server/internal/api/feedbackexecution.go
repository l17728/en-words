package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"enwords/internal/agent"
	"enwords/internal/obs"
	"enwords/internal/store"
	"enwords/internal/tools"
	"errors"
	"gorm.io/gorm/clause"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var feedbackSubmissionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func normalizeFeedback(in FeedbackSubmission) (FeedbackSubmission, error) {
	if in.ParentSubmissionID != "" && (!feedbackSubmissionID.MatchString(in.ParentSubmissionID) || in.ParentSubmissionID == in.SubmissionID) {
		return in, feedbackErr(400, "invalid_parent_submission_id", false)
	}
	if in.SubmissionID != "" && !feedbackSubmissionID.MatchString(in.SubmissionID) {
		return in, feedbackErr(400, "invalid_submission_id", false)
	}
	if in.Channel == "" {
		in.Channel = "page"
	}
	if in.Channel != "page" && in.Channel != "voice" {
		return in, feedbackErr(400, "invalid_channel", false)
	}
	if !utf8.ValidString(in.Text) {
		return in, feedbackErr(400, "invalid_text", false)
	}
	in.Text = strings.TrimSpace(strings.ReplaceAll(in.Text, "\r\n", "\n"))
	if in.Text == "" || utf8.RuneCountInString(in.Text) > 2000 {
		return in, feedbackErr(400, "invalid_text", false)
	}
	for _, r := range in.Text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return in, feedbackErr(400, "invalid_text", false)
		}
	}
	return in, nil
}
func (a *App) SubmitFeedback(ctx context.Context, sid int64, input FeedbackSubmission) (*FeedbackRecord, error) {
	if a.DB == nil && (!a.Cfg.SupportsMemoryTransactions() || !a.Sched.SupportsMemoryTransactions() || !a.Mem.SupportsMemoryTransactions()) {
		return nil, feedbackErr(409, "atomic_execution_unavailable", false)
	}
	in, err := normalizeFeedback(input)
	if err != nil {
		return nil, err
	}
	if err = a.feedbackActive(ctx, sid, a.DB); err != nil {
		return nil, err
	}
	if in.ParentSubmissionID != "" {
		if _, err := a.validateClarificationParent(ctx, sid, in.ParentSubmissionID); err != nil {
			return nil, err
		}
	}
	generated := in.SubmissionID == ""
	if generated {
		in.SubmissionID = obs.NewTraceID()
	}
	hash := sha256.Sum256([]byte(in.Text))
	now := a.Clock.Now()
	task := store.FeedbackTask{StudentID: sid, SubmissionID: in.SubmissionID, TaskID: "feedback_" + obs.NewTraceID(), SchemaVersion: 1, InputText: in.Text, InputHash: hex.EncodeToString(hash[:]), Channel: in.Channel, Idempotency: "client", Version: 1, CreatedAt: now, UpdatedAt: now}
	task.ParentSubmissionID = in.ParentSubmissionID
	if generated {
		task.Idempotency = "server_generated"
	}
	created := false
	if a.DB != nil {
		res := a.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "student_id"}, {Name: "submission_id"}}, DoNothing: true}).Create(&task)
		if res.Error != nil {
			return nil, &FeedbackSubmissionError{503, "storage_unavailable", true, in.SubmissionID}
		}
		created = res.RowsAffected == 1
	} else {
		a.mu.Lock()
		mem := a.feedbackMemoryLocked()
		exists := false
		for _, t := range mem.Tasks {
			if t.StudentID == sid && t.SubmissionID == in.SubmissionID {
				exists = true
				break
			}
		}
		if !exists {
			a.nextFeedbackID++
			task.ID = a.nextFeedbackID
			mem.Tasks[task.TaskID] = task
			created = true
		}
		a.mu.Unlock()
	}
	saved, intents, err := a.feedbackRead(ctx, sid, in.SubmissionID)
	if err != nil {
		return nil, err
	}
	if saved.InputHash != task.InputHash || saved.InputText != in.Text || saved.ParentSubmissionID != in.ParentSubmissionID {
		a.feedbackLog(ctx, saved, "", "feedback_conflict", -1, "submission_conflict", "none", 0)
		return nil, &FeedbackSubmissionError{409, "submission_conflict", false, in.SubmissionID}
	}
	if !created {
		a.feedbackLog(ctx, saved, "", "feedback_replayed", -1, "replayed", "none", 0)
		return feedbackRecord(saved, intents), nil
	}
	a.feedbackLog(ctx, saved, "", "feedback_task_created", -1, "created", "committed", 0)
	return a.runFeedback(ctx, saved)
}
func (a *App) FeedbackSubmissionByID(ctx context.Context, sid int64, id string) (*FeedbackRecord, error) {
	if !feedbackSubmissionID.MatchString(id) {
		return nil, feedbackErr(400, "invalid_submission_id", false)
	}
	if err := a.feedbackActive(ctx, sid, a.DB); err != nil {
		return nil, err
	}
	t, its, err := a.feedbackRead(ctx, sid, id)
	if err != nil {
		return nil, err
	}
	refreshPending := a.refreshFeedbackConfig(ctx, t, its)
	t, its, err = a.feedbackRead(ctx, sid, id)
	if err != nil {
		return nil, err
	}
	r := feedbackRecord(t, its)
	if refreshPending {
		r.RefreshPending = true
		r.Reply += " 设置已保存，运行配置刷新待恢复；请勿重复提交调整。"
		if r.Status == "applied" {
			r.Status = "applied_refresh_pending"
		}
		for i := range r.Results {
			f, e := decodeFrozen(its[i])
			if e == nil && f.Mode == "config" && r.Results[i].Status == "applied" {
				r.Results[i].RefreshPending = true
			}
		}
	}
	return r, nil
}
func (a *App) RetryFeedbackSubmission(ctx context.Context, sid int64, id string) (*FeedbackRecord, error) {
	if !feedbackSubmissionID.MatchString(id) {
		return nil, feedbackErr(400, "invalid_submission_id", false)
	}
	if err := a.feedbackActive(ctx, sid, a.DB); err != nil {
		return nil, err
	}
	t, its, err := a.feedbackRead(ctx, sid, id)
	if err != nil {
		return nil, err
	}
	if !feedbackRecord(t, its).Retryable {
		return a.FeedbackSubmissionByID(ctx, sid, id)
	}
	return a.runFeedback(ctx, t)
}
func (a *App) runFeedback(parent context.Context, task store.FeedbackTask) (*FeedbackRecord, error) {
	attempt := obs.NewTraceID()
	ctx := parent
	if obs.TraceID(ctx) == "" {
		ctx = obs.WithTrace(ctx, attempt)
	}
	ctx = context.WithValue(ctx, agentTaskKey{}, &agentTask{app: a, studentID: task.StudentID, taskID: task.TaskID, trigger: "feedback_submitted", role: tools.RoleSystemAgent})
	ctx = tools.WithEventBudget(ctx, 8)
	a.feedbackLog(ctx, task, attempt, "feedback_retry", -1, "started", "none", 0)
	invalidPlan := false
	if err := a.withFeedbackWork(ctx, task.TaskID, func(w *feedbackWork) error {
		invalidPlan = w.task.SchemaVersion != 1
		for i, row := range w.intents {
			_, e := decodeFrozen(row)
			invalidPlan = invalidPlan || e != nil || row.IntentIndex != i
		}
		if !invalidPlan {
			return nil
		}
		w.task.ErrorCode, w.task.Retryable = "invalid_plan", false
		for i := range w.intents {
			row := &w.intents[i]
			if row.CompletedAt != nil {
				continue
			}
			result := FeedbackIntentResult{Index: row.IntentIndex, Kind: row.Kind, Status: "failed", ResultCode: "invalid_plan", Reply: feedbackFailureReply("invalid_plan", false)}
			encoded, e := json.Marshal(result)
			if e != nil {
				return e
			}
			row.Status, row.ResultJSON = "failed", string(encoded)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if invalidPlan {
		a.feedbackLog(ctx, task, attempt, "feedback_plan_rejected", -1, "invalid_plan", "committed", 0)
		return a.FeedbackSubmissionByID(ctx, task.StudentID, task.SubmissionID)
	}
	if task.PlanJSON == "" {
		parsed, err := a.FB.ParseContextAt(ctx, task.InputText, task.CreatedAt)
		var plan []feedbackFrozenIntent
		if err == nil {
			acts := a.System.Decide(agent.SystemInput{Trigger: "feedback_submitted", StudentID: task.StudentID, FeedbackText: task.InputText, ParsedFeedback: &parsed, FeedbackAnchor: task.CreatedAt, ClarificationReply: task.ParentSubmissionID != ""})
			plan, err = compileFeedbackPlan(parsed, acts)
		}
		if err == nil {
			err = a.withFeedbackWork(ctx, task.TaskID, func(w *feedbackWork) error {
				if w.task.PlanJSON != "" {
					return nil
				}
				return w.freezeFeedbackPlan(plan)
			})
		}
		if err != nil {
			var parentErr *FeedbackSubmissionError
			if errors.As(err, &parentErr) && (parentErr.ErrorCode == "clarification_parent_unavailable" || parentErr.ErrorCode == "clarification_already_answered") {
				if cleanup := a.discardUnplannedFeedback(context.WithoutCancel(ctx), task); cleanup != nil {
					return nil, cleanup
				}
				return nil, err
			}
			saveErr := a.withFeedbackWork(context.WithoutCancel(ctx), task.TaskID, func(w *feedbackWork) error {
				if w.task.PlanJSON == "" {
					w.task.ErrorCode = feedbackErrorCode(err)
					w.task.Retryable = feedbackRetryable(err)
				}
				return nil
			})
			if saveErr != nil {
				return nil, &FeedbackSubmissionError{503, "storage_unavailable", true, task.SubmissionID}
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			t, it, e := a.feedbackRead(context.WithoutCancel(ctx), task.StudentID, task.SubmissionID)
			if e != nil {
				return nil, e
			}
			return feedbackRecord(t, it), nil
		}
		a.feedbackLog(ctx, task, attempt, "feedback_plan_frozen", -1, "frozen", "committed", 0)
		if task.ParentSubmissionID != "" {
			a.feedbackLog(ctx, task, attempt, "feedback_clarification_linked", -1, "linked", "committed", 0)
		}
	}
	current, initial, err := a.feedbackRead(ctx, task.StudentID, task.SubmissionID)
	if err != nil {
		return nil, err
	}
	task = current
	for _, item := range initial {
		if ctx.Err() != nil {
			break
		}
		start := time.Now()
		buffer := &tools.MemAuditor{}
		skipped := false
		escalated := false
		err = a.withFeedbackWork(ctx, task.TaskID, func(w *feedbackWork) error {
			if item.IntentIndex < 0 || item.IntentIndex >= len(w.intents) {
				return feedbackErr(400, "invalid_plan", false)
			}
			row := &w.intents[item.IntentIndex]
			var old FeedbackIntentResult
			_ = json.Unmarshal([]byte(row.ResultJSON), &old)
			if row.CompletedAt != nil || (row.Status == "failed" && !old.Retryable) {
				skipped = true
				return nil
			}
			f, e := decodeFrozen(*row)
			if e != nil {
				return e
			}
			if f.Failure != "" {
				return feedbackErr(409, f.Failure, false)
			}
			if f.DependsOn >= 0 {
				dep := w.intents[f.DependsOn]
				if dep.CompletedAt == nil || dep.Status != "applied" {
					var predecessor FeedbackIntentResult
					_ = json.Unmarshal([]byte(dep.ResultJSON), &predecessor)
					retry := dep.CompletedAt == nil && (dep.Status == "pending" || (dep.Status == "failed" && predecessor.Retryable))
					return feedbackErr(409, "dependency_failed", retry)
				}
			}
			if f.Mode == "config" {
				rev, e := w.cfg.Revisions(task.StudentID, f.Key)
				if e != nil {
					return e
				}
				if rev != f.Revision || w.cfg.Int(task.StudentID, f.Key) != f.Before {
					return feedbackErr(409, "stale_target", false)
				}
			}
			limitFacts, e := w.enforceFeedbackLimits(ctx, &f)
			if e != nil {
				return e
			}
			result := FeedbackIntentResult{Index: f.Index, Kind: f.Kind, Status: "applied", ResultCode: "applied", Actual: limitFacts}
			session := &feedbackToolSession{work: w, frozen: f, result: &result}
			svc := agentServices{config: w.cfg, scheduler: w.sched, memory: w.memory, feedback: session, transactional: w.db != nil}
			reg := tools.New(buffer)
			if e := a.registerAgentTools(reg, svc); e != nil {
				return e
			}
			if e := a.registerFeedbackTools(reg, svc); e != nil {
				return e
			}
			caller := tools.Caller{Role: tools.RoleSystemAgent, ID: string(tools.RoleSystemAgent)}
			for _, name := range f.Calls {
				var args any = map[string]any{"reason": "feedback intent"}
				switch name {
				case "config.set_student":
					args = map[string]any{"key": f.Key, "value": f.Target, "reason": "学生反馈调整"}
				case "memory.append":
					args = map[string]any{"scope": "student_profile", "kind": f.MemoryKind, "content": f.MemoryContent, "importance": f.MemoryImportance}
				}
				raw, _ := json.Marshal(args)
				out, e := reg.Call(ctx, caller, name, raw)
				if e != nil {
					return e
				}
				if name == "config.set_student" {
					_ = out
					result.Actual = map[string]any{"key": f.Key, "value": w.cfg.Int(task.StudentID, f.Key)}
				}
			}
			result.ResultCode = result.Status
			escalated = result.Status == "escalated"
			result.Retryable = false
			row.Status = result.Status
			row.AttemptCount++
			row.CompletedAt = feedbackNowPtr(a.Clock.Now())
			b, _ := json.Marshal(result)
			row.ResultJSON = string(b)
			if w.db != nil {
				if e := persistToolAudits(w.db, buffer.Snapshot(), false); e != nil {
					return e
				}
			}
			rec := feedbackRecord(w.task, w.intents)
			return w.notice(-1, task.StudentID, "feedback_result", "反馈处理结果", rec.Reply)
		})
		if skipped {
			continue
		}
		if err != nil {
			code, retry := feedbackErrorCode(err), feedbackRetryable(err)
			persistErr := a.withFeedbackWork(context.WithoutCancel(ctx), task.TaskID, func(w *feedbackWork) error {
				if item.IntentIndex >= len(w.intents) {
					return feedbackErr(400, "invalid_plan", false)
				}
				row := &w.intents[item.IntentIndex]
				if row.CompletedAt != nil {
					return nil
				}
				r := FeedbackIntentResult{Index: row.IntentIndex, Kind: row.Kind, Status: "failed", ResultCode: code, Reply: feedbackFailureReply(code, retry), Retryable: retry}
				raw, _ := json.Marshal(r)
				row.ResultJSON = string(raw)
				row.Status = "failed"
				row.AttemptCount++
				if !retry {
					row.CompletedAt = feedbackNowPtr(a.Clock.Now())
				}
				if w.db != nil {
					if e := persistToolAudits(w.db, buffer.Snapshot(), true); e != nil {
						return e
					}
				}
				rec := feedbackRecord(w.task, w.intents)
				return w.notice(-1, task.StudentID, "feedback_result", "反馈处理结果", rec.Reply)
			})
			a.feedbackLog(ctx, task, attempt, "feedback_intent_rolled_back", item.IntentIndex, code, "rolled_back", time.Since(start))
			if persistErr != nil {
				a.feedbackLog(ctx, task, attempt, "audit_persist_failed", item.IntentIndex, "storage_unavailable", "failed", time.Since(start))
				return nil, &FeedbackSubmissionError{503, "storage_unavailable", true, task.SubmissionID}
			}
		} else {
			a.feedbackLog(ctx, task, attempt, "feedback_intent_committed", item.IntentIndex, "committed", "committed", time.Since(start))
			if escalated {
				a.feedbackLog(ctx, task, attempt, "feedback_admin_escalated", item.IntentIndex, "escalated", "committed", time.Since(start))
			}
		}
		for _, entry := range buffer.Snapshot() {
			if err != nil {
				entry.OK = false
				entry.Result = "rolled_back_" + entry.Result
			}
			a.ToolAudit.ToolCalled(entry)
		}
	}
	return a.FeedbackSubmissionByID(context.WithoutCancel(ctx), task.StudentID, task.SubmissionID)
}
func feedbackFailureReply(code string, retryable bool) string {
	switch code {
	case "plan_budget_exceeded":
		return "一次反馈的调整事项太多，请拆成几次提交。"
	case "invalid_plan", "unknown_action":
		return "这次反馈未能形成可执行的调整方案，请说明具体目标后重新提交。"
	case "atomic_execution_unavailable":
		return "当前存储配置不支持安全执行这些调整，请联系管理员检查服务配置。"
	case "canceled":
		if retryable {
			return "反馈处理已中断，已完成的调整会保留；可恢复未完成项。"
		}
		return "反馈处理已中断，已完成的调整会保留。"
	case "stale_target":
		return "设置或课程已被其他操作修改，本项未应用，请确认当前状态后新提交。"
	case "word_not_enrolled":
		return "未找到已选择词库中的该词，本项未加强。"
	case "lesson_not_found":
		return "未找到可改期课程，本项未改期。"
	case "invalid_date", "schedule_conflict":
		return "日期不可用或课程时间冲突，本项未改期。"
	case "admin_unavailable":
		return "暂无有效管理员，尚未转交。"
	case "dependency_failed":
		if retryable {
			return "前置调整未完成，本项尚未应用；可恢复未完成项。"
		}
		return "前置调整无法应用，请确认调整目标后重新提交。"
	case "resource_unavailable", "book_not_enrolled", "book_unavailable":
		return "原课程或词库已不可用，本项未应用，请确认当前资源后重新提交。"
	}
	if retryable {
		return "本项暂未保存，可稍后恢复未完成项。"
	}
	return "本项未应用，请确认调整目标后重新提交。"
}
func (a *App) feedbackLog(ctx context.Context, t store.FeedbackTask, attempt, event string, index int, code, outcome string, duration time.Duration) {
	fields := map[string]any{"task_id": t.TaskID, "attempt_id": attempt, "student_id": t.StudentID, "intent_index": index, "result_code": code, "transaction_outcome": outcome, "duration_ms": duration.Milliseconds()}
	var plan []feedbackFrozenIntent
	if json.Unmarshal([]byte(t.PlanJSON), &plan) == nil && index >= 0 && index < len(plan) {
		f := plan[index]
		fields["tool_names"] = f.Calls
		fields["action"] = f.Mode
		fields["lesson_id"] = f.LessonID
		fields["book_id"] = f.BookID
		fields["resources"] = f.Words
		if f.Mode == "config" {
			fields["config_key"] = f.Key
			fields["target"] = f.Target
		}
	}
	a.Log.InfoCtx(ctx, "feedback", event, fields)
}
func (a *App) refreshFeedbackConfig(ctx context.Context, t store.FeedbackTask, its []store.FeedbackIntent) bool {
	if a.DB == nil {
		return false
	}
	hasConfig := false
	pending := false
	for _, it := range its {
		f, e := decodeFrozen(it)
		if e == nil && f.Mode == "config" && it.Status == "applied" {
			hasConfig = true
			var r FeedbackIntentResult
			_ = json.Unmarshal([]byte(it.ResultJSON), &r)
			pending = pending || r.RefreshPending
		}
	}
	if !hasConfig {
		return false
	}
	failed := a.Cfg.Reload() != nil
	if failed == pending {
		return failed
	}
	err := a.withFeedbackWork(ctx, t.TaskID, func(w *feedbackWork) error {
		for i := range w.intents {
			it := &w.intents[i]
			f, e := decodeFrozen(*it)
			if e == nil && f.Mode == "config" && it.Status == "applied" {
				var r FeedbackIntentResult
				_ = json.Unmarshal([]byte(it.ResultJSON), &r)
				r.RefreshPending = failed
				b, _ := json.Marshal(r)
				it.ResultJSON = string(b)
			}
		}
		return nil
	})
	if failed {
		a.feedbackLog(ctx, t, "", "feedback_refresh_pending", -1, "refresh_pending", "committed", 0)
	}
	if err != nil {
		a.feedbackLog(ctx, t, "", "feedback_refresh_pending", -1, "refresh_state_unavailable", "failed", 0)
	}
	return failed || err != nil
}

// Compile-time assertions keep the error distinction discoverable to callers.
var _ error = (*FeedbackSubmissionError)(nil)

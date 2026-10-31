//go:build cgo

package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"enwords/internal/wordbook"

	"gorm.io/gorm"
)

const (
	firstAudioFixtureBookID    int64 = 9101
	firstAudioFixtureWordIDOne int64 = 9201
	firstAudioFixtureWordIDTwo int64 = 9202
	firstAudioFixtureLessonID  int64 = 9301
)

type firstAudioFixtureIdentity struct {
	Username  string
	StudentID int64
	BookID    int64
	WordIDs   []int64
	LessonID  int64
}

// seedFirstAudioSetupFixture is deliberately restricted to the setup pool.
// The returned login password is transient and is never stored in the database.
func seedFirstAudioSetupFixture(db *gorm.DB, capability []byte, runID string, now time.Time) (firstAudioFixtureIdentity, string, error) {
	username, password, err := firstAudioSyntheticLogin(capability, runID)
	if err != nil {
		return firstAudioFixtureIdentity{}, "", err
	}

	authService := auth.NewService("first-audio-matrix-test-only", time.Hour)
	var studentID int64
	for _, account := range []struct {
		name string
		role auth.Role
	}{
		{name: "teacher-agent", role: auth.RoleTeacherAgent},
		{name: "system-agent", role: auth.RoleSystemAgent},
		{name: username, role: auth.RoleStudent},
	} {
		secret := password
		if account.role != auth.RoleStudent {
			secret = "matrix-internal-" + account.name
		}
		created, err := authService.CreateUser(account.name, secret, account.name, account.role)
		if err != nil {
			return firstAudioFixtureIdentity{}, "", err
		}
		if account.role == auth.RoleStudent {
			studentID = created.ID
		}
		if err := store.NewAuthStore(db).SaveUser(authService, account.name); err != nil {
			return firstAudioFixtureIdentity{}, "", err
		}
	}

	book := store.WordBook{
		ID: firstAudioFixtureBookID, Name: "first-audio-matrix-v1", Grade: "g7",
		Description: "Synthetic first-audio fixture", Source: "test-only",
		Status: string(wordbook.StatusPublished), CreatedBy: "teacher-agent",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&book).Error; err != nil {
		return firstAudioFixtureIdentity{}, "", err
	}
	bookRepo := store.NewWordbookRepo(db)
	entries := []*wordbook.Entry{
		{ID: firstAudioFixtureWordIDOne, BookID: firstAudioFixtureBookID, Word: "cat", NormKey: "cat", Meanings: []string{"猫"}, Unit: "matrix", FreqRank: 1},
		{ID: firstAudioFixtureWordIDTwo, BookID: firstAudioFixtureBookID, Word: "dog", NormKey: "dog", Meanings: []string{"狗"}, Unit: "matrix", FreqRank: 2},
	}
	for _, entry := range entries {
		if err := bookRepo.CreateEntry(entry); err != nil {
			return firstAudioFixtureIdentity{}, "", err
		}
	}
	lesson := &scheduler.Lesson{
		ID: firstAudioFixtureLessonID, StudentID: studentID, BookID: firstAudioFixtureBookID,
		Status: scheduler.LessonScheduledStr, OpenAt: now.Add(-time.Minute),
		BatchWordIDs: []int64{firstAudioFixtureWordIDOne, firstAudioFixtureWordIDTwo},
	}
	if err := store.NewSchedulerRepo(db).CreateLesson(lesson); err != nil {
		return firstAudioFixtureIdentity{}, "", err
	}
	identity := firstAudioFixtureIdentity{
		Username: username, StudentID: studentID, BookID: firstAudioFixtureBookID,
		WordIDs: []int64{firstAudioFixtureWordIDOne, firstAudioFixtureWordIDTwo}, LessonID: firstAudioFixtureLessonID,
	}
	return identity, password, nil
}

func TestFirstAudioMatrixSetupFixtureIsDeterministicAndLoginReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal("setup pool did not open", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	capability := firstAudioTestCapability()
	runID := "fixture_20261027_1"
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	identity, password, err := seedFirstAudioSetupFixture(db, capability, runID, now)
	if err != nil {
		_ = pool.Close()
		t.Fatal("fixture seed failed", err)
	}
	if identity.StudentID <= 0 || identity.BookID != firstAudioFixtureBookID || identity.LessonID != firstAudioFixtureLessonID {
		_ = pool.Close()
		t.Fatalf("fixture identity changed: %#v", identity)
	}
	if len(identity.WordIDs) != 2 || identity.WordIDs[0] != firstAudioFixtureWordIDOne || identity.WordIDs[1] != firstAudioFixtureWordIDTwo {
		_ = pool.Close()
		t.Fatalf("fixture word ordering changed: %#v", identity.WordIDs)
	}

	app := NewApp(scheduler.RealClock{}, db)
	if app.StartupError != nil {
		_ = pool.Close()
		t.Fatalf("app startup restored an error: %v", app.StartupError)
	}
	user, err := app.Auth.GetUser(identity.Username)
	if err != nil || user.ID != identity.StudentID || user.Role != auth.RoleStudent {
		_ = pool.Close()
		t.Fatalf("student was not restored by NewApp: user=%#v err=%v", user, err)
	}
	if _, _, err := app.Auth.Login(identity.Username, password); err != nil {
		_ = pool.Close()
		t.Fatalf("derived fixture credentials do not log in: %v", err)
	}
	var persisted store.User
	if err := db.Where("username = ?", identity.Username).First(&persisted).Error; err != nil {
		_ = pool.Close()
		t.Fatal("synthetic student credential row missing", err)
	}
	if persisted.Hash == password || persisted.Salt == "" || persisted.Hash == "" {
		_ = pool.Close()
		t.Fatal("fixture persisted a plaintext or incomplete student credential")
	}
	for _, item := range []struct {
		model any
		want  int64
	}{
		{model: &store.User{}, want: 3},
		{model: &store.WordBook{}, want: 1},
		{model: &store.WordEntry{}, want: 2},
		{model: &store.Lesson{}, want: 1},
	} {
		var count int64
		if err := db.Model(item.model).Count(&count).Error; err != nil || count != item.want {
			_ = pool.Close()
			t.Fatalf("setup fixture was not isolated: model=%T count=%d want=%d err=%v", item.model, count, item.want, err)
		}
	}
	for _, name := range []string{"teacher-agent", "system-agent"} {
		if _, err := app.Auth.GetUser(name); err != nil {
			_ = pool.Close()
			t.Fatalf("internal account %q missing after startup: %v", name, err)
		}
	}

	var lesson store.Lesson
	if err := db.First(&lesson, identity.LessonID).Error; err != nil {
		_ = pool.Close()
		t.Fatal("synthetic lesson missing", err)
	}
	var wordIDs []int64
	if err := json.Unmarshal([]byte(lesson.BatchWordIDs), &wordIDs); err != nil || len(wordIDs) != 2 || wordIDs[0] != identity.WordIDs[0] || wordIDs[1] != identity.WordIDs[1] {
		_ = pool.Close()
		t.Fatalf("lesson word list is invalid: %q err=%v", lesson.BatchWordIDs, err)
	}
	for _, model := range []any{&store.Notification{}, &store.Feedback{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
			_ = pool.Close()
			t.Fatalf("startup legacy state was not empty: model=%T count=%d err=%v", model, count, err)
		}
	}

	if err := pool.Close(); err != nil {
		t.Fatal("setup pool close failed", err)
	}
	if err := pool.PingContext(context.Background()); err == nil {
		t.Fatalf("closed setup pool remained usable: %v", err)
	}
}

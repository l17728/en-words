package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/classroom"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDuplexDeferredEffectsFlushOnlyAfterCommit(t *testing.T) {
	_, _, _, room := setupRoom(t)
	client := &fakeClient{}
	room.Join(client)
	client.frames = nil // Ignore the initial room snapshot.
	require.Equal(t, classroom.PhaseShowWord, room.Machine().Phase())

	before := room.durableSnapshot()
	room.beginDeferredEffects()
	frames := room.HandleEvent(classroom.Event{Type: classroom.EvWordClicked})
	require.Empty(t, frames, "the fast classroom transition must not emit uncommitted TTS")
	require.Empty(t, client.frames, "clients must not receive deferred effects before commit")
	firstQueued := len(room.deferredActions)
	require.Len(t, room.deferredActions, 1)
	require.Equal(t, classroom.ActPlayTTS, room.deferredActions[0].Type)
	require.Equal(t, 1, room.deferredActions[0].SegIdx)

	// A second transition before the receipt commit must append to the same
	// queue rather than replace the first segment.
	frames = room.HandleEvent(classroom.Event{Type: classroom.EvTTSFinished})
	require.Empty(t, frames)
	require.Empty(t, client.frames)
	require.Len(t, room.deferredActions, firstQueued+1)
	require.Equal(t, classroom.ActPlayTTS, room.deferredActions[1].Type)
	require.Equal(t, 2, room.deferredActions[1].SegIdx)

	committed := room.durableSnapshot()
	require.NotEqual(t, before.Phase, committed.Phase)
	flushed := room.flushDeferredEffects(context.Background())
	require.NotEmpty(t, flushed, "committed effects should be released")
	require.NotEmpty(t, client.frames, "committed effects should be broadcast")
	firstFlushFrameCount := len(client.frames)
	require.False(t, room.deferEffects)
	require.Empty(t, room.deferredActions)
	require.Empty(t, room.flushDeferredEffects(context.Background()), "a repeated flush must be idempotent")
	require.Len(t, client.frames, firstFlushFrameCount, "a repeated flush must not replay audio")
}

func TestJoinSnapshotPrecedesRecoveryFramesWithCurrentContext(t *testing.T) {
	_, _, _, room := setupRoom(t)
	client := &fakeClient{}
	room.Join(client)
	require.NotEmpty(t, client.frames)
	require.Equal(t, "state_snapshot", client.frames[0]["type"], "the fresh connection must receive its snapshot before playback")
	snapshotContext := client.frames[0]["context"]
	lastSeq := client.frames[0]["seq"].(int)
	for _, frame := range client.frames[1:] {
		require.Greater(t, frame["seq"].(int), lastSeq, "recovery frames must have a sequence newer than the snapshot")
		lastSeq = frame["seq"].(int)
		if frame["context"] != nil {
			require.Equal(t, snapshotContext, frame["context"], "recovery frames must use the context already published in the snapshot")
		}
	}
}

func TestDuplexDeferredEffectsDiscardAndRestoreAfterCommitFailure(t *testing.T) {
	_, _, _, room := setupRoom(t)
	client := &fakeClient{}
	room.Join(client)
	client.frames = nil
	require.Equal(t, classroom.PhaseShowWord, room.Machine().Phase())

	before := room.durableSnapshot()
	room.beginDeferredEffects()
	frames := room.HandleEvent(classroom.Event{Type: classroom.EvWordClicked})
	require.Empty(t, frames)
	require.NotEmpty(t, room.deferredActions)

	room.discardDeferredEffects()
	require.False(t, room.deferEffects)
	require.Empty(t, room.deferredActions)
	require.NoError(t, room.restoreDurableSnapshot(before))
	require.Equal(t, before.Phase, room.Machine().Phase())
	require.Empty(t, client.frames, "a failed commit must not leak audio or teacher effects")
}

func TestRestoreDurableSnapshotRejectsInvalidStateWithoutMutation(t *testing.T) {
	_, _, _, room := setupRoom(t)
	before := room.durableSnapshot()
	invalid := before
	invalid.Phase = "invalid-phase"

	err := room.restoreDurableSnapshot(invalid)
	require.Error(t, err)
	require.Equal(t, before, room.durableSnapshot(), "failed recovery must leave the live room untouched")
}

func TestNextDuplexInputRevisionAdvancesEvenWhenClassroomTurnDoesNot(t *testing.T) {
	first, ok := nextDuplexInputRevision(7, 0)
	require.True(t, ok)
	require.Equal(t, uint64(8), first)
	second, ok := nextDuplexInputRevision(7, first)
	require.True(t, ok)
	require.Equal(t, uint64(9), second, "a second audio attempt on the same classroom turn needs a distinct receipt key")
	reconnected, ok := nextDuplexInputRevision(8, second)
	require.True(t, ok)
	require.Equal(t, uint64(10), reconnected, "reconnects retain monotonicity within the room epoch")
	_, ok = nextDuplexInputRevision(^uint64(0), 0)
	require.False(t, ok, "revision overflow must fail closed")
}

func TestRoomDuplexInputRevisionSurvivesConnectionReplacement(t *testing.T) {
	_, _, _, room := setupRoom(t)
	room.mu.Lock()
	room.turnVersion = 10
	first, ok := room.nextDuplexInputRevisionLocked()
	require.True(t, ok)
	second, ok := room.nextDuplexInputRevisionLocked() // same context, new stream on same socket
	require.True(t, ok)
	room.turnVersion++ // Join/reconnect advances the classroom turn but keeps the same Room epoch.
	reconnected, ok := room.nextDuplexInputRevisionLocked()
	room.mu.Unlock()
	require.True(t, ok)
	require.Equal(t, uint64(11), first)
	require.Equal(t, uint64(12), second)
	require.Equal(t, uint64(13), reconnected, "the Room-level counter prevents receipt collisions across replacement WebSockets")
}

func TestRestoredRoomStartsANewDuplexEpoch(t *testing.T) {
	app, _, lessonID, room := setupRoom(t)
	room.mu.Lock()
	oldEpoch := room.contextLocked().Epoch
	studentID := room.studentID
	room.mu.Unlock()
	require.NotEmpty(t, oldEpoch)
	require.NoError(t, app.Rooms.Drop(lessonID))
	restored, _, err := app.Rooms.Enter(lessonID, studentID)
	require.NoError(t, err)
	restored.mu.Lock()
	newEpoch := restored.contextLocked().Epoch
	restored.mu.Unlock()
	require.NotEmpty(t, newEpoch)
	require.NotEqual(t, oldEpoch, newEpoch, "restoring a Room must not reuse duplex receipt keys from its previous epoch")
}

func TestDeferredVoiceIngressDoesNotBroadcastSuccessBeforeCommit(t *testing.T) {
	app, clock := newTestApp(t)
	adminToken, _ := loginGet(t, app, "admin", "adminpw")
	bookID := seedBook(t, app, adminToken, "duplex-confirm", "g7", []string{"cat"})
	_, err := app.Auth.CreateUser("duplex-student", "pw", "student", auth.RoleStudent)
	require.NoError(t, err)
	_, studentID := loginGet(t, app, "duplex-student", "pw")
	require.NoError(t, app.EnableBook(studentID, bookID))
	require.NoError(t, app.PlacementSubmit(studentID, bookID, nil, true))
	lessons, err := app.Sched.Lessons(studentID, bookID)
	require.NoError(t, err)
	clock.Advance(25 * time.Hour)
	room, _, err := app.Rooms.Enter(lessons[0].ID, studentID)
	require.NoError(t, err)
	client := &fakeClient{}
	room.Join(client)
	client.frames = nil
	room.HandleEvent(classroom.Event{Type: classroom.EvWordClicked})
	for i := 0; i < 8 && room.Machine().Phase() != classroom.PhaseRepeating; i++ {
		room.HandleEvent(classroom.Event{Type: classroom.EvTTSFinished})
	}
	require.Equal(t, classroom.PhaseRepeating, room.Machine().Phase())
	client.frames = nil // Ignore the setup playback actions; assert only the confirmation boundary.
	room.beginDeferredEffects()
	target := room.machine.CurrentWord().Word

	room.mu.Lock()
	envelope := ClientEnvelope{Context: room.contextLocked(), EventID: "duplex-confirm-ack"}
	room.mu.Unlock()
	command, _, frames := room.HandleClientVoiceSubmissionContext(context.Background(), envelope, target, client)
	require.Equal(t, "repeat_read", command)
	require.Contains(t, frames[len(frames)-1], "type")
	require.Equal(t, "event_processed", frames[len(frames)-1]["type"])
	require.Empty(t, client.frames, "the durable success acknowledgement cannot reach the client before receipt commit")

	flushed := room.flushDeferredEffects(context.Background())
	require.NotEmpty(t, flushed, "committed effects must run through the teacher capability boundary")
	var quizItems int
	for _, frame := range flushed {
		if frame["type"] != "ui_action" {
			continue
		}
		if action, ok := frame["action"].(classroom.Action); ok && action.Type == classroom.ActShowQuizItem {
			quizItems++
		}
	}
	require.Equal(t, 1, quizItems, "the repeat confirmation should publish the next quiz item")
	for _, frame := range frames {
		_ = client.Send(frame) // ws.go sends these only after CommitSnapshot returns successfully
	}
	require.Contains(t, client.frames, Msg{"type": "event_processed", "event_id": "duplex-confirm-ack", "command": "repeat_read", "submission_id": ""})
}

func TestRoomMutationLeaseSerializesConcurrentIngress(t *testing.T) {
	_, _, _, room := setupRoom(t)
	room.mu.Lock()
	turn := room.contextLocked()
	room.mu.Unlock()
	room.mutationMu.Lock() // Model the duplex confirmation holding the lease through SQLite commit.
	started := make(chan struct{})
	done := make(chan []Msg, 1)
	go func() {
		close(started)
		done <- room.HandleClientEventContext(context.Background(), ClientEnvelope{Context: turn, EventID: "serialized-event"}, classroom.Event{Type: classroom.EvTTSFinished})
	}()
	<-started
	select {
	case <-done:
		require.FailNow(t, "concurrent classroom ingress must wait while a durable duplex transaction owns the room")
	case <-time.After(20 * time.Millisecond):
	}
	room.mutationMu.Unlock()
	select {
	case frames := <-done:
		require.NotEmpty(t, frames)
	case <-time.After(time.Second):
		require.FailNow(t, "serialized ingress did not resume after the durable transaction released the room")
	}
}

func TestEnableDuplexShadowStoreIsExplicitAndIdempotent(t *testing.T) {
	app, _ := newTestApp(t)
	require.NoError(t, app.EnableDuplexShadowStore(), "in-memory deployments need no shadow tables")
	require.Nil(t, app.DuplexStore)

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "duplex-shadow.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	app.DB = db
	require.NoError(t, app.EnableDuplexShadowStore())
	require.NotNil(t, app.DuplexStore)
	require.NoError(t, app.EnableDuplexShadowStore(), "restarting with shadow mode enabled must safely repeat migration")
	for _, model := range []any{&store.DuplexCheckpoint{}, &store.DuplexReceipt{}, &store.DuplexOutbox{}} {
		require.True(t, db.Migrator().HasTable(model))
	}
}

func TestAutoSeedImportsDefaultDirectoryIdempotently(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	seedDir := filepath.Join(root, "data", "seed")
	require.NoError(t, os.MkdirAll(seedDir, 0o755))
	seed := `{"id":"fixture","name":"Startup fixture","grade":"g7","source":"test","entries":[{"word":"apple","meanings":["苹果"]}]}`
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "wordbook-fixture.json"), []byte(seed), 0o600))
	app, _ := newTestApp(t)

	books, words, err := app.AutoSeed()
	require.NoError(t, err)
	require.Equal(t, 1, books)
	require.Equal(t, 1, words)
	books, words, err = app.AutoSeed()
	require.NoError(t, err)
	require.Zero(t, books, "repeated startup must not import a duplicate book")
	require.Zero(t, words)
	published, err := app.Books.ListPublished()
	require.NoError(t, err)
	require.Len(t, published, 1)
}

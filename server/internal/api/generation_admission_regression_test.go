package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

type closeFailClient struct{ err error }

func (c closeFailClient) Send(Msg) error { return nil }
func (c closeFailClient) Close() error   { return c.err }

func TestInvalidWebSocketTicketsDoNotAllocateGenerations(t *testing.T) {
	app := newBareApp()
	router := app.Router()
	for id := int64(700_000); id < 700_100; id++ {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("GET", "/ws/classroom?lessonId="+strconv.FormatInt(id, 10)+"&ticket=invalid", nil)
		router.ServeHTTP(recorder, request)
		require.Equal(t, 401, recorder.Code)
	}
	app.generations.mu.Lock()
	defer app.generations.mu.Unlock()
	require.Empty(t, app.generations.lessons, "an unauthenticated ID spray must not grow persistent coordinator state")
}

func TestDropPropagatesClientCloseFailureAndFailsGeneration(t *testing.T) {
	app := newBareApp()
	const lessonID int64 = 700_101
	lease, err := app.generations.acquire(context.Background(), lessonID)
	require.NoError(t, err)
	closeErr := errors.New("fixture socket close failure")
	room := &Room{app: app, lessonID: lessonID, generation: lease.Token(), clients: map[Client]bool{closeFailClient{err: closeErr}: true}}
	app.Rooms.mu.Lock()
	app.Rooms.rooms[lessonID] = room
	app.Rooms.mu.Unlock()
	lease.Release()

	err = app.Rooms.Drop(lessonID)
	require.ErrorIs(t, err, closeErr)
	app.generations.mu.Lock()
	require.True(t, app.generations.failed[lessonID], "a cleanup failure must fail closed")
	_, retained := app.generations.lessons[lessonID]
	app.generations.mu.Unlock()
	require.True(t, retained)
}

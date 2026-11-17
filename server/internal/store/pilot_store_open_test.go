package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenPilotStoreExplicitlyMigratesAndBindsPool(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "pilot-open.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	var events []PilotStoreEvent
	pilot, err := OpenPilotStore(context.Background(), db, func(_ context.Context, event PilotStoreEvent) {
		events = append(events, event)
	})
	require.NoError(t, err)
	require.NotNil(t, pilot)
	require.NotNil(t, pilot.verifier)
	require.Empty(t, events)

	state, err := inspectPilotSchema(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, PilotSchemaReady, state)
}

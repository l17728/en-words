package api

import (
	"encoding/json"
	"testing"

	"enwords/internal/classroom"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCapabilityReportUsesDurableAndMemorySources(t *testing.T) {
	want := &classroom.Result{}
	app := &App{reports: map[int64]*classroom.Result{7: want}}

	got, err := app.capabilityReport(7)
	require.NoError(t, err)
	require.Same(t, want, got)
	_, err = app.capabilityReport(8)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unavailable")

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&store.RuntimeRecord{}))
	encoded, err := json.Marshal(want)
	require.NoError(t, err)
	require.NoError(t, db.Create(&store.RuntimeRecord{Key: "report/7", Value: string(encoded)}).Error)
	app.DB = db

	got, err = app.capabilityReport(7)
	require.NoError(t, err)
	require.Equal(t, want, got)
	_, err = app.capabilityReport(8)
	require.Error(t, err)
	var malformed store.RuntimeRecord
	require.NoError(t, db.Create(&store.RuntimeRecord{Key: "report/9", Value: "{"}).Error)
	_, err = app.capabilityReport(9)
	require.Error(t, err)
	require.NoError(t, db.First(&malformed, "key = ?", "report/9").Error)
}

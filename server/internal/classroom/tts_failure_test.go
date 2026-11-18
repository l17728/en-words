package classroom

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnheardTTSSegmentPausesAndOnlyExplicitSkipAdvances(t *testing.T) {
	m, _ := newMachine(t, batchN(1))
	require.NoError(t, m.OnEvent(Event{Type: EvWordClicked}))
	before := m.Snapshot()
	require.Equal(t, PhaseTTSSeg, before.Phase)
	require.NoError(t, m.OnTimeout(PhaseTTSSeg))
	require.True(t, m.Paused())
	require.Equal(t, before.TtsSegIdx, m.Snapshot().TtsSegIdx)
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	require.Equal(t, before.TtsSegIdx, m.Snapshot().TtsSegIdx)
	m.Resume()
	require.False(t, m.Paused())
	require.Equal(t, before.TtsSegIdx, m.Snapshot().TtsSegIdx)
	require.NoError(t, m.PauseForPlaybackFailure())
	require.NoError(t, m.SkipUnheardSegment())
	require.False(t, m.Paused())
	require.NotEqual(t, before.TtsSegIdx, m.Snapshot().TtsSegIdx)
}

package config

import (
	"errors"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
)

func TestPlaybackDefaultsOverridesAndZeroGaps(t *testing.T) {
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	require.Equal(t, PlaybackSettings{3, 1, 1500, 1500, 1500, 250}, s.Playback(7))
	global := PlaybackSettings{2, 2, 0, 10000, 100, 500}
	require.NoError(t, s.SetPlayback("admin", 0, true, global, "default"))
	require.Equal(t, global, s.Playback(7))
	personal := PlaybackSettings{5, 5, 200, 0, 0, 1000}
	require.NoError(t, s.SetPlayback("student", 7, false, personal, "personal"))
	require.Equal(t, personal, s.Playback(7))
	require.Equal(t, global, s.Playback(8))
	st := s.Snapshot(7)
	require.Equal(t, 1, st.PlaybackVersion)
	require.Equal(t, 5, st.TtsZhRepeat)
	require.Zero(t, st.TtsSwitchIntervalMs)
}

func TestPlaybackValidationAndWriteFailureDoNotPublishPartialCache(t *testing.T) {
	p := newMemPersister()
	f := &playbackFailKey{Persister: p}
	s, err := NewStore(f, nil)
	require.NoError(t, err)
	before := s.Playback(7)
	for _, v := range []PlaybackSettings{{0, 1, 0, 0, 0, 250}, {1, 6, 0, 0, 0, 250}, {1, 1, -1, 0, 0, 250}, {1, 1, 0, 10001, 0, 250}, {1, 1, 0, 0, 10001, 250}, {1, 1, 0, 0, 0, 99}, {1, 1, 0, 0, 0, 2001}} {
		require.Error(t, s.SetPlayback("student", 7, false, v, "fixture"))
		require.Equal(t, before, s.Playback(7))
		require.Empty(t, p.overrides)
	}
	f.failOn = "ttsZhIntervalMs"
	require.Error(t, s.SetPlayback("student", 7, false, PlaybackSettings{2, 2, 100, 200, 300, 250}, "fixture"))
	require.Equal(t, before, s.Playback(7), "partial persistence writes must not publish cache; caller rolls back outer transaction")
	require.Error(t, s.SetPlayback("student", 0, false, before, "fixture"))
	require.Error(t, s.SetPlayback("admin", 7, true, before, "fixture"))
	require.Error(t, s.SetPlayback("student", 7, false, before, " "))
}

func TestLegacyPlaybackUpdateDoesNotCreateOrChangeLeadOverride(t *testing.T) {
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	global := PlaybackSettings{3, 1, 1500, 1500, 1500, 700}
	require.NoError(t, s.SetPlayback("admin", 0, true, global, "global defaults"))
	legacy := PlaybackSettings{2, 2, 0, 300, 500, 0}
	require.NoError(t, s.SetPlaybackLegacy("student", 7, false, legacy, "old client"))
	require.Equal(t, 700, s.Playback(7).ShowWordLeadMs)
	global.ShowWordLeadMs = 900
	require.NoError(t, s.SetPlayback("admin", 0, true, global, "updated global default"))
	require.Equal(t, 900, s.Playback(7).ShowWordLeadMs, "old clients must preserve inheritance")
}

type playbackFailKey struct {
	Persister
	failOn string
}

func (p *playbackFailKey) SaveOverride(sid int64, key string, value any) error {
	if key == p.failOn {
		return errors.New("fixture playback write failure")
	}
	return p.Persister.SaveOverride(sid, key, value)
}

func TestPlaybackReadersNeverSeeMixedFiveFieldSnapshot(t *testing.T) {
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	a := PlaybackSettings{1, 1, 0, 0, 0, 100}
	b := PlaybackSettings{5, 5, 10000, 10000, 10000, 2000}
	require.NoError(t, s.SetPlayback("student", 7, false, a, "fixture"))
	var wg sync.WaitGroup
	bad := make(chan PlaybackSettings, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			v := s.Playback(7)
			if v != a && v != b {
				select {
				case bad <- v:
				default:
				}
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		require.NoError(t, s.SetPlayback("student", 7, false, b, "fixture"))
		require.NoError(t, s.SetPlayback("student", 7, false, a, "fixture"))
	}
	wg.Wait()
	select {
	case v := <-bad:
		t.Fatalf("mixed playback snapshot: %+v", v)
	default:
	}
}

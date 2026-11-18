package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"enwords/internal/localmodel"
	"enwords/internal/scheduler"
	"github.com/stretchr/testify/require"
)

func TestSpeechCacheDeduplicatesConcurrentGeneration(t *testing.T) {
	wav, err := os.ReadFile(filepath.Join("..", "..", "..", "review", "audio-fixtures", "en", "01.wav"))
	require.NoError(t, err)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wav)
	}))
	defer upstream.Close()
	client, err := localmodel.New(upstream.URL, nil)
	require.NoError(t, err)
	client.Services["tts"] = localmodel.Service{ID: "tts", Model: "fixture", Capabilities: []string{"tts"}}
	a := NewApp(scheduler.RealClock{}, nil)
	a.Local = client
	defer os.RemoveAll(filepath.Join("data", "cache", "tts"))
	type result struct {
		raw []byte
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			raw, _, e := a.synthesizeCached(context.Background(), "cache fixture", "zh")
			results <- result{raw: raw, err: e}
		}()
	}
	r1, r2 := <-results, <-results
	require.NoError(t, r1.err)
	require.NoError(t, r2.err)
	require.Equal(t, wav, r1.raw)
	require.Equal(t, wav, r2.raw)
	require.EqualValues(t, 1, calls.Load())
}

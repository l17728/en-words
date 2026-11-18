package localmodel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadyUsesLiveStatusAndCoalescesConcurrentReads(t *testing.T) {
	var calls atomic.Int32
	var mode atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		calls.Add(1)
		switch mode.Load() {
		case 1:
			_, _ = w.Write([]byte(`{"services":[{"id":"vl","status":"stopped"},{"id":"asr","status":"stopped"},{"id":"tts","status":"ready"}]}`))
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte(`{"services":[{"id":"vl","status":"stopped"},{"id":"asr","status":"ready"},{"id":"tts","status":"ready"}]}`))
		}
	}))
	defer s.Close()
	c, err := New(s.URL, nil)
	require.NoError(t, err)
	c.Services["asr"] = Service{Model: "fixture-asr", Capabilities: []string{"asr"}}
	c.Services["tts"] = Service{Model: "fixture-tts", Capabilities: []string{"tts"}}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready := c.Ready(context.Background())
			if !ready["asr"] || !ready["tts"] || ready["vl"] {
				t.Errorf("unexpected ready snapshot: %v", ready)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
	mode.Store(1)
	c.statusAt = time.Time{}
	ready := c.Ready(context.Background())
	require.False(t, ready["asr"])
	require.True(t, ready["tts"])
	mode.Store(2)
	c.statusAt = time.Time{}
	ready = c.Ready(context.Background())
	require.False(t, ready["asr"])
	require.False(t, ready["tts"], "status failure must fail closed")
	require.EqualValues(t, 3, calls.Load())
}

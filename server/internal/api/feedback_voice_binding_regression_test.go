package api

import (
	"github.com/stretchr/testify/require"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentVoiceFeedbackBodiesStayBoundToTheirOwnEnvelope(t *testing.T) {
	_, _, _, room := setupRoom(t)
	room.mu.Lock()
	ctx := room.contextLocked()
	room.mu.Unlock()
	bodies := []string{strings.Repeat("完整中文A 保持空白！", 30) + "cat 我老忘啊", strings.Repeat("完整中文B 保持标点！", 30) + "C++ dog 我老忘呢"}
	type result struct {
		index     int
		cmd, body string
		frames    []Msg
	}
	out := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index, body := range bodies {
		wg.Add(1)
		go func(index int, body string) {
			defer wg.Done()
			<-start
			cmd, got, frames := room.HandleClientVoiceSubmission(ClientEnvelope{Context: ctx, EventID: "voice-event-" + itoa(index), SubmissionID: "voice-submission-" + itoa(index)}, "我 想 反 馈 "+body)
			out <- result{index, cmd, got, frames}
		}(index, body)
	}
	close(start)
	wg.Wait()
	close(out)
	for result := range out {
		require.Equal(t, "feedback", result.cmd)
		require.Equal(t, bodies[result.index], result.body)
		require.NotEmpty(t, result.frames)
		done := result.frames[len(result.frames)-1]
		require.Equal(t, "voice-submission-"+itoa(result.index), done["submission_id"])
		require.Equal(t, "voice-event-"+itoa(result.index), done["event_id"])
	}
	require.Empty(t, room.FeedbackCh, "external callers must not depend on a shared feedback queue")
	_, _, frames := room.HandleClientVoiceSubmission(ClientEnvelope{Context: ctx, EventID: "voice-event-0", SubmissionID: "voice-submission-0"}, "反馈 another body")
	require.Equal(t, "event_rejected", frames[0]["type"])
	require.Equal(t, "voice-event-0", frames[0]["event_id"])
	require.Equal(t, "voice-submission-0", frames[0]["submission_id"])
}

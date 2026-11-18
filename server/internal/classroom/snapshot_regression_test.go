package classroom

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestQuizIdentityDurableJSONRoundTrip(t *testing.T) {
	id := QuizItemID{WordID: 42, Kind: "final", Round: 1, Seq: 3}
	original := State{QuizQueue: []QuizItemID{id}, Judged: map[QuizItemID]Judgment{id: JudgmentWrong}}
	raw, err := json.Marshal(original)
	require.NoError(t, err)
	var restored State
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Equal(t, original.QuizQueue, restored.QuizQueue)
	require.Equal(t, original.Judged, restored.Judged)
	object, err := json.Marshal(id)
	require.NoError(t, err)
	require.JSONEq(t, `{"word_id":42,"kind":"final","round":1,"seq":3}`, string(object), "wire identity remains an object")
	for _, key := range []string{`[]`, `[1,"final"]`, `["invalid","final",0,0]`, `null`} {
		var broken QuizItemID
		require.Error(t, broken.UnmarshalText([]byte(key)))
	}
}

func TestFinishedResultIncludesFailedWordsWithoutInflatingCorrectCount(t *testing.T) {
	m, _ := newMachine(t, batchN(3))
	m.state.Finished = true
	m.state.Judged = map[QuizItemID]Judgment{
		{WordID: 1, Kind: "final"}:           JudgmentCorrect,
		{WordID: 2, Kind: "final"}:           JudgmentWrong,
		{WordID: 2, Kind: "final", Round: 1}: JudgmentWrong,
	}
	result := m.Result()
	require.NotNil(t, result)
	require.Equal(t, map[int64]bool{1: true, 2: false, 3: false}, result.Correct, "scheduler needs explicit false entries for every taught word")
	require.Equal(t, 1, result.Report.CorrectCount, "map length is not the number correct")
}

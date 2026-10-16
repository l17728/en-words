package duplex

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func identity() Identity { return Identity{"session", "epoch", 1, "utterance"} }
func TestStrictControlAndNormalization(t *testing.T) {
	f := Frame{Protocol: "duplex.v1", Type: "transcript.preview", Identity: identity(), EventID: "event", Revision: 1, Text: "猫"}
	raw, _ := json.Marshal(f)
	got, e := DecodeControl(raw)
	require.NoError(t, e)
	require.Equal(t, f, got)
	for _, bad := range [][]byte{nil, []byte("null"), []byte("[]"), []byte("{"), []byte(`{"a":1,"a":2}`), []byte(`{"x":`), []byte(`{"x":1} {}`), append(raw, byte(255)), []byte(strings.Repeat("x", 16385)), []byte(strings.Replace(string(raw), `"event_id"`, `"unknown"`, 1)), []byte(strings.Replace(string(raw), `"revision":1`, `"revision":0`, 1)), []byte(strings.Replace(string(raw), `"duplex.v1"`, `"v9"`, 1))} {
		_, e = DecodeControl(bad)
		require.Error(t, e)
	}
	for _, kind := range []string{"audio.start", "audio.end", "state.sync", "heartbeat", "response.interrupt"} {
		f.Type = kind
		f.Text = ""
		f.Revision = 0
		raw, _ = json.Marshal(f)
		_, e = DecodeControl(raw)
		require.NoError(t, e)
		f.Text = "unexpected"
		raw, _ = json.Marshal(f)
		_, e = DecodeControl(raw)
		require.Error(t, e)
	}
	for _, bad := range []Frame{{Protocol: "duplex.v1", Type: "bad", Identity: identity(), EventID: "e"}, {Protocol: "duplex.v1", Type: "transcript.final", Identity: identity(), EventID: "e", Revision: 1, Text: " "}, {Protocol: "duplex.v1", Type: "heartbeat", Identity: identity(), EventID: " "}} {
		raw, _ = json.Marshal(bad)
		_, e = DecodeControl(raw)
		require.Error(t, e)
	}
	require.True(t, Agree("  CAFÉ. ", "cafe\u0301"))
	require.True(t, Agree("我  会了。", "我 会了"))
	for _, pair := range [][2]string{{"不是猫", "猫"}, {"１２", "12"}, {"ap ple", "apple"}, {"a,b", "ab"}, {"!", "!"}, {"", ""}} {
		require.False(t, Agree(pair[0], pair[1]))
	}
	_, e = New(Identity{})
	require.Error(t, e)
}

func TestRevokedLeaseAndInvalidFinalFailClosed(t *testing.T) {
	t.Run("commit_requires_deadline_gate", func(t *testing.T) { s, _ := verified(t); require.Error(t, s.MarkCommitted(identity())) })
	t.Run("mixed_periods", func(t *testing.T) {
		require.Equal(t, "词.。", Normalize("词.。"))
		require.Equal(t, "词。.", Normalize("词。."))
	})
	t.Run("case_alias_and_id_controls", func(t *testing.T) {
		f := Frame{Protocol: "duplex.v1", Type: "heartbeat", Identity: identity(), EventID: "e"}
		raw, _ := json.Marshal(f)
		bad := strings.TrimSuffix(string(raw), "}") + `,"Protocol":"duplex.v1"}`
		_, e := DecodeControl([]byte(bad))
		require.ErrorIs(t, e, ErrProtocol)
		for _, id := range []string{"a\tb", "a b", "a\u0085b"} {
			f.EventID = id
			raw, _ = json.Marshal(f)
			_, e = DecodeControl(raw)
			require.ErrorIs(t, e, ErrProtocol)
		}
	})
	t.Run("question_not_command", func(t *testing.T) {
		for _, a := range []string{"跳过？", "跳过!", "跳过…", "跳过..."} {
			require.False(t, Agree(a, "跳过"))
		}
	})
	t.Run("revoked_output", func(t *testing.T) {
		s, n := verified(t)
		require.NoError(t, s.BeginCommit(identity(), n))
		require.NoError(t, s.MarkCommitted(identity()))
		require.NoError(t, s.GrantOutput("r", "l"))
		s.Cancel()
		require.Error(t, s.GrantOutput("r", "l"))
		require.False(t, s.OutputValid("r", "l"))
		require.Equal(t, "committed", s.State())
	})
	for _, text := range []string{string([]byte{255}), strings.Repeat("a", 2049)} {
		t.Run("invalid_final", func(t *testing.T) {
			s, n := capturing(t)
			require.NoError(t, s.Chunk(identity(), 0, []byte{0, 0}, n))
			require.NoError(t, s.Preview(identity(), 1, "cat"))
			require.NoError(t, s.End(n))
			require.ErrorIs(t, s.Confirm(identity(), 1, text, n), ErrProtocol)
			require.Error(t, s.MarkCommitted(identity()))
		})
	}
	t.Run("null_fields", func(t *testing.T) {
		f := Frame{Protocol: "duplex.v1", Type: "heartbeat", Identity: identity(), EventID: "e"}
		raw, _ := json.Marshal(f)
		for _, field := range []string{"seq", "revision", "text"} {
			bad := strings.TrimSuffix(string(raw), "}") + `,"` + field + `":null}`
			_, e := DecodeControl([]byte(bad))
			require.ErrorIs(t, e, ErrProtocol)
		}
	})
}
func capturing(t *testing.T) (*Session, time.Time) {
	t.Helper()
	s, e := New(identity())
	require.NoError(t, e)
	now := time.Now()
	require.NoError(t, s.Start(now))
	return s, now
}
func verified(t *testing.T) (*Session, time.Time) {
	s, n := capturing(t)
	require.NoError(t, s.Chunk(identity(), 0, make([]byte, 640), n))
	require.NoError(t, s.Preview(identity(), 1, "Cat."))
	require.NoError(t, s.End(n))
	require.NoError(t, s.Confirm(identity(), 1, "cat", n))
	return s, n
}
func TestConfirmationSingleCommitAndOutputCancellation(t *testing.T) {
	s, n := verified(t)
	require.Equal(t, "verified", s.State())
	require.Error(t, s.Confirm(identity(), 1, "cat", n))
	bad := identity()
	bad.Epoch = "old"
	require.ErrorIs(t, s.MarkCommitted(bad), ErrStale)
	require.NoError(t, s.BeginCommit(identity(), n))
	require.NoError(t, s.MarkCommitted(identity()))
	require.NoError(t, s.MarkCommitted(identity()))
	require.NoError(t, s.GrantOutput("r", "l"))
	require.True(t, s.OutputValid("r", "l"))
	require.Error(t, s.GrantOutput("r2", "l2"))
	s.Cancel()
	require.Equal(t, "committed", s.State())
	require.False(t, s.OutputValid("r", "l"))
	require.Error(t, s.Start(n))
}
func TestInputFailuresNeverBecomeTeachingCommit(t *testing.T) {
	for _, mode := range []string{"gap", "odd", "empty", "oversize", "capture_timeout", "end_timeout", "confirm_timeout", "conflict", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, n := capturing(t)
			var err error
			switch mode {
			case "gap":
				err = s.Chunk(identity(), 1, []byte{0, 0}, n)
			case "odd":
				err = s.Chunk(identity(), 0, []byte{0}, n)
			case "empty":
				err = s.Chunk(identity(), 0, nil, n)
			case "oversize":
				err = s.Chunk(identity(), 0, make([]byte, 2562), n)
			case "capture_timeout":
				err = s.Chunk(identity(), 0, []byte{0, 0}, n.Add(15*time.Second))
			default:
				require.NoError(t, s.Chunk(identity(), 0, []byte{0, 0}, n))
				require.NoError(t, s.Preview(identity(), 1, "not cat"))
				if mode == "end_timeout" {
					err = s.End(n.Add(15 * time.Second))
					break
				}
				require.NoError(t, s.End(n))
				switch mode {
				case "confirm_timeout":
					err = s.Confirm(identity(), 1, "not cat", n.Add(8*time.Second))
				case "conflict":
					err = s.Confirm(identity(), 1, "cat", n)
				case "cancel":
					s.Cancel()
					err = s.Confirm(identity(), 1, "not cat", n)
				}
			}
			require.Error(t, err)
			require.Error(t, s.MarkCommitted(identity()))
			require.Error(t, s.GrantOutput("r", "l"))
		})
	}
}
func TestStalePreviewAndChunkCannotChangeCurrentInput(t *testing.T) {
	s, n := capturing(t)
	bad := identity()
	bad.InputRevision++
	require.ErrorIs(t, s.Chunk(bad, 0, []byte{0, 0}, n), ErrStale)
	require.Error(t, s.End(n))
	require.Error(t, s.Preview(bad, 1, "cat"))
	require.Error(t, s.Preview(identity(), 1, ""))
	require.Error(t, s.Preview(identity(), 1, strings.Repeat("x", 2049)))
	require.NoError(t, s.Preview(identity(), 2, "cat"))
	require.Error(t, s.Preview(identity(), 1, "wrong"))
	require.NoError(t, s.Chunk(identity(), 0, []byte{0, 0}, n))
	require.NoError(t, s.End(n))
	require.Error(t, s.Preview(identity(), 3, "cat"))
	require.Error(t, s.Chunk(identity(), 1, []byte{0, 0}, n))
	require.Error(t, s.Confirm(bad, 2, "cat", n))
	require.NoError(t, s.Confirm(identity(), 2, "cat", n))
	require.NoError(t, s.BeginCommit(identity(), n))
	require.NoError(t, s.MarkCommitted(identity()))
	require.Error(t, s.GrantOutput("", "x"))
}

func TestCommitDeadlineAndUnknownOutcomeStaySafe(t *testing.T) {
	t.Run("late_judge", func(t *testing.T) {
		s, n := verified(t)
		require.ErrorIs(t, s.BeginCommit(identity(), n.Add(8*time.Second)), ErrTimeout)
		require.Error(t, s.MarkCommitted(identity()))
	})
	t.Run("wrong_identity", func(t *testing.T) {
		s, n := verified(t)
		id := identity()
		id.Epoch = "old"
		require.ErrorIs(t, s.BeginCommit(id, n), ErrStale)
		require.Error(t, s.CommitFailed(identity()))
		require.NoError(t, s.BeginCommit(identity(), n))
		require.Error(t, s.BeginCommit(identity(), n))
		require.ErrorIs(t, s.CommitFailed(id), ErrStale)
	})
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed_rollback", true: "committed_after_cancel"}[commit], func(t *testing.T) {
			s, n := verified(t)
			require.NoError(t, s.BeginCommit(identity(), n))
			s.Cancel()
			require.Equal(t, "committing", s.State(), "unknown DB outcome cannot become canceled/allow retry")
			require.Error(t, s.Start(n))
			if commit {
				require.NoError(t, s.MarkCommitted(identity()))
				require.Equal(t, "committed", s.State())
				require.Error(t, s.CommitFailed(identity()))
				require.Error(t, s.GrantOutput("r", "l"))
			} else {
				require.NoError(t, s.CommitFailed(identity()))
				require.Equal(t, "canceled", s.State())
				require.Error(t, s.MarkCommitted(identity()))
			}
		})
	}
	t.Run("rollback_without_cancel", func(t *testing.T) {
		s, n := verified(t)
		require.NoError(t, s.BeginCommit(identity(), n))
		require.NoError(t, s.CommitFailed(identity()))
		require.Equal(t, "rejected", s.State())
	})
}

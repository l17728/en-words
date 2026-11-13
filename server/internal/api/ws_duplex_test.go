package api

import (
	"bytes"
	"errors"
	"testing"

	"enwords/internal/duplex"
	"github.com/stretchr/testify/require"
)

func TestDuplexWireHelpers(t *testing.T) {
	pcm := []byte{1, 0, 0xff, 0x7f}
	wav := pcm16WAV(pcm, 16000)
	require.Equal(t, []byte("RIFF"), wav[:4])
	require.Equal(t, []byte("WAVEfmt "), wav[8:16])
	require.Equal(t, []byte("data"), wav[36:40])
	require.True(t, bytes.Equal(pcm, wav[44:]))
	require.Equal(t, "text_hash_mismatch", duplexReason(duplex.ErrTextHash))
	require.Equal(t, "stale_stream", duplexReason(duplex.ErrStale))
	require.Equal(t, "invalid_schema", duplexReason(errors.New("other")))
}

package api

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"enwords/internal/duplex"
	"enwords/internal/store"
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

func TestDuplexReceiptRecoveryWindow(t *testing.T) {
	now := time.Now().UTC()
	receipt := store.DuplexReceipt{Status: "committing", UpdatedAt: now.Add(-duplexReceiptRecoveryWindow)}
	require.True(t, duplexReceiptNeedsRecovery(receipt, now))
	receipt.UpdatedAt = now.Add(-duplexReceiptRecoveryWindow + time.Millisecond)
	require.False(t, duplexReceiptNeedsRecovery(receipt, now))
	receipt.Status = "committed"
	require.False(t, duplexReceiptNeedsRecovery(receipt, now))
}

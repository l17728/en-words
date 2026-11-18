package localmodel

import (
	"bytes"
	"context"
	"encoding/binary"
	"enwords/internal/obs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMalformedWAVLogsProviderFailureNotCancellation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("RIFFbad")) }))
	defer upstream.Close()
	var logs bytes.Buffer
	client, err := New(upstream.URL, obs.NewWithSink(&logs))
	require.NoError(t, err)
	client.Services["tts"] = Service{ID: "tts", Model: "fixture", Capabilities: []string{"tts"}}
	_, err = client.Synthesize(obs.WithTrace(context.Background(), "lesson-99"), "苹果", "zh")
	require.Error(t, err)
	require.Contains(t, logs.String(), `"reason":"invalid_media_or_provider_failure"`)
	require.NotContains(t, logs.String(), `"reason":"canceled_or_timeout"`)
	require.Contains(t, logs.String(), `"trace_id":"lesson-99"`)
	require.NotContains(t, logs.String(), "苹果")
}

func pcmWAVFixture() []byte {
	wav := make([]byte, 52)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], 44)
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 8)
	copy(wav[44:], []byte{0, 0, 100, 0, 0, 0, 156, 255})
	return wav
}
func TestConsoleRejectsMalformedWAVBeyondRIFFHeader(t *testing.T) {
	header := make([]byte, 44)
	copy(header, "RIFF")
	copy(header[8:], "WAVE")
	badBlock := pcmWAVFixture()
	binary.LittleEndian.PutUint32(badBlock[40:], 1000)
	truncated := pcmWAVFixture()[:50]
	badRate := pcmWAVFixture()
	binary.LittleEndian.PutUint32(badRate[28:], 0)
	badFormat := pcmWAVFixture()
	binary.LittleEndian.PutUint16(badFormat[20:], 6)
	noFormat := pcmWAVFixture()
	copy(noFormat[12:], "JUNK")
	noData := pcmWAVFixture()
	copy(noData[36:], "JUNK")
	for _, tc := range []struct {
		name string
		raw  []byte
	}{{"header_only", header}, {"oversized_data_chunk", badBlock}, {"truncated_pcm", truncated}, {"invalid_byte_rate", badRate}, {"unsupported_codec", badFormat}, {"missing_fmt", noFormat}, {"missing_data", noData}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tc.raw) }))
			defer s.Close()
			c, err := New(s.URL, nil)
			require.NoError(t, err)
			c.Services["tts"] = Service{Model: "fixture", Capabilities: []string{"tts"}}
			audio, err := c.Synthesize(context.Background(), "测试", "zh")
			require.Error(t, err, "valid header must not authenticate malformed audio")
			require.Empty(t, audio)
		})
	}
}

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"enwords/internal/localmodel"
	"enwords/internal/obs"
	"fmt"
	"github.com/stretchr/testify/require"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type asrQualityCapture struct {
	raw    string
	status int
}

func (c *asrQualityCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || req.URL.Path != "/api/asr/transcriptions" {
		return resp, err
	}
	c.status = resp.StatusCode
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if readErr == nil {
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &body) == nil {
			c.raw = body.Text
		}
	}
	return resp, err
}

func TestOptInIndependentASRThroughOwnedProductRoute(t *testing.T) {
	if os.Getenv("ENWORDS_PRODUCT_ASR_EVAL") != "1" {
		t.Skip("explicit local product ASR quality opt-in required")
	}
	path := "../../../review/audio-fixtures/manifest.json"
	expectedCount := 16
	if os.Getenv("ENWORDS_PRODUCT_ASR_LANGUAGE") == "zh" {
		path = "../../../review/audio-fixtures/manifest-zh.json"
		expectedCount = 12
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	expected, err := os.ReadFile(path + ".sha256")
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	require.Equal(t, strings.TrimSpace(string(expected)), digest)
	var labels struct {
		Status  string `json:"status"`
		Samples []struct {
			ID       string `json:"case_id"`
			Text     string `json:"text"`
			Language string `json:"language"`
			File     string `json:"file"`
			SHA      string `json:"sha256"`
		} `json:"samples"`
	}
	require.NoError(t, json.Unmarshal(raw, &labels))
	require.Equal(t, "frozen_reviewed", labels.Status)
	require.Len(t, labels.Samples, expectedCount)
	output := os.Getenv("ENWORDS_LOCAL_SPEECH_OUTPUT")
	require.NotEmpty(t, output)
	reportFile, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	report := map[string]any{"scope": "independent synthetic audio through authenticated own-lesson product HTTP route and production adapter; one inference per sample", "expected_cases": expectedCount, "manifest_sha256": digest, "human_listening": "pending", "started_at": time.Now().Format(time.RFC3339)}
	defer func() {
		report["finished_at"] = time.Now().Format(time.RFC3339)
		require.NoError(t, json.NewEncoder(reportFile).Encode(report))
		require.NoError(t, reportFile.Close())
	}()
	journal, err := os.OpenFile(output+".jsonl", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	defer func() { require.NoError(t, journal.Close()) }()
	a, _, lid, _ := setupRoom(t)
	token, _ := loginGet(t, a, "sfin", "pw")
	client, err := localmodel.New("http://127.0.0.1:8090", obs.Nop())
	require.NoError(t, err)
	err = client.Discover(context.Background())
	if err != nil {
		report["fatal_error"] = err.Error()
		t.Fatal(err)
	}
	a.Local = client
	capture := &asrQualityCapture{}
	client.HTTP.Transport = capture
	report["models"] = client.Services
	rows := []map[string]any{}
	failed, consecutive := 0, 0
	router := a.Router()
	for _, sample := range labels.Samples {
		capture.raw = ""
		capture.status = 0
		require.False(t, filepath.IsAbs(sample.File) || strings.Contains(sample.File, ".."))
		audio, err := os.ReadFile(filepath.Join(filepath.Dir(path), sample.File))
		require.NoError(t, err)
		sum := sha256.Sum256(audio)
		require.Equal(t, sample.SHA, hex.EncodeToString(sum[:]))
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", "independent.wav")
		require.NoError(t, err)
		_, err = part.Write(audio)
		require.NoError(t, err)
		require.NoError(t, form.WriteField("language", sample.Language))
		require.NoError(t, form.Close())
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/classroom/lessons/%d/transcribe", lid), &body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("X-Trace-ID", "eval-"+sample.ID)
		response := httptest.NewRecorder()
		start := time.Now()
		router.ServeHTTP(response, req)
		row := map[string]any{"case_id": sample.ID, "reference": sample.Text, "source_sha256": sample.SHA, "http_status": response.Code, "duration_ms": time.Since(start).Milliseconds(), "language": sample.Language, "human_listening": "pending"}
		var envelope struct {
			Code int `json:"code"`
			Data struct {
				Text string `json:"text"`
			} `json:"data"`
		}
		decodeErr := json.Unmarshal(response.Body.Bytes(), &envelope)
		passed := response.Code == 200 && decodeErr == nil && envelope.Code == 0 && envelope.Data.Text != ""
		row["transport_pass"] = passed
		row["transcript"] = envelope.Data.Text
		row["raw_transcript"] = capture.raw
		row["raw_available"] = capture.raw != ""
		row["provider_status"] = capture.status
		if !passed {
			failed++
			consecutive++
			row["error"] = "product_transcription_failed"
		} else {
			consecutive = 0
		}
		rows = append(rows, row)
		report["cases"] = rows
		report["completed_cases"] = len(rows)
		report["unattempted_cases"] = expectedCount - len(rows)
		report["failed_cases"] = failed
		require.NoError(t, json.NewEncoder(journal).Encode(row))
		require.NoError(t, journal.Sync())
		fmt.Printf("ASR %s pass=%t\n", sample.ID, passed)
		if os.Getenv("ENWORDS_PRODUCT_ASR_PROBE") == "1" {
			report["scope"] = "single first-sample availability diagnostic, not 16-case quality acceptance"
			break
		}
		if consecutive >= 5 {
			report["stopped_reason"] = "five_consecutive_provider_failures"
			break
		}
	}
	if failed > 0 || (len(rows) != expectedCount && os.Getenv("ENWORDS_PRODUCT_ASR_PROBE") != "1") {
		t.Errorf("ASR run failed/incomplete: %d failures / %d attempted", failed, len(rows))
	}
}

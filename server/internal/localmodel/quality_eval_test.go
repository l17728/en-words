package localmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"enwords/internal/obs"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is an opt-in adapter quality run, not an HTTP/browser acceptance test.
// No model configuration is changed and the frozen sample set is never rewritten.
func TestOptInLocalSpeechQuality(t *testing.T) {
	mode := os.Getenv("ENWORDS_LOCAL_SPEECH_EVAL")
	if mode != "asr" && mode != "tts" {
		t.Skip("explicit local speech quality opt-in required")
	}
	labelPath := "../../../review/audio-fixtures/manifest.json"
	if mode == "tts" {
		labelPath = "../../../review/tts-local-cases.json"
	}
	labels, err := os.ReadFile(labelPath)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(labelPath + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(labels)
	labelSHA := hex.EncodeToString(hash[:])
	if labelSHA != strings.TrimSpace(string(expected)) {
		t.Fatal("frozen label digest mismatch")
	}
	var dataset struct {
		Status  string `json:"status"`
		Samples []struct {
			ID       string `json:"case_id"`
			Text     string `json:"text"`
			Language string `json:"language"`
			File     string `json:"file"`
			SHA      string `json:"sha256"`
		} `json:"samples"`
	}
	if err = json.Unmarshal(labels, &dataset); err != nil || dataset.Status != "frozen_reviewed" {
		t.Fatal("labels not reviewed")
	}
	expectedCount := 16
	if mode == "tts" {
		expectedCount = 12
	}
	if len(dataset.Samples) != expectedCount {
		t.Fatal("unexpected sample count")
	}
	output := os.Getenv("ENWORDS_LOCAL_SPEECH_OUTPUT")
	if output == "" {
		t.Fatal("new report output path required")
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"mode": mode, "label_sha256": labelSHA, "human_listening": "pending", "scope": "production local adapter, synthetic independent English ASR or Chinese TTS; not browser/product-route acceptance", "started_at": time.Now().Format(time.RFC3339)}
	defer func() {
		report["finished_at"] = time.Now().Format(time.RFC3339)
		if err := json.NewEncoder(file).Encode(report); err != nil {
			t.Error(err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	journal, err := os.OpenFile(output+".jsonl", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	}()
	report["expected_cases"] = expectedCount
	client, err := New("http://127.0.0.1:8090", obs.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Discover(context.Background()); err != nil {
		report["fatal_error"] = err.Error()
		t.Fatal(err)
	}
	report["models"] = client.Services
	rows := []map[string]any{}
	failed, consecutive := 0, 0
	for _, sample := range dataset.Samples {
		row := map[string]any{"case_id": sample.ID, "reference": sample.Text, "language": sample.Language, "human_listening": "pending"}
		start := time.Now()
		ctx := obs.WithTrace(context.Background(), "speech-eval-"+sample.ID)
		var callErr error
		if mode == "asr" {
			if filepath.IsAbs(sample.File) || strings.Contains(sample.File, "..") {
				t.Fatal("unsafe fixture path")
			}
			audio, e := os.ReadFile(filepath.Join(filepath.Dir(labelPath), sample.File))
			if e != nil {
				t.Fatal(e)
			}
			digest := sha256.Sum256(audio)
			if hex.EncodeToString(digest[:]) != sample.SHA {
				t.Fatal("audio digest mismatch")
			}
			var transcript string
			transcript, callErr = client.Transcribe(ctx, audio, "independent.wav", sample.Language)
			row["transcript"] = transcript
			row["source_sha256"] = sample.SHA
		} else {
			var audio []byte
			audio, callErr = client.Synthesize(ctx, sample.Text, sample.Language)
			if callErr == nil {
				name := output + "." + sample.ID + ".wav"
				f, e := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				_, e = f.Write(audio)
				closeErr := f.Close()
				if e != nil || closeErr != nil {
					t.Fatal("audio write failed")
				}
				digest := sha256.Sum256(audio)
				row["file"] = name
				row["sha256"] = hex.EncodeToString(digest[:])
				row["bytes"] = len(audio)
			}
		}
		row["duration_ms"] = time.Since(start).Milliseconds()
		row["transport_and_format_pass"] = callErr == nil
		if callErr != nil {
			row["error"] = callErr.Error()
			failed++
			consecutive++
		} else {
			consecutive = 0
		}
		rows = append(rows, row)
		report["cases"] = rows
		report["completed_cases"] = len(rows)
		report["failed_cases"] = failed
		report["unattempted_cases"] = len(dataset.Samples) - len(rows)
		if err = json.NewEncoder(journal).Encode(row); err != nil {
			t.Fatal(err)
		}
		if err = journal.Sync(); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s %s pass=%t\n", mode, sample.ID, callErr == nil)
		if consecutive >= 5 {
			report["stopped_reason"] = "five_consecutive_provider_failures"
			break
		}
	}
	if failed > 0 || len(rows) != len(dataset.Samples) {
		t.Errorf("provider quality run incomplete or failed: %d/%d attempted failed", failed, len(rows))
	}
}

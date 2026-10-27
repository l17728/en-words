package api

// This file contains the test-binary-only control protocol for the first-audio
// matrix. It deliberately does not add a production route, CLI, or listener.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	firstAudioHarnessGateEnv = "EN_WORDS_FIRST_AUDIO_MATRIX_HARNESS"
	firstAudioHarnessCapEnv  = "EN_WORDS_FIRST_AUDIO_MATRIX_CAPABILITY"
	firstAudioHarnessRunEnv  = "EN_WORDS_FIRST_AUDIO_MATRIX_RUN_ID"
	firstAudioHarnessDBEnv   = "EN_WORDS_FIRST_AUDIO_MATRIX_DB_PATH"
	firstAudioHarnessGate    = "1"
	firstAudioFixture        = "first_audio_v1"
	firstAudioMaxLine        = 64 * 1024
)

var (
	firstAudioIDPattern            = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	firstAudioRunIDPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
	errFirstAudioFatal             = errors.New("first-audio matrix protocol failure")
	errMatrixBackendNotImplemented = errors.New("isolated guarded matrix backend is not implemented")
)

type firstAudioState string

const (
	firstAudioUnbound        firstAudioState = "UNBOUND"
	firstAudioStateBound     firstAudioState = "BOUND"
	firstAudioResetting      firstAudioState = "RESETTING"
	firstAudioTerminalFailed firstAudioState = "TERMINAL_FAILED"
	firstAudioShutdown       firstAudioState = "SHUTDOWN"
)

type firstAudioRequest struct {
	RequestID  string
	Capability string
	Command    string
	Args       map[string]any
	Raw        []byte
}

type firstAudioResponse struct {
	RequestID string           `json:"request_id"`
	Status    string           `json:"status"`
	Data      any              `json:"data"`
	Error     *firstAudioError `json:"error"`
}

type firstAudioError struct {
	Code string `json:"code"`
}

type firstAudioResult struct {
	status string
	data   any
	code   string
}

type firstAudioRequestRecord struct {
	raw        []byte
	response   []byte
	replayable bool
}

type firstAudioResetRecord struct {
	lessonID int64
	result   firstAudioResult
}

type firstAudioBound struct {
	Origin              string  `json:"-"`
	SourceBuildIdentity string  `json:"-"`
	StudentID           int64   `json:"student_id"`
	BookID              int64   `json:"book_id"`
	WordIDs             []int64 `json:"word_ids"`
	LessonID            int64   `json:"lesson_id"`
	Generation          uint64  `json:"generation"`
}

type firstAudioBackend interface {
	Bind(context.Context, string) (firstAudioBound, error)
	Reset(context.Context, int64) (firstAudioBound, error)
	Close() error
}

type firstAudioBackendFactory func() (firstAudioBackend, error)

type firstAudioControl struct {
	capability []byte
	newBackend firstAudioBackendFactory
	state      firstAudioState
	bound      firstAudioBound
	resetCount uint64
	terminal   string
	backend    firstAudioBackend
	closed     bool
	closeErr   error
	requests   map[string]firstAudioRequestRecord
	resets     map[string]firstAudioResetRecord
}

func newFirstAudioControl(capability []byte, factory firstAudioBackendFactory) *firstAudioControl {
	return &firstAudioControl{
		capability: append([]byte(nil), capability...),
		newBackend: factory,
		state:      firstAudioUnbound,
		requests:   make(map[string]firstAudioRequestRecord),
		resets:     make(map[string]firstAudioResetRecord),
	}
}

// TestFirstAudioMatrixControlHarness is selected only by the dedicated test
// binary invocation. The production implementation of the isolated SQLite
// bootstrap is intentionally absent; bind therefore fails closed until that
// reviewed backend is implemented.
func TestFirstAudioMatrixControlHarness(t *testing.T) {
	if os.Getenv(firstAudioHarnessGateEnv) != firstAudioHarnessGate {
		t.Skip("dedicated first-audio matrix harness only")
	}
	capability, err := firstAudioCapabilityFromEnvironment()
	if err != nil {
		t.Errorf("first-audio matrix harness environment rejected: %v", err)
		return
	}
	control := newFirstAudioControl(capability, unavailableFirstAudioBackend)
	if err := control.serve(os.Stdin, os.Stdout); err != nil {
		// The Go test runner's failure trailer is emitted only after the protocol
		// has terminated; never write a non-JSON diagnostic into the control stream.
		fmt.Fprintf(os.Stderr, "first-audio matrix harness failed: %v\n", err)
		t.Errorf("first-audio matrix harness failed")
	}
}

func unavailableFirstAudioBackend() (firstAudioBackend, error) {
	return nil, errMatrixBackendNotImplemented
}

func firstAudioCapabilityFromEnvironment() ([]byte, error) {
	if os.Getenv(firstAudioHarnessGateEnv) != firstAudioHarnessGate {
		return nil, errors.New("explicit harness gate is required")
	}
	encoded := os.Getenv(firstAudioHarnessCapEnv)
	capability, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(capability) < 32 {
		return nil, errors.New("capability environment value is invalid")
	}
	runID := os.Getenv(firstAudioHarnessRunEnv)
	if !firstAudioRunIDPattern.MatchString(runID) {
		return nil, errors.New("run ID environment value is invalid")
	}
	dbPath := os.Getenv(firstAudioHarnessDBEnv)
	if !filepath.IsAbs(dbPath) || filepath.Clean(dbPath) != dbPath || !strings.EqualFold(filepath.Base(dbPath), "matrix.db") {
		return nil, errors.New("database path must be absolute and clean")
	}
	artifactRoot := filepath.Dir(filepath.Dir(dbPath))
	runRoot := filepath.Dir(dbPath)
	for _, item := range []struct{ path, wantName string }{
		{path: artifactRoot, wantName: ".test-artifacts"},
		{path: runRoot, wantName: "first-audio-matrix-" + runID},
	} {
		path, wantName := item.path, item.wantName
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Base(path), wantName) {
			return nil, errors.New("run root is missing, redirected, or misnamed")
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || !firstAudioSamePath(canonical, path) {
			return nil, errors.New("run root is not canonical")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("run root permissions are not private")
		}
	}
	if !firstAudioSamePath(filepath.Dir(runRoot), artifactRoot) {
		return nil, errors.New("run root is outside the private artifact directory")
	}
	if err := verifyFirstAudioPrivateDirectory(runRoot); err != nil {
		return nil, errors.New("run root permissions are not private")
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(dbPath + suffix); err == nil || !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("database or sidecar already exists")
		}
	}
	pathParts := strings.Split(strings.ToLower(dbPath), string(filepath.Separator))
	artifactRootFound := false
	matchingRunDirFound := false
	for index, part := range pathParts {
		if part != ".test-artifacts" {
			continue
		}
		artifactRootFound = true
		if index+1 < len(pathParts) && pathParts[index+1] == "first-audio-matrix-"+strings.ToLower(runID) {
			matchingRunDirFound = true
		}
	}
	if !artifactRootFound || !matchingRunDirFound || !strings.EqualFold(filepath.Base(filepath.Dir(dbPath)), "first-audio-matrix-"+runID) {
		return nil, errors.New("database path must be matrix.db below this run's .test-artifacts directory")
	}
	return capability, nil
}

func firstAudioSamePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		infoA, errA := os.Stat(a)
		infoB, errB := os.Stat(b)
		return errA == nil && errB == nil && os.SameFile(infoA, infoB)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func normalizeFirstAudioWindowsPath(path string) string {
	clean := filepath.Clean(path)
	upper := strings.ToUpper(clean)
	const extendedUNC = `\\?\UNC\`
	if strings.HasPrefix(upper, extendedUNC) {
		clean = `\\` + clean[len(extendedUNC):]
	} else if strings.HasPrefix(upper, `\\?\`) {
		clean = clean[len(`\\?\`):]
	}
	return filepath.Clean(clean)
}

func (c *firstAudioControl) serve(input io.Reader, output io.Writer) error {
	reader := bufio.NewReaderSize(input, 4096)
	for c.state != firstAudioShutdown {
		line, err := readFirstAudioLine(reader)
		if err != nil {
			c.closeBackend()
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("%w: EOF before shutdown", errFirstAudioFatal)
			}
			return fmt.Errorf("%w: %v", errFirstAudioFatal, err)
		}
		request, err := parseFirstAudioRequest(line)
		if err != nil || !c.validCapability(request.Capability) {
			c.closeBackend()
			return fmt.Errorf("%w: invalid request", errFirstAudioFatal)
		}
		response, replayable, fatal, exitErr := c.handle(request)
		if fatal != nil {
			c.closeBackend()
			return fmt.Errorf("%w: %v", errFirstAudioFatal, fatal)
		}
		encoded, err := marshalFirstAudioResponse(response)
		if err != nil || writeFirstAudioResponse(output, encoded) != nil {
			c.closeBackend()
			return fmt.Errorf("%w: response write failed", errFirstAudioFatal)
		}
		c.requests[request.RequestID] = firstAudioRequestRecord{
			raw: append([]byte(nil), request.Raw...), response: append([]byte(nil), encoded...), replayable: replayable,
		}
		if exitErr != nil {
			return exitErr
		}
	}
	return nil
}

func readFirstAudioLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > firstAudioMaxLine+2 {
			return nil, errors.New("request line exceeds 64 KiB")
		}
		line = append(line, part...)
		if err == nil {
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) == 0 || len(line) > firstAudioMaxLine {
				return nil, errors.New("empty or oversized request line")
			}
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil, io.EOF
		}
		if errors.Is(err, io.EOF) {
			return nil, errors.New("unterminated JSONL request")
		}
		return nil, err
	}
}

func parseFirstAudioRequest(line []byte) (firstAudioRequest, error) {
	if len(line) == 0 || len(line) > firstAudioMaxLine || !utf8.Valid(line) {
		return firstAudioRequest{}, errors.New("invalid request bytes")
	}
	value, err := decodeFirstAudioJSON(line)
	if err != nil {
		return firstAudioRequest{}, err
	}
	fields, ok := value.(map[string]any)
	if !ok || !exactFirstAudioFields(fields, "request_id", "capability", "command", "args") {
		return firstAudioRequest{}, errors.New("request must contain exactly four fields")
	}
	requestID, ok1 := fields["request_id"].(string)
	capability, ok2 := fields["capability"].(string)
	command, ok3 := fields["command"].(string)
	args, ok4 := fields["args"].(map[string]any)
	if !ok1 || !ok2 || !ok3 || !ok4 || !firstAudioIDPattern.MatchString(requestID) {
		return firstAudioRequest{}, errors.New("request field types or IDs are invalid")
	}
	if !validFirstAudioRequestArgs(command, args) {
		return firstAudioRequest{}, errors.New("request args contain unknown fields")
	}
	return firstAudioRequest{RequestID: requestID, Capability: capability, Command: command, Args: args, Raw: append([]byte(nil), line...)}, nil
}

func validFirstAudioRequestArgs(command string, args map[string]any) bool {
	switch command {
	case "bind":
		return exactFirstAudioFields(args, "fixture")
	case "metadata", "shutdown":
		return len(args) == 0
	case "reset":
		return exactFirstAudioFields(args, "reset_id", "lesson_id")
	default:
		return len(args) == 0
	}
}

func decodeFirstAudioJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readFirstAudioJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return value, nil
}

func readFirstAudioJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate JSON key %q", key)
			}
			value, err := readFirstAudioJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return nil, errors.New("unterminated JSON object")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := readFirstAudioJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
			return nil, errors.New("unterminated JSON array")
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

func exactFirstAudioFields(fields map[string]any, expected ...string) bool {
	if len(fields) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func (c *firstAudioControl) validCapability(encoded string) bool {
	provided, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(provided) != len(c.capability) {
		return false
	}
	return subtle.ConstantTimeCompare(provided, c.capability) == 1
}

func (c *firstAudioControl) handle(request firstAudioRequest) (firstAudioResponse, bool, error, error) {
	if prior, exists := c.requests[request.RequestID]; exists {
		if prior.replayable && bytes.Equal(prior.raw, request.Raw) {
			var response firstAudioResponse
			if err := json.Unmarshal(prior.response, &response); err != nil {
				return firstAudioResponse{}, false, err, nil
			}
			return response, true, nil, nil
		}
		return firstAudioResponse{}, false, errors.New("duplicate request ID"), nil
	}

	// Reset replay resolution intentionally precedes current state/lesson checks.
	if request.Command == "reset" {
		resetID, lessonID, err := parseFirstAudioResetArgs(request.Args)
		if err != nil {
			return firstAudioResponse{}, false, err, nil
		}
		if prior, exists := c.resets[resetID]; exists {
			if prior.lessonID != lessonID {
				return c.reject(request.RequestID, "reset_id_payload_conflict"), false, nil, nil
			}
			response := firstAudioResultResponse(request.RequestID, prior.result)
			return response, true, nil, nil
		}
	}

	switch request.Command {
	case "bind":
		if !exactFirstAudioFields(request.Args, "fixture") {
			return firstAudioResponse{}, false, errors.New("bind args schema invalid"), nil
		}
		fixture, ok := request.Args["fixture"].(string)
		if !ok {
			return firstAudioResponse{}, false, errors.New("bind fixture must be a string"), nil
		}
		if c.state != firstAudioUnbound {
			return c.reject(request.RequestID, "invalid_state"), false, nil, nil
		}
		if fixture != firstAudioFixture {
			return c.reject(request.RequestID, "fixture_unavailable"), false, nil, nil
		}
		if c.newBackend == nil {
			response, replayable, err := c.failBind(request.RequestID)
			return response, replayable, nil, err
		}
		backend, err := c.newBackend()
		if err != nil {
			response, replayable, _ := c.failBind(request.RequestID)
			return response, replayable, nil, nil
		}
		if backend == nil {
			response, replayable, err := c.failBind(request.RequestID)
			return response, replayable, nil, err
		}
		c.backend = backend
		bound, err := backend.Bind(context.Background(), fixture)
		if err != nil || !validFirstAudioBound(bound) {
			response, replayable, _ := c.failBind(request.RequestID)
			return response, replayable, nil, nil
		}
		c.bound = cloneFirstAudioBound(bound)
		c.state = firstAudioStateBound
		return firstAudioResponse{RequestID: request.RequestID, Status: "ok", Data: cloneFirstAudioBound(bound), Error: nil}, false, nil, nil

	case "metadata":
		if len(request.Args) != 0 {
			return firstAudioResponse{}, false, errors.New("metadata args must be empty"), nil
		}
		return firstAudioResponse{RequestID: request.RequestID, Status: "ok", Data: c.metadata(), Error: nil}, false, nil, nil

	case "reset":
		resetID, lessonID, _ := parseFirstAudioResetArgs(request.Args) // validated before replay lookup
		if c.state != firstAudioStateBound {
			return c.reject(request.RequestID, "invalid_state"), false, nil, nil
		}
		if lessonID != c.bound.LessonID {
			return c.reject(request.RequestID, "lesson_mismatch"), false, nil, nil
		}
		if c.backend == nil {
			response, replayable, err := c.failReset(request.RequestID, resetID, lessonID, false)
			return response, replayable, nil, err
		}
		c.state = firstAudioResetting
		updated, err := c.backend.Reset(context.Background(), lessonID)
		if err != nil {
			response, replayable, _ := c.failReset(request.RequestID, resetID, lessonID, errors.Is(err, errFirstAudioOutcomeUncertain))
			// Preserve the terminal controller so it can answer metadata and one
			// shutdown command. Never expose provider/SQL error text on the wire.
			return response, replayable, nil, nil
		}
		if !validFirstAudioBound(updated) || updated.StudentID != c.bound.StudentID || updated.BookID != c.bound.BookID ||
			!equalFirstAudioWordIDs(updated.WordIDs, c.bound.WordIDs) || updated.LessonID == lessonID || updated.Generation <= c.bound.Generation {
			response, replayable, err := c.failReset(request.RequestID, resetID, lessonID, true)
			return response, replayable, nil, err
		}
		c.bound = cloneFirstAudioBound(updated)
		c.resetCount++
		c.state = firstAudioStateBound
		result := firstAudioResult{status: "ok", data: map[string]any{
			"lesson_id": updated.LessonID, "generation": updated.Generation,
			"word_ids": append([]int64(nil), updated.WordIDs...),
		}}
		c.resets[resetID] = firstAudioResetRecord{lessonID: lessonID, result: result}
		return firstAudioResultResponse(request.RequestID, result), true, nil, nil

	case "shutdown":
		if len(request.Args) != 0 {
			return firstAudioResponse{}, false, errors.New("shutdown args must be empty"), nil
		}
		if c.state == firstAudioTerminalFailed {
			code := c.terminal
			c.closeBackend()
			c.state = firstAudioShutdown
			return firstAudioResponse{RequestID: request.RequestID, Status: "failed", Data: nil, Error: &firstAudioError{Code: code}}, false, nil, errFirstAudioRunFailed
		}
		if c.state == firstAudioUnbound || (c.state == firstAudioStateBound && c.resetCount == 0) {
			c.closeBackend()
			c.state = firstAudioShutdown
			return firstAudioResponse{RequestID: request.RequestID, Status: "failed", Data: nil, Error: &firstAudioError{Code: "shutdown_incomplete"}}, false, nil, errFirstAudioRunFailed
		}
		if c.state != firstAudioStateBound {
			return c.reject(request.RequestID, "invalid_state"), false, nil, nil
		}
		if err := c.closeBackend(); err != nil {
			c.state = firstAudioShutdown
			return firstAudioResponse{RequestID: request.RequestID, Status: "failed", Data: nil, Error: &firstAudioError{Code: "shutdown_failed"}}, false, nil, errFirstAudioRunFailed
		}
		c.state = firstAudioShutdown
		return firstAudioResponse{RequestID: request.RequestID, Status: "ok", Data: map[string]any{}, Error: nil}, false, nil, nil

	default:
		return c.reject(request.RequestID, "command_unsupported"), false, nil, nil
	}
}

var errFirstAudioOutcomeUncertain = errors.New("first-audio matrix outcome uncertain")
var errFirstAudioRunFailed = errors.New("first-audio matrix run failed")

func (c *firstAudioControl) reject(requestID, code string) firstAudioResponse {
	return firstAudioResponse{RequestID: requestID, Status: "rejected", Data: nil, Error: &firstAudioError{Code: code}}
}

func (c *firstAudioControl) failBind(requestID string) (firstAudioResponse, bool, error) {
	cleanupErr := c.closeBackend()
	c.state = firstAudioTerminalFailed
	c.terminal = "bind_setup_failed"
	return firstAudioResponse{RequestID: requestID, Status: "failed", Data: nil, Error: &firstAudioError{Code: c.terminal}}, false, cleanupErr
}

func (c *firstAudioControl) failReset(requestID, resetID string, lessonID int64, uncertain bool) (firstAudioResponse, bool, error) {
	if c.closeBackend() != nil {
		uncertain = true
	}
	c.state = firstAudioTerminalFailed
	status, code := "failed", "reset_failed"
	if uncertain {
		status, code = "outcome_uncertain", "reset_outcome_uncertain"
	}
	c.terminal = code
	result := firstAudioResult{status: status, code: code}
	c.resets[resetID] = firstAudioResetRecord{lessonID: lessonID, result: result}
	return firstAudioResultResponse(requestID, result), true, nil
}

func (c *firstAudioControl) closeBackend() error {
	if c.closed {
		return c.closeErr
	}
	if c.backend == nil {
		c.closed = true
		return nil
	}
	c.closed = true
	c.closeErr = c.backend.Close()
	return c.closeErr
}

func (c *firstAudioControl) metadata() map[string]any {
	var origin any
	var studentID, bookID, lessonID any
	var generation uint64
	if c.bound.LessonID > 0 {
		studentID, bookID, lessonID, generation = c.bound.StudentID, c.bound.BookID, c.bound.LessonID, c.bound.Generation
		if c.state == firstAudioStateBound || c.state == firstAudioResetting {
			origin = c.bound.Origin
		}
	}
	data := map[string]any{
		"loopback_origin":        origin,
		"student_id":             studentID,
		"book_id":                bookID,
		"lesson_id":              lessonID,
		"generation":             generation,
		"lifecycle_state":        c.state,
		"successful_reset_count": c.resetCount,
		"source_build_identity":  c.bound.SourceBuildIdentity,
		"word_ids":               append([]int64(nil), c.bound.WordIDs...),
	}
	if c.terminal != "" {
		data["terminal_code"] = c.terminal
	}
	return data
}

func parseFirstAudioResetArgs(args map[string]any) (string, int64, error) {
	if !exactFirstAudioFields(args, "reset_id", "lesson_id") {
		return "", 0, errors.New("reset args schema invalid")
	}
	resetID, ok := args["reset_id"].(string)
	if !ok || !firstAudioIDPattern.MatchString(resetID) {
		return "", 0, errors.New("reset ID invalid")
	}
	lessonNumber, ok := args["lesson_id"].(json.Number)
	if !ok {
		return "", 0, errors.New("lesson ID must be an integer")
	}
	lessonID, err := lessonNumber.Int64()
	if err != nil || lessonID <= 0 {
		return "", 0, errors.New("lesson ID invalid")
	}
	return resetID, lessonID, nil
}

func validFirstAudioBound(bound firstAudioBound) bool {
	if bound.StudentID <= 0 || bound.BookID <= 0 || bound.LessonID <= 0 || bound.Generation == 0 || bound.SourceBuildIdentity == "" || len(bound.WordIDs) == 0 || !validFirstAudioOrigin(bound.Origin) {
		return false
	}
	seen := make(map[int64]struct{}, len(bound.WordIDs))
	for _, wordID := range bound.WordIDs {
		if wordID <= 0 {
			return false
		}
		if _, exists := seen[wordID]; exists {
			return false
		}
		seen[wordID] = struct{}{}
	}
	return true
}

func equalFirstAudioWordIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneFirstAudioBound(bound firstAudioBound) firstAudioBound {
	bound.WordIDs = append([]int64(nil), bound.WordIDs...)
	return bound
}

func validFirstAudioOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Hostname() != "127.0.0.1" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port > 0 && port < 65536
}

type firstAudioFakeBackend struct {
	bound       firstAudioBound
	resetResult firstAudioBound
	resetErr    error
	bindCalls   int
	resetCalls  int
	closeCalls  int
}

func (b *firstAudioFakeBackend) Bind(_ context.Context, fixture string) (firstAudioBound, error) {
	b.bindCalls++
	if fixture != firstAudioFixture {
		return firstAudioBound{}, errors.New("unexpected fixture")
	}
	return b.bound, nil
}

func (b *firstAudioFakeBackend) Reset(_ context.Context, lessonID int64) (firstAudioBound, error) {
	b.resetCalls++
	if lessonID != b.bound.LessonID {
		return firstAudioBound{}, errors.New("unexpected lesson")
	}
	if b.resetErr != nil {
		return firstAudioBound{}, b.resetErr
	}
	b.bound = b.resetResult
	return b.resetResult, nil
}

func (b *firstAudioFakeBackend) Close() error {
	b.closeCalls++
	return nil
}

type firstAudioMutatingWordSetBackend struct {
	bound      firstAudioBound
	closeCalls int
}

func (b *firstAudioMutatingWordSetBackend) Bind(_ context.Context, fixture string) (firstAudioBound, error) {
	if fixture != firstAudioFixture {
		return firstAudioBound{}, errors.New("unexpected fixture")
	}
	return b.bound, nil
}

func (b *firstAudioMutatingWordSetBackend) Reset(_ context.Context, lessonID int64) (firstAudioBound, error) {
	if lessonID != b.bound.LessonID {
		return firstAudioBound{}, errors.New("unexpected lesson")
	}
	// Simulate a backend reusing and mutating the exact slice returned by Bind.
	b.bound.WordIDs[1] = 15
	b.bound.LessonID = 102
	b.bound.Generation = 2
	return b.bound, nil
}

func (b *firstAudioMutatingWordSetBackend) Close() error {
	b.closeCalls++
	return nil
}

func firstAudioTestCapability() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func firstAudioRequestLine(t *testing.T, requestID, capability, command string, args map[string]any) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"request_id": requestID,
		"capability": capability,
		"command":    command,
		"args":       args,
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func decodeFirstAudioTestResponses(t *testing.T, raw []byte) []firstAudioResponse {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	responses := make([]firstAudioResponse, 0, len(lines))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var response firstAudioResponse
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatalf("decode response: %v; line=%q", err, line)
		}
		responses = append(responses, response)
	}
	return responses
}

func TestFirstAudioMatrixStrictJSONLParser(t *testing.T) {
	good := []byte(`{"request_id":"req-1","capability":"abc","command":"metadata","args":{}}`)
	if _, err := parseFirstAudioRequest(good); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := map[string][]byte{
		"duplicate top-level key": []byte(`{"request_id":"a","request_id":"b","capability":"x","command":"metadata","args":{}}`),
		"duplicate nested key":    []byte(`{"request_id":"a","capability":"x","command":"metadata","args":{"x":1,"x":2}}`),
		"unknown top-level field": []byte(`{"request_id":"a","capability":"x","command":"metadata","args":{},"extra":1}`),
		"unknown nested field":    []byte(`{"request_id":"a","capability":"x","command":"metadata","args":{"extra":1}}`),
		"trailing value":          []byte(`{"request_id":"a","capability":"x","command":"metadata","args":{}} {}`),
		"invalid UTF-8":           append([]byte(`{"request_id":"a","capability":"x","command":"metadata","args":{"x":"`), 0xff, '"', '}', '}'),
		"bad request ID":          []byte(`{"request_id":"bad/id","capability":"x","command":"metadata","args":{}}`),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseFirstAudioRequest(input); err == nil {
				t.Fatal("invalid request was accepted")
			}
		})
	}

	line, err := readFirstAudioLine(bufio.NewReader(strings.NewReader("{}\r\n")))
	if err != nil || string(line) != "{}" {
		t.Fatalf("CRLF framing: line=%q err=%v", line, err)
	}
	if _, err := readFirstAudioLine(bufio.NewReader(strings.NewReader("{}"))); err == nil {
		t.Fatal("unterminated JSONL line was accepted")
	}
	oversized := strings.Repeat("x", firstAudioMaxLine+1) + "\n"
	if _, err := readFirstAudioLine(bufio.NewReader(strings.NewReader(oversized))); err == nil {
		t.Fatal("oversized request line was accepted")
	}
}

func TestFirstAudioMatrixEnvironmentGateAndCapability(t *testing.T) {
	capability := base64.RawURLEncoding.EncodeToString(firstAudioTestCapability())
	t.Setenv(firstAudioHarnessGateEnv, firstAudioHarnessGate)
	t.Setenv(firstAudioHarnessCapEnv, capability)
	t.Setenv(firstAudioHarnessRunEnv, "run_20261026_1")
	artifactRoot := filepath.Join(t.TempDir(), ".test-artifacts")
	runRoot := filepath.Join(artifactRoot, "first-audio-matrix-run_20261026_1")
	if err := os.MkdirAll(filepath.Dir(runRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := createFirstAudioPrivateTestDirectory(runRoot); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Mkdir(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(runRoot, "matrix.db")
	t.Setenv(firstAudioHarnessDBEnv, dbPath)
	got, err := firstAudioCapabilityFromEnvironment()
	if err != nil || subtle.ConstantTimeCompare(got, firstAudioTestCapability()) != 1 {
		t.Fatalf("valid harness environment rejected: err=%v", err)
	}
	t.Setenv(firstAudioHarnessGateEnv, "true")
	if _, err := firstAudioCapabilityFromEnvironment(); err == nil {
		t.Fatal("noncanonical harness gate accepted")
	}
	t.Setenv(firstAudioHarnessGateEnv, firstAudioHarnessGate)
	t.Setenv(firstAudioHarnessCapEnv, "short")
	if _, err := firstAudioCapabilityFromEnvironment(); err == nil {
		t.Fatal("short capability accepted")
	}
	t.Setenv(firstAudioHarnessCapEnv, capability)
	if err := os.WriteFile(dbPath+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := firstAudioCapabilityFromEnvironment(); err == nil {
		t.Fatal("pre-existing SQLite sidecar accepted")
	}
}

func TestFirstAudioMatrixWindowsCanonicalPathComparison(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows extended path prefixes are platform-specific")
	}
	if got := normalizeFirstAudioWindowsPath(`\\?\C:\matrix\run`); !strings.EqualFold(got, `C:\matrix\run`) {
		t.Fatalf("extended drive path normalization=%q", got)
	}
	if got := normalizeFirstAudioWindowsPath(`\\?\UNC\server\share\matrix`); !strings.EqualFold(got, `\\server\share\matrix`) {
		t.Fatalf("extended UNC path normalization=%q", got)
	}
	directory := t.TempDir()
	extended := `\\?\` + directory
	if !firstAudioSamePath(extended, directory) {
		t.Fatal("extended drive path did not identify the same directory")
	}
	if firstAudioSamePath(directory, t.TempDir()) {
		t.Fatal("different Windows directories compared equal")
	}
}

func TestFirstAudioMatrixResetReplayAndShutdownStateMachine(t *testing.T) {
	capability := firstAudioTestCapability()
	encodedCapability := base64.RawURLEncoding.EncodeToString(capability)
	backend := &firstAudioFakeBackend{
		bound:       firstAudioBound{Origin: "http://127.0.0.1:12345", SourceBuildIdentity: "unit-test-only", StudentID: 11, BookID: 12, WordIDs: []int64{13, 14}, LessonID: 101, Generation: 1},
		resetResult: firstAudioBound{Origin: "http://127.0.0.1:12345", SourceBuildIdentity: "unit-test-only", StudentID: 11, BookID: 12, WordIDs: []int64{13, 14}, LessonID: 102, Generation: 2},
	}
	control := newFirstAudioControl(capability, func() (firstAudioBackend, error) { return backend, nil })
	bind := firstAudioRequestLine(t, "bind-1", encodedCapability, "bind", map[string]any{"fixture": firstAudioFixture})
	reset := firstAudioRequestLine(t, "reset-1", encodedCapability, "reset", map[string]any{"reset_id": "reset_a", "lesson_id": 101})
	resetNewRequestID := firstAudioRequestLine(t, "reset-2", encodedCapability, "reset", map[string]any{"reset_id": "reset_a", "lesson_id": 101})
	conflict := firstAudioRequestLine(t, "reset-3", encodedCapability, "reset", map[string]any{"reset_id": "reset_a", "lesson_id": 999})
	metadata := firstAudioRequestLine(t, "metadata-1", encodedCapability, "metadata", map[string]any{})
	shutdown := firstAudioRequestLine(t, "shutdown-1", encodedCapability, "shutdown", map[string]any{})
	input := bytes.Join([][]byte{bind, reset, reset, resetNewRequestID, conflict, metadata, shutdown}, nil)
	var output bytes.Buffer
	if err := control.serve(bytes.NewReader(input), &output); err != nil {
		t.Fatalf("protocol serve failed: %v", err)
	}
	responses := decodeFirstAudioTestResponses(t, output.Bytes())
	if len(responses) != 7 {
		t.Fatalf("response count=%d, want 7; output=%s", len(responses), output.String())
	}
	if responses[0].Status != "ok" || responses[1].Status != "ok" || responses[2].Status != "ok" || responses[3].Status != "ok" {
		t.Fatalf("bind/reset/replay unexpectedly failed: %#v", responses[:4])
	}
	if responses[2].RequestID != "reset-1" || responses[3].RequestID != "reset-2" {
		t.Fatalf("replay did not echo each request ID: %#v", responses[2:4])
	}
	wantWordIDs := []any{float64(13), float64(14)}
	if got := responses[0].Data.(map[string]any)["word_ids"]; !reflect.DeepEqual(got, wantWordIDs) {
		t.Fatalf("bind did not return the frozen word IDs: got=%#v want=%#v", got, wantWordIDs)
	}
	if responses[1].Data.(map[string]any)["lesson_id"] != float64(102) || responses[3].Data.(map[string]any)["lesson_id"] != float64(102) {
		t.Fatalf("replay did not return the recorded replacement lesson: reset=%#v replay=%#v", responses[1], responses[3])
	}
	if got := responses[1].Data.(map[string]any)["word_ids"]; !reflect.DeepEqual(got, wantWordIDs) {
		t.Fatalf("reset result changed frozen word IDs: got=%#v want=%#v", got, wantWordIDs)
	}
	if responses[4].Status != "rejected" || responses[4].Error == nil || responses[4].Error.Code != "reset_id_payload_conflict" {
		t.Fatalf("changed reset payload was not rejected: %#v", responses[4])
	}
	metadataData := responses[5].Data.(map[string]any)
	if metadataData["successful_reset_count"] != float64(1) || metadataData["lesson_id"] != float64(102) ||
		!reflect.DeepEqual(metadataData["word_ids"], wantWordIDs) {
		t.Fatalf("metadata after replay is inconsistent: %#v", metadataData)
	}
	if responses[6].Status != "ok" || control.state != firstAudioShutdown || backend.resetCalls != 1 || backend.closeCalls != 1 {
		t.Fatalf("shutdown/reset accounting mismatch: response=%#v state=%s resets=%d closes=%d", responses[6], control.state, backend.resetCalls, backend.closeCalls)
	}
}

func TestFirstAudioMatrixResetRejectsChangedFrozenWordIDs(t *testing.T) {
	capability := firstAudioTestCapability()
	encodedCapability := base64.RawURLEncoding.EncodeToString(capability)
	backend := &firstAudioFakeBackend{
		bound:       firstAudioBound{Origin: "http://127.0.0.1:12345", SourceBuildIdentity: "unit-test-only", StudentID: 11, BookID: 12, WordIDs: []int64{13, 14}, LessonID: 101, Generation: 1},
		resetResult: firstAudioBound{Origin: "http://127.0.0.1:12345", SourceBuildIdentity: "unit-test-only", StudentID: 11, BookID: 12, WordIDs: []int64{13, 15}, LessonID: 102, Generation: 2},
	}
	control := newFirstAudioControl(capability, func() (firstAudioBackend, error) { return backend, nil })
	bind := firstAudioRequestLine(t, "bind-1", encodedCapability, "bind", map[string]any{"fixture": firstAudioFixture})
	reset := firstAudioRequestLine(t, "reset-1", encodedCapability, "reset", map[string]any{"reset_id": "reset_bad_words", "lesson_id": 101})
	shutdown := firstAudioRequestLine(t, "shutdown-1", encodedCapability, "shutdown", map[string]any{})
	var output bytes.Buffer
	err := control.serve(bytes.NewReader(bytes.Join([][]byte{bind, reset, shutdown}, nil)), &output)
	if !errors.Is(err, errFirstAudioRunFailed) {
		t.Fatalf("changed reset word IDs did not make the run terminal: %v", err)
	}
	responses := decodeFirstAudioTestResponses(t, output.Bytes())
	if len(responses) != 3 || responses[1].Status != "outcome_uncertain" || responses[1].Error == nil || responses[1].Error.Code != "reset_outcome_uncertain" {
		t.Fatalf("changed reset word IDs were not rejected: %#v", responses)
	}
	if control.state != firstAudioShutdown || backend.closeCalls != 1 {
		t.Fatalf("changed reset word IDs did not close terminally: state=%s closes=%d", control.state, backend.closeCalls)
	}
}

func TestFirstAudioMatrixResetRejectsAliasedInPlaceWordIDMutation(t *testing.T) {
	capability := firstAudioTestCapability()
	encodedCapability := base64.RawURLEncoding.EncodeToString(capability)
	backend := &firstAudioMutatingWordSetBackend{bound: firstAudioBound{
		Origin: "http://127.0.0.1:12345", SourceBuildIdentity: "unit-test-only", StudentID: 11, BookID: 12,
		WordIDs: []int64{13, 14}, LessonID: 101, Generation: 1,
	}}
	control := newFirstAudioControl(capability, func() (firstAudioBackend, error) { return backend, nil })
	bind := firstAudioRequestLine(t, "bind-alias", encodedCapability, "bind", map[string]any{"fixture": firstAudioFixture})
	reset := firstAudioRequestLine(t, "reset-alias", encodedCapability, "reset", map[string]any{"reset_id": "reset_alias", "lesson_id": 101})
	shutdown := firstAudioRequestLine(t, "shutdown-alias", encodedCapability, "shutdown", map[string]any{})
	var output bytes.Buffer
	err := control.serve(bytes.NewReader(bytes.Join([][]byte{bind, reset, shutdown}, nil)), &output)
	if !errors.Is(err, errFirstAudioRunFailed) {
		t.Fatalf("in-place aliased word ID mutation did not make the run terminal: %v", err)
	}
	responses := decodeFirstAudioTestResponses(t, output.Bytes())
	if len(responses) != 3 || responses[1].Status != "outcome_uncertain" || responses[1].Error == nil || responses[1].Error.Code != "reset_outcome_uncertain" {
		t.Fatalf("in-place aliased word ID mutation was not rejected: %#v", responses)
	}
	if control.state != firstAudioShutdown || backend.closeCalls != 1 {
		t.Fatalf("aliased word ID mutation did not close terminally: state=%s closes=%d", control.state, backend.closeCalls)
	}
}

func TestFirstAudioMatrixBindFailsClosedWithoutIsolationBackend(t *testing.T) {
	capability := firstAudioTestCapability()
	encodedCapability := base64.RawURLEncoding.EncodeToString(capability)
	control := newFirstAudioControl(capability, unavailableFirstAudioBackend)
	bind := firstAudioRequestLine(t, "bind-1", encodedCapability, "bind", map[string]any{"fixture": firstAudioFixture})
	metadata := firstAudioRequestLine(t, "metadata-1", encodedCapability, "metadata", map[string]any{})
	shutdown := firstAudioRequestLine(t, "shutdown-1", encodedCapability, "shutdown", map[string]any{})
	var output bytes.Buffer
	err := control.serve(bytes.NewReader(bytes.Join([][]byte{bind, metadata, shutdown}, nil)), &output)
	if !errors.Is(err, errFirstAudioRunFailed) {
		t.Fatalf("fail-closed bind path did not fail the run: %v", err)
	}
	responses := decodeFirstAudioTestResponses(t, output.Bytes())
	if len(responses) != 3 || responses[0].Status != "failed" || responses[0].Error == nil || responses[0].Error.Code != "bind_setup_failed" {
		t.Fatalf("missing explicit bind failure response: %#v", responses)
	}
	if responses[1].Status != "ok" || responses[1].Data.(map[string]any)["lifecycle_state"] != string(firstAudioTerminalFailed) {
		t.Fatalf("terminal metadata was not retained safely: %#v", responses[1])
	}
	if responses[2].Status != "failed" || responses[2].Error == nil || responses[2].Error.Code != "bind_setup_failed" {
		t.Fatalf("terminal shutdown code mismatch: %#v", responses[2])
	}
}

func TestFirstAudioMatrixDuplicateRequestIDAndCapabilityAreFatal(t *testing.T) {
	capability := firstAudioTestCapability()
	encodedCapability := base64.RawURLEncoding.EncodeToString(capability)
	t.Run("changed request ID reuse", func(t *testing.T) {
		control := newFirstAudioControl(capability, nil)
		first := firstAudioRequestLine(t, "same-id", encodedCapability, "metadata", map[string]any{})
		second := firstAudioRequestLine(t, "same-id", encodedCapability, "shutdown", map[string]any{})
		var output bytes.Buffer
		err := control.serve(bytes.NewReader(bytes.Join([][]byte{first, second}, nil)), &output)
		if !errors.Is(err, errFirstAudioFatal) || len(decodeFirstAudioTestResponses(t, output.Bytes())) != 1 {
			t.Fatalf("duplicate request ID was not fatal after one response: err=%v output=%q", err, output.String())
		}
	})
	t.Run("wrong capability", func(t *testing.T) {
		control := newFirstAudioControl(capability, nil)
		wrong := base64.RawURLEncoding.EncodeToString([]byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))
		line := firstAudioRequestLine(t, "metadata-1", wrong, "metadata", map[string]any{})
		var output bytes.Buffer
		if err := control.serve(bytes.NewReader(line), &output); !errors.Is(err, errFirstAudioFatal) || output.Len() != 0 {
			t.Fatalf("wrong capability was not a fatal no-response error: err=%v output=%q", err, output.String())
		}
	})
	t.Run("bind request ID cannot replay", func(t *testing.T) {
		backend := &firstAudioFakeBackend{bound: firstAudioBound{
			Origin: "http://127.0.0.1:12345", SourceBuildIdentity: "unit-test-only",
			StudentID: 21, BookID: 22, WordIDs: []int64{23}, LessonID: 201, Generation: 4,
		}}
		control := newFirstAudioControl(capability, func() (firstAudioBackend, error) { return backend, nil })
		line := firstAudioRequestLine(t, "bind-once", encodedCapability, "bind", map[string]any{"fixture": firstAudioFixture})
		var output bytes.Buffer
		err := control.serve(bytes.NewReader(bytes.Join([][]byte{line, line}, nil)), &output)
		if !errors.Is(err, errFirstAudioFatal) || len(decodeFirstAudioTestResponses(t, output.Bytes())) != 1 || backend.bindCalls != 1 {
			t.Fatalf("duplicate bind request was not fatal and single-execution: err=%v calls=%d output=%q", err, backend.bindCalls, output.String())
		}
	})
}

func TestFirstAudioMatrixResetFailureIsRecordedAndTerminal(t *testing.T) {
	capability := firstAudioTestCapability()
	encodedCapability := base64.RawURLEncoding.EncodeToString(capability)
	backend := &firstAudioFakeBackend{
		bound:       firstAudioBound{Origin: "http://127.0.0.1:12345", SourceBuildIdentity: "unit-test-only", StudentID: 21, BookID: 22, WordIDs: []int64{23, 24}, LessonID: 201, Generation: 4},
		resetResult: firstAudioBound{},
		resetErr:    errFirstAudioOutcomeUncertain,
	}
	control := newFirstAudioControl(capability, func() (firstAudioBackend, error) { return backend, nil })
	control.backend = backend
	control.bound = backend.bound
	control.state = firstAudioStateBound
	reset := firstAudioRequestLine(t, "reset-1", encodedCapability, "reset", map[string]any{"reset_id": "reset_uncertain", "lesson_id": 201})
	replay := firstAudioRequestLine(t, "reset-2", encodedCapability, "reset", map[string]any{"reset_id": "reset_uncertain", "lesson_id": 201})
	shutdown := firstAudioRequestLine(t, "shutdown-1", encodedCapability, "shutdown", map[string]any{})
	var output bytes.Buffer
	err := control.serve(bytes.NewReader(bytes.Join([][]byte{reset, replay, shutdown}, nil)), &output)
	if !errors.Is(err, errFirstAudioRunFailed) {
		t.Fatalf("uncertain reset should fail the run: %v", err)
	}
	responses := decodeFirstAudioTestResponses(t, output.Bytes())
	if len(responses) != 3 || responses[0].Status != "outcome_uncertain" || responses[1].Status != "outcome_uncertain" || responses[1].RequestID != "reset-2" || responses[2].Error == nil || responses[2].Error.Code != "reset_outcome_uncertain" {
		t.Fatalf("uncertain result was not replayed and retained: %#v", responses)
	}
	if backend.resetCalls != 1 {
		t.Fatalf("uncertain replay executed reset again: %d calls", backend.resetCalls)
	}
}

func firstAudioResultResponse(requestID string, result firstAudioResult) firstAudioResponse {
	var data any
	var wireError *firstAudioError
	if result.status == "ok" {
		data = result.data
	} else {
		wireError = &firstAudioError{Code: result.code}
	}
	return firstAudioResponse{RequestID: requestID, Status: result.status, Data: data, Error: wireError}
}

func marshalFirstAudioResponse(response firstAudioResponse) ([]byte, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	if len(encoded) > firstAudioMaxLine {
		return nil, errors.New("response exceeds 64 KiB")
	}
	return append(encoded, '\n'), nil
}

func writeFirstAudioResponse(output io.Writer, encoded []byte) error {
	for len(encoded) > 0 {
		written, err := output.Write(encoded)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		encoded = encoded[written:]
	}
	return nil
}

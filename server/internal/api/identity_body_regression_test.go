package api

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/obs"
	"github.com/stretchr/testify/require"
)

type identityDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines                []time.Time
	initialError, resetError error
}

func (w *identityDeadlineRecorder) SetReadDeadline(d time.Time) error {
	w.deadlines = append(w.deadlines, d)
	if d.IsZero() {
		return w.resetError
	}
	return w.initialError
}

func TestIdentityLegalUnicodeAndDeadlineRestoration(t *testing.T) {
	app := newBareApp()
	writer := &identityDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest("POST", "/api/v1/bootstrap", strings.NewReader(`{"username":"家长","password":"valid-password","nickname":"爸爸"}`))
	app.Router().ServeHTTP(writer, req)
	require.Equal(t, 200, writer.Code)
	require.Contains(t, writer.Body.String(), `"code":0`)
	require.Len(t, writer.deadlines, 2)
	require.False(t, writer.deadlines[0].IsZero())
	require.True(t, writer.deadlines[1].IsZero())
	server := httptest.NewServer(app.Router())
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	reader := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		body := `{"username":"家长","password":"valid-password"}`
		_, err = fmt.Fprintf(conn, "POST /api/v1/auth/login HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
		require.NoError(t, err)
		response, err := http.ReadResponse(reader, nil)
		require.NoError(t, err)
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		require.NoError(t, err)
		require.Equal(t, 200, response.StatusCode)
		require.Contains(t, string(raw), `"code":0`)
		require.False(t, response.Close)
	}
}

func TestIdentityInitializedBootstrapHasPriorityWithoutReadingBody(t *testing.T) {
	app, _ := newTestApp(t)
	server := httptest.NewServer(app.Router())
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	_, err = fmt.Fprint(conn, "POST /api/v1/bootstrap HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 20000\r\n\r\n")
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	require.Contains(t, string(raw), `"code":403`)
}

func TestIdentityRealTransportDeadlineErrorsFailClosed(t *testing.T) {
	for _, reset := range []bool{false, true} {
		app := newBareApp()
		writer := &identityDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		if reset {
			writer.resetError = http.ErrNotSupported
		} else {
			writer.initialError = http.ErrNotSupported
		}
		req := httptest.NewRequest("POST", "/api/v1/bootstrap", strings.NewReader(`{"username":"safe","password":"password"}`))
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{}))
		app.Router().ServeHTTP(writer, req)
		require.Equal(t, 503, writer.Code)
		require.False(t, app.Auth.HasAdmin())
	}
}

type identityLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *identityLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *identityLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestIdentityBodySizeAndMalformedFailClosed(t *testing.T) {
	for _, route := range []string{"/auth/login", "/bootstrap"} {
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunked=%v", route, chunked), func(t *testing.T) {
				app := newBareApp()
				log := &identityLog{}
				app.SetLogger(obs.New(log))
				server := httptest.NewServer(app.Router())
				defer server.Close()
				body := `{"username":"sensitive-user","password":"sensitive-password","nickname":"用户"}` + strings.Repeat(" ", 16<<10)
				req, err := http.NewRequest("POST", server.URL+"/api/v1"+route, strings.NewReader(body))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				if chunked {
					req.ContentLength = -1
					req.TransferEncoding = []string{"chunked"}
				}
				response, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				require.Equal(t, 413, response.StatusCode)
				require.False(t, app.Auth.HasAdmin())
				require.Contains(t, log.String(), "identity_body_rejected")
				require.NotContains(t, log.String(), "sensitive-user")
				require.NotContains(t, log.String(), "sensitive-password")
			})
		}
	}
	for _, body := range []string{`null`, `{"username":"sensitive-user",`, `{"username":"sensitive-user"} {}`} {
		app := newBareApp()
		log := &identityLog{}
		app.SetLogger(obs.New(log))
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		app.Router().ServeHTTP(w, req)
		require.Equal(t, 400, w.Code)
		require.NotContains(t, log.String(), "login_failed")
		require.NotContains(t, log.String(), "sensitive-user")
	}
}

func TestIdentitySlowTCPBodyDeadlineAndOtherUser(t *testing.T) {
	for _, route := range []string{"/auth/login", "/bootstrap"} {
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunked=%v", route, chunked), func(t *testing.T) {
				t.Parallel()
				app := newBareApp()
				_, err := app.Auth.CreateUser("other", "password", "另一用户", auth.RoleStudent)
				require.NoError(t, err)
				server := httptest.NewServer(app.Router())
				defer server.Close()
				conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
				require.NoError(t, err)
				defer conn.Close()
				require.NoError(t, conn.SetDeadline(time.Now().Add(12*time.Second)))
				framing := "Content-Length: 1000\r\n"
				payload := `{"password":"`
				if chunked {
					framing = "Transfer-Encoding: chunked\r\n"
					payload = "100\r\n" + payload
				}
				started := time.Now()
				_, err = fmt.Fprintf(conn, "POST /api/v1%s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\n%s\r\n%s", route, framing, payload)
				require.NoError(t, err)
				client := &http.Client{Timeout: time.Second}
				response, err := client.Post(server.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"username":"other","password":"password"}`))
				require.NoError(t, err)
				raw, err := io.ReadAll(response.Body)
				response.Body.Close()
				require.NoError(t, err)
				require.Contains(t, string(raw), `"code":0`)
				reply, err := http.ReadResponse(bufio.NewReader(conn), nil)
				require.NoError(t, err, "slow identity body must return HTTP408 without finishing upload")
				defer reply.Body.Close()
				require.Equal(t, 408, reply.StatusCode)
				require.GreaterOrEqual(t, time.Since(started), 9*time.Second)
				require.Less(t, time.Since(started), 12*time.Second)
				require.False(t, app.Auth.HasAdmin())
			})
		}
	}
}

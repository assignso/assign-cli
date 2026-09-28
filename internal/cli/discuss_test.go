package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscussStartsAuthenticatedLoopbackBrokerWithoutPassingBearer(t *testing.T) {
	workspaceID := "018f0d15-7b42-7cc1-8f3e-0c1122334455"
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer cli-access" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/dev/workspace":
			_, _ = io.WriteString(response, `{"id":"`+workspaceID+`","name":"Assign","slug":"assign","role":"owner"}`)
		case "/api/v1/workspaces/" + workspaceID + "/discuss/messages":
			_, _ = io.WriteString(response, `{"stream_id":"stream","messages":[],"next_before":null,"unread_count":0,"last_sequence":0,"last_read_sequence":0}`)
		default:
			t.Fatalf("unexpected upstream path %s", request.URL.Path)
		}
	}))
	defer server.Close()

	binary := filepath.Join(t.TempDir(), "assign-discuss")
	if err := os.WriteFile(binary, []byte("prepared fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASSIGN_DISCUSS_BINARY", binary)
	t.Setenv("ASSIGN_TOKEN", "")
	store := &memoryCredentialStore{value: storedCredential{AccessToken: "cli-access", RefreshToken: "cli-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
	var receivedEnvironment []string
	opts := &options{
		host: server.URL, credentials: store, isTerminal: func(io.Reader, io.Writer) bool { return true },
		runDiscuss: func(_ context.Context, gotBinary string, environment []string, _ io.Reader, _, _ io.Writer) error {
			if gotBinary != binary {
				t.Fatalf("binary = %q", gotBinary)
			}
			receivedEnvironment = append([]string(nil), environment...)
			values := environmentValues(environment)
			request, err := http.NewRequest(http.MethodGet, values["ASSIGN_DISCUSS_BROKER"]+"/api/v1/workspaces/"+workspaceID+"/discuss/messages?limit=100", nil)
			if err != nil {
				return err
			}
			request.Header.Set(discussSessionHeader, values["ASSIGN_DISCUSS_SESSION"])
			result, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer result.Body.Close()
			if result.StatusCode != http.StatusOK {
				t.Fatalf("broker status = %d", result.StatusCode)
			}
			return nil
		},
	}
	if err := runDiscuss(context.Background(), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, opts, server.Client()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(receivedEnvironment, "\n")
	if strings.Contains(joined, "cli-access") || !strings.Contains(joined, "ASSIGN_DISCUSS_WORKSPACE_ID="+workspaceID) {
		t.Fatalf("sidecar environment = %q", joined)
	}
}

func TestDiscussBrokerRejectsMissingSessionAndUnscopedRoutes(t *testing.T) {
	broker := &discussBroker{host: "https://api.assign.so", workspaceID: "workspace", secret: "session", client: http.DefaultClient, credentials: &memoryCredentialStore{}}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/workspace/discuss/messages", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/workspace/tasks", nil),
	} {
		if strings.Contains(request.URL.Path, "/tasks") {
			request.Header.Set(discussSessionHeader, "session")
		}
		recorder := httptest.NewRecorder()
		broker.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusNotFound {
			t.Fatalf("status for %s = %d", request.URL.Path, recorder.Code)
		}
	}
}

func TestDiscussBrokerAllowlistCoversQueuedTurnsStopAndDecisions(t *testing.T) {
	prefix := "/api/v1/workspaces/workspace/discuss"
	for _, path := range []string{
		prefix + "/turns",
		prefix + "/messages/message/cancel",
		prefix + "/interactions/interaction/decision",
	} {
		if !allowedDiscussBrokerRequest(http.MethodPost, path, prefix) {
			t.Fatalf("expected terminal Discuss route %s to be allowed", path)
		}
	}
	if allowedDiscussBrokerRequest(http.MethodPost, prefix+"/messages/message/retry", prefix) {
		t.Fatal("unexpected Discuss route was allowed")
	}
}

func TestDiscussBrokerFlushesEventStreamWithoutWholeResponseLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		flusher := response.(http.Flusher)
		_, _ = io.WriteString(response, "event: hello\ndata: {}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()
	broker := &discussBroker{host: upstream.URL, workspaceID: "workspace", secret: "session", client: upstream.Client(), credentials: &memoryCredentialStore{value: storedCredential{AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour)}}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/workspace/discuss/events/stream?envelope_version=1", nil)
	request.Header.Set(discussSessionHeader, "session")
	recorder := httptest.NewRecorder()
	broker.ServeHTTP(recorder, request)
	if recorder.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(recorder.Body.String(), "event: hello") {
		t.Fatalf("stream response = %q %#v", recorder.Body.String(), recorder.Header())
	}
}

func TestDiscussChildEnvironmentStripsCredentialsAndStaleSessionValues(t *testing.T) {
	got := environmentValues(discussChildEnvironment(
		[]string{"PATH=/bin", "ASSIGN_TOKEN=apt_secret", "ASSIGN_DISCUSS_SESSION=stale", "ASSIGN_DISCUSS_BROKER=http://evil.invalid"},
		[]string{"ASSIGN_DISCUSS_SESSION=fresh", "ASSIGN_DISCUSS_BROKER=http://127.0.0.1:1234"},
	))
	if _, exists := got["ASSIGN_TOKEN"]; exists {
		t.Fatal("personal API token reached the Discuss child environment")
	}
	if got["ASSIGN_DISCUSS_SESSION"] != "fresh" || got["ASSIGN_DISCUSS_BROKER"] != "http://127.0.0.1:1234" {
		t.Fatalf("child environment = %#v", got)
	}
}

func environmentValues(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			values[name] = value
		}
	}
	return values
}

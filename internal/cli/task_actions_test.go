package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	taskActionFixtureHost      = "https://api.example.invalid"
	taskActionFixtureTask      = "10000000-0000-4000-8000-000000000011"
	taskActionFixtureWorkspace = "10000000-0000-4000-8000-000000000012"
	taskActionFixtureProject   = "10000000-0000-4000-8000-000000000013"
	taskActionFixtureStatus    = "10000000-0000-4000-8000-000000000014"
	taskActionFixtureETag      = `"a1.s1.item.prepared-task"`
	taskActionFixtureKey       = "prepared-task-action-key"
)

func taskActionFixture(code, revision string) map[string]any {
	return map[string]any{"id": taskActionFixtureTask, "workspace_id": taskActionFixtureWorkspace, "project_id": taskActionFixtureProject, "status_id": taskActionFixtureStatus, "code": code, "title": "Ship now", "revision": revision, "task_number": "123"}
}

// Use the standard HTTP/1 transport over an in-memory connection. The action
// still exercises request framing and transport policy without a listener.
func taskActionFixtureClient(t *testing.T, handler http.Handler) *http.Client {
	t.Helper()
	return &http.Client{Transport: &http.Transport{
		DialTLSContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				request, err := http.ReadRequest(bufio.NewReader(server))
				if err != nil {
					t.Errorf("read prepared action request: %v", err)
					return
				}
				defer request.Body.Close()
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				response := recorder.Result()
				defer response.Body.Close()
				response.Close = true
				_ = response.Write(server)
			}()
			return client, nil
		},
	}}
}

// Shared canonical fixture: real CLI parsing and transport policy,
// // no provider or environment credential store. Its complete Task deliberately
// exceeds the collection/Comment response limit.
func taskActionFixtureHandler(t *testing.T, code, revision string, post http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer prepared-task-token" || r.Header.Get("Accept-Encoding") != "identity" || r.ProtoMajor != 1 || !r.Close {
			t.Errorf("unpinned transport: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			post(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/v1/dev/workspace":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": taskActionFixtureWorkspace, "slug": "prepared"})
		case "/api/v1/workspaces/" + taskActionFixtureWorkspace + "/tasks":
			if r.URL.Query().Get("code") != code {
				t.Errorf("discovery code = %q", r.URL.Query().Get("code"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{taskActionFixture(code, revision)}, "next_cursor": nil, "coverage": "complete"})
		case "/api/v1/tasks/" + taskActionFixtureTask:
			if r.URL.RawQuery != "view=canonical" {
				t.Errorf("observation query = %q", r.URL.RawQuery)
			}
			w.Header().Set("ETag", taskActionFixtureETag)
			item := taskActionFixture(code, revision)
			delete(item, "code")
			item["description"] = strings.Repeat("x", 300000)
			_ = json.NewEncoder(w).Encode(item)
		default:
			t.Errorf("unexpected read %s", r.URL)
			w.WriteHeader(404)
		}
	}
}

func taskActionFixtureResult(w http.ResponseWriter, replay, reassigned bool) {
	item := taskActionFixture("", "8")
	delete(item, "code")
	result := map[string]any{"task": item, "reassigned": reassigned}
	if !replay {
		w.Header().Set("ETag", `"a1.s1.projection.prepared-receipt"`)
		result["item_validator"] = map[string]any{"etag": `"a1.s1.item.prepared-after"`, "encoding": "identity", "representation": "canonical-item"}
	}
	_ = json.NewEncoder(w).Encode(result)
}

func TestCanonicalTaskActionsUseObservedCondition(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	for _, action := range []string{"start", "done", "reopen"} {
		t.Run(action, func(t *testing.T) {
			posts := 0
			client := taskActionFixtureClient(t, taskActionFixtureHandler(t, "PRO-123", "7", func(w http.ResponseWriter, r *http.Request) {
				posts++
				canonical := action
				if canonical == "done" {
					canonical = "complete"
				}
				if r.URL.Path != "/api/v1/tasks/"+taskActionFixtureTask+"/"+canonical || r.Header.Get("If-Match") != taskActionFixtureETag || r.Header.Get("Idempotency-Key") != taskActionFixtureKey || r.ContentLength != 0 {
					t.Errorf("incorrect action: %s headers=%v", r.URL, r.Header)
				}
				taskActionFixtureResult(w, false, action == "start")
			}))
			var out, diagnostic bytes.Buffer
			command := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostic, client)
			command.SetArgs([]string{"--host", taskActionFixtureHost, "task", action, "pro-123", "--revision", "7", "--idempotency-key", taskActionFixtureKey})
			if err := command.ExecuteContext(context.Background()); err != nil || posts != 1 || out.String() != "PRO-123  Ship now\n" || !strings.Contains(diagnostic.String(), "--if-match '"+taskActionFixtureETag+"'") {
				t.Fatalf("posts=%d err=%v out=%q diagnostic=%q", posts, err, out.String(), diagnostic.String())
			}
			if warned := strings.Contains(diagnostic.String(), "Task was reassigned to you"); warned != (action == "start") {
				t.Fatalf("unexpected reassignment diagnostic for %s: %q", action, diagnostic.String())
			}
		})
	}
}

func TestCanonicalTaskActionRecoverySkipsDiscoveryAndAcceptsHistoricalReceipt(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	for commandName, canonical := range map[string]string{"start": "start", "done": "complete"} {
		t.Run(commandName, func(t *testing.T) {
			requests := 0
			client := taskActionFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tasks/"+taskActionFixtureTask+"/"+canonical || r.Header.Get("If-Match") != taskActionFixtureETag || r.Header.Get("Idempotency-Key") != taskActionFixtureKey || r.Header.Get("Authorization") != "Bearer prepared-task-token" {
					t.Errorf("recovery rediscovered/replaced original command: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				taskActionFixtureResult(w, true, false)
			}))
			var out, diagnostic bytes.Buffer
			command := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostic, client)
			command.SetArgs([]string{"--host", taskActionFixtureHost, commandName, "PRO-123", "--task-id", taskActionFixtureTask, "--if-match", taskActionFixtureETag, "--idempotency-key", taskActionFixtureKey})
			if err := command.ExecuteContext(context.Background()); err != nil || requests != 1 || out.String() != "PRO-123  Ship now\n" {
				t.Fatalf("requests=%d err=%v out=%q", requests, err, out.String())
			}
		})
	}
}

func TestCanonicalTaskActionStaleObservationNeverWrites(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	client := taskActionFixtureClient(t, taskActionFixtureHandler(t, "PRO-123", "8", func(w http.ResponseWriter, r *http.Request) {
		t.Error("stale revision sent a write")
		w.WriteHeader(500)
	}))
	var out, diagnostic bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostic, client)
	command.SetArgs([]string{"--host", taskActionFixtureHost, "done", "PRO-123", "--revision", "7"})
	if err := command.ExecuteContext(context.Background()); exitCode(err) != ExitConflict || out.Len() != 0 || diagnostic.Len() != 0 {
		t.Fatalf("err=%v out=%q diagnostic=%q", err, out.String(), diagnostic.String())
	}
}

func TestCanonicalTaskActionUncertainOutcomeDoesNotRetry(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	requests := 0
	client := taskActionFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(503)
	}))
	var out, diagnostic bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostic, client)
	command.SetArgs([]string{"--host", taskActionFixtureHost, "reopen", "PRO-123", "--task-id", taskActionFixtureTask, "--if-match", taskActionFixtureETag, "--idempotency-key", taskActionFixtureKey})
	err := command.ExecuteContext(context.Background())
	if requests != 1 || exitCode(err) != ExitOperation || !strings.Contains(fmt.Sprint(err), "outcome is uncertain") || !strings.Contains(diagnostic.String(), taskActionFixtureKey) || out.Len() != 0 {
		t.Fatalf("requests=%d err=%v diagnostic=%q", requests, err, diagnostic.String())
	}
}

func TestExactTaskLookupRejectsUnusableResponses(t *testing.T) {
	match := taskActionFixture("PRO-123", "7")
	for _, test := range []struct {
		name   string
		page   map[string]any
		exit   int
		detail string
	}{
		{"multiple-matches", map[string]any{"data": []any{match, match}, "next_cursor": nil}, ExitOperation, "ambiguous"},
		{"match-with-continuation", map[string]any{"data": []any{match}, "next_cursor": "opaque"}, ExitOperation, "ambiguous"},
		{"wrong-code", map[string]any{"data": []any{taskActionFixture("PRO-124", "7")}, "next_cursor": nil}, ExitOperation, "identity"},
		{"missing-continuation-field", map[string]any{"data": []any{match}}, ExitOperation, "incomplete"},
		{"partial-empty", map[string]any{"data": []any{}, "next_cursor": nil, "coverage": "partial"}, ExitOperation, "absence is unknown"},
		{"authorized-empty", map[string]any{"data": []any{}, "next_cursor": nil}, ExitContext, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &operationTransport{host: "https://api.example.invalid", token: "prepared-task-token"}
			calls := 0
			transport.client.Transport = operationFixtureRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/workspaces/"+taskActionFixtureWorkspace+"/tasks" ||
					r.URL.Query().Get("code") != "PRO-123" || r.URL.Query().Get("limit") != "1" {
					t.Errorf("unexpected exact lookup %s %s", r.Method, r.URL)
				}
				w := httptest.NewRecorder()
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(test.page); err != nil {
					t.Fatal(err)
				}
				return w.Result(), nil
			})
			item, err := resolveResourceTask(context.Background(), transport, taskActionFixtureWorkspace, "PRO-123")
			if exitCode(err) != test.exit || !strings.Contains(err.Error(), test.detail) || item.ID != "" || calls != 1 {
				t.Fatalf("id=%q calls=%d err=%v", item.ID, calls, err)
			}
		})
	}
}

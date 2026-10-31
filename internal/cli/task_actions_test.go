package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
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

// Shared canonical fixture: real CLI parsing/transport, prepared TLS server,
// no provider or environment credential store. Its complete Task deliberately
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
			item["description"] = strings.Repeat("x", 300000)
			_ = json.NewEncoder(w).Encode(item)
		default:
			t.Errorf("unexpected read %s", r.URL)
			w.WriteHeader(404)
		}
	}
}

func taskActionFixtureResult(w http.ResponseWriter, code string, replay bool) {
	result := map[string]any{"task": taskActionFixture(code, "8"), "reassigned": false}
	if !replay {
		w.Header().Set("ETag", `"a1.s1.projection.prepared-receipt"`)
		result["item_validator"] = map[string]any{"etag": `"a1.s1.item.prepared-after"`, "encoding": "identity", "representation": "canonical-item"}
	}
	_ = json.NewEncoder(w).Encode(result)
}

func TestCanonicalTaskActionsUseObservedCondition(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	for _, action := range []string{"done", "reopen"} {
		t.Run(action, func(t *testing.T) {
			posts := 0
			server := httptest.NewTLSServer(taskActionFixtureHandler(t, "PRO-123", "7", func(w http.ResponseWriter, r *http.Request) {
				posts++
				canonical := action
				if canonical == "done" {
					canonical = "complete"
				}
				if r.URL.Path != "/api/v1/tasks/"+taskActionFixtureTask+"/"+canonical || r.Header.Get("If-Match") != taskActionFixtureETag || r.Header.Get("Idempotency-Key") != taskActionFixtureKey || r.ContentLength != 0 {
					t.Errorf("incorrect action: %s headers=%v", r.URL, r.Header)
				}
				taskActionFixtureResult(w, "PRO-123", false)
			}))
			defer server.Close()
			var out, diagnostic bytes.Buffer
			command := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostic, server.Client())
			command.SetArgs([]string{"--host", server.URL, "task", action, "pro-123", "--revision", "7", "--idempotency-key", taskActionFixtureKey})
			if err := command.ExecuteContext(context.Background()); err != nil || posts != 1 || out.String() != "PRO-123  Ship now\n" || !strings.Contains(diagnostic.String(), "--if-match '"+taskActionFixtureETag+"'") {
				t.Fatalf("posts=%d err=%v out=%q diagnostic=%q", posts, err, out.String(), diagnostic.String())
			}
		})
	}
}

func TestCanonicalTaskActionRecoverySkipsDiscoveryAndAcceptsHistoricalReceipt(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tasks/"+taskActionFixtureTask+"/complete" || r.Header.Get("If-Match") != taskActionFixtureETag || r.Header.Get("Idempotency-Key") != taskActionFixtureKey {
			t.Errorf("recovery rediscovered/replaced original command: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		taskActionFixtureResult(w, "MOVED-2", true)
	}))
	defer server.Close()
	var out, diagnostic bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostic, server.Client())
	command.SetArgs([]string{"--host", server.URL, "done", "PRO-123", "--task-id", taskActionFixtureTask, "--if-match", taskActionFixtureETag, "--idempotency-key", taskActionFixtureKey})
	if err := command.ExecuteContext(context.Background()); err != nil || requests != 1 || out.String() != "MOVED-2  Ship now\n" {
		t.Fatalf("requests=%d err=%v out=%q", requests, err, out.String())
	}
}

func TestCanonicalTaskActionStaleObservationNeverWrites(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	server := httptest.NewTLSServer(taskActionFixtureHandler(t, "PRO-123", "8", func(w http.ResponseWriter, r *http.Request) {
		t.Error("stale revision sent a write")
		w.WriteHeader(500)
	}))
	defer server.Close()
	var out, diagnostic bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostic, server.Client())
	command.SetArgs([]string{"--host", server.URL, "done", "PRO-123", "--revision", "7"})
	if err := command.ExecuteContext(context.Background()); exitCode(err) != ExitConflict || out.Len() != 0 || diagnostic.Len() != 0 {
		t.Fatalf("err=%v out=%q diagnostic=%q", err, out.String(), diagnostic.String())
	}
}

func TestCanonicalTaskActionUncertainOutcomeDoesNotRetry(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(503)
	}))
	defer server.Close()
	var out, diagnostic bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostic, server.Client())
	command.SetArgs([]string{"--host", server.URL, "reopen", "PRO-123", "--task-id", taskActionFixtureTask, "--if-match", taskActionFixtureETag, "--idempotency-key", taskActionFixtureKey})
	err := command.ExecuteContext(context.Background())
	if requests != 1 || exitCode(err) != ExitOperation || !strings.Contains(fmt.Sprint(err), "outcome is uncertain") || !strings.Contains(diagnostic.String(), taskActionFixtureKey) || out.Len() != 0 {
		t.Fatalf("requests=%d err=%v diagnostic=%q", requests, err, diagnostic.String())
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func executeTaskFixture(t *testing.T, server *httptest.Server, input string, args ...string) (string, string, error) {
	t.Helper()
	var output, diagnostics bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(input), &output, &diagnostics, server.Client())
	command.SetArgs(append([]string{"--host", server.URL}, args...))
	err := command.ExecuteContext(context.Background())
	return output.String(), diagnostics.String(), err
}

func TestTaskContextProfilesAndSafeStreams(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/cli/tasks/PRO-1/context" || r.Header.Get("Authorization") != "Bearer apt_fixture" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"task_code": "PRO-1", "profile": r.URL.Query().Get("profile"), "markdown": "# PRO-1\n\nUntrusted content\n\x1b[31mtext\n", "truncated": true, "omissions": []string{"unsupported"}})
	}))
	defer server.Close()
	for _, flag := range []string{"--compact", "--full"} {
		out, diagnostics, err := executeTaskFixture(t, server, "", "task", "context", "pro-1", flag)
		if err != nil || strings.Contains(out, "\x1b") || !strings.Contains(out, "Untrusted content") || strings.Contains(out, "assign:") || !strings.Contains(diagnostics, "truncated") || !strings.Contains(diagnostics, "unsupported") {
			t.Fatalf("out=%q diagnostics=%q err=%v", out, diagnostics, err)
		}
	}
	_, _, err := executeTaskFixture(t, server, "", "task", "context", "PRO-1", "--compact", "--full")
	if err == nil || calls != 2 {
		t.Fatalf("exclusive profile err=%v calls=%d", err, calls)
	}
}

func TestTaskContextRejectsWrongIdentityAndOversizedPacket(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	for _, packet := range []map[string]any{
		{"task_code": "OTHER-1", "profile": "compact", "markdown": "wrong"},
		{"task_code": "PRO-1", "profile": "full", "markdown": "wrong profile"},
		{"task_code": "PRO-1", "profile": "compact", "markdown": strings.Repeat("a", 16*1024+1)},
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(packet) }))
		out, _, err := executeTaskFixture(t, server, "", "task", "context", "PRO-1")
		server.Close()
		if err == nil || out != "" {
			t.Fatalf("out=%q err=%v", out, err)
		}
	}
}

func TestTaskCommentPreservesTextAndRetryIdentity(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	key := "comment-fixture-0001"
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/cli/tasks/PRO-1/comments" || r.Header.Get("Idempotency-Key") != key || r.Header.Get("If-Match") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request %v", r)
		}
		var body struct {
			Markdown string `json:"markdown"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Markdown != "Prepared evidence é\nnext\n" {
			t.Errorf("body=%q err=%v", body.Markdown, err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"revision":1}`)
	}))
	defer server.Close()
	for range 2 {
		out, diagnostics, err := executeTaskFixture(t, server, "Prepared evidence é\r\nnext\r\n", "task", "comment", "pro-1", "-", "--idempotency-key", key)
		if err != nil || out != "PRO-1\n" || diagnostics != "" {
			t.Fatalf("out=%q diagnostics=%q err=%v", out, diagnostics, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestTaskWriteInputErrorsMakeNoRequest(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	for _, text := range []string{"", " \n", "a\x00b", "\xff", strings.Repeat("a", maxTaskTextBytes+1), "\x1b[31m"} {
		out, _, err := executeTaskFixture(t, server, text, "task", "comment", "PRO-1", "-")
		if err == nil || exitCode(err) != ExitArguments || out != "" {
			t.Fatalf("input rejected incorrectly: err=%v", err)
		}
	}
	for _, args := range [][]string{
		{"task", "comment", "PRO-1", "valid", "--idempotency-key", "short"},
		{"task", "comment", "PRO-1", "valid", "--idempotency-key", "key-with spaces-0001"},
		{"task", "create", "valid", "--project", "PRO", "--priority", "invalid"},
		{"task", "create", "valid", "--project", "PRO", "--description", ""},
		{"task", "create", strings.Repeat("é", 501), "--project", "PRO"},
		{"task", "create", "title\x1b", "--project", "PRO"},
	} {
		_, _, err := executeTaskFixture(t, server, "", args...)
		if err == nil || exitCode(err) != ExitArguments {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid input made %d requests", calls)
	}
}

func TestTaskTextBoundsBeforeNormalization(t *testing.T) {
	valid := strings.Repeat("é", maxTaskTextBytes/2)
	result, err := taskText("-", strings.NewReader(valid))
	if err != nil || result != valid {
		t.Fatalf("exact limit err=%v", err)
	}
	_, err = taskText("-", strings.NewReader(strings.Repeat("\r\n", maxTaskTextBytes/2+1)))
	if err == nil {
		t.Fatal("raw oversized input accepted")
	}
}

func TestTaskCreateUsesDefaultsAndExplicitProject(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli/projects/PRO/tasks" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s", r.URL)
		}
		if len(r.Header.Get("Idempotency-Key")) < 16 {
			t.Error("missing key")
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["title"] != "Build batch" {
			t.Errorf("body=%v", body)
		}
		if _, exists := body["priority"]; exists {
			t.Error("invented priority default")
		}
		if _, exists := body["description_markdown"]; exists {
			t.Error("invented description")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"code":"PRO-2","title":"Build batch"}`)
	}))
	defer server.Close()
	out, _, err := executeTaskFixture(t, server, "", "task", "create", "Build batch", "--project", "pro", "--field", "code")
	if err != nil || out != "PRO-2\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestProjectTaskPagesExposeOpaqueContinuation(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	requests := 0
	cursor := "opaque/+cursor?=&"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/cli/projects/PRO/tasks" || r.URL.Query().Get("limit") != "1" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]string{"code": "PRO-1", "title": "first"}}, "has_more": true, "next_cursor": cursor})
			return
		}
		if r.URL.Query().Get("cursor") != cursor {
			t.Errorf("cursor corrupted: %q", r.URL.Query().Get("cursor"))
		}
		_, _ = io.WriteString(w, `{"items":[{"code":"PRO-2","title":"second"}],"has_more":false,"next_cursor":null}`)
	}))
	defer server.Close()
	out, _, err := executeTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", "1", "--field", "next_cursor")
	if err != nil || out != cursor+"\n" || requests != 1 {
		t.Fatalf("out=%q err=%v requests=%d", out, err, requests)
	}
	out, diagnostics, err := executeTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", "1", "--cursor", cursor, "--field", "code")
	if err != nil || out != "PRO-2\n" || diagnostics != "" || requests != 2 {
		t.Fatalf("out=%q diagnostics=%q err=%v requests=%d", out, diagnostics, err, requests)
	}
	out, _, err = executeTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", "1", "--cursor", cursor, "--field", "next_cursor")
	if err != nil || out != "\n" {
		t.Fatalf("end cursor=%q err=%v", out, err)
	}
	for _, flag := range []string{"0", "101"} {
		_, _, err := executeTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", flag)
		if err == nil || exitCode(err) != ExitArguments {
			t.Fatalf("limit err=%v", err)
		}
	}
	if requests != 3 {
		t.Fatalf("invalid limit fetched page: %d", requests)
	}
}

func TestProjectTaskInvalidContinuationFailsWithoutOutput(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	for _, body := range []string{`{"items":[],"has_more":true}`, `{"items":[],"has_more":false,"next_cursor":"unexpected"}`, `{"items":[{"code":"PRO-1","title":"ok"},{"code":"PRO-2","title":"extra"}],"has_more":false}`} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		out, _, err := executeTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", "1")
		server.Close()
		if err == nil || out != "" {
			t.Fatalf("out=%q err=%v", out, err)
		}
	}
}

func TestLifecycleIdenticalRetryRetainsKeyAndRevision(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	key := "lifecycle-fixture-001"
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Idempotency-Key") != key || r.Header.Get("If-Match") != `"7"` {
			t.Errorf("headers=%v", r.Header)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"task":{"code":"PRO-1","title":"Review queued"}}`)
	}))
	defer server.Close()
	args := []string{"task", "done", "PRO-1", "--revision", "7", "--idempotency-key", key}
	out, _, err := executeTaskFixture(t, server, "", args...)
	if err == nil || out != "" || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "uncertain") || calls != 1 {
		t.Fatalf("out=%q err=%v calls=%d", out, err, calls)
	}
	out, _, err = executeTaskFixture(t, server, "", args...)
	if err != nil || out != "PRO-1  Review queued\n" || calls != 2 {
		t.Fatalf("out=%q err=%v calls=%d", out, err, calls)
	}
}

func TestGeneratedMutationKeyIsRecoverableOnMalformedResponse(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	var key string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"revision":1} trailing`)
	}))
	defer server.Close()
	out, _, err := executeTaskFixture(t, server, "", "task", "comment", "PRO-1", "fixture")
	if err == nil || out != "" || len(key) < 16 || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "apt_fixture") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestMutationErrorCategories(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	for status, want := range map[int]int{401: ExitAuthentication, 403: ExitAuthentication, 404: ExitContext, 409: ExitConflict, 429: ExitOperation} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			out, _, err := executeTaskFixture(t, server, "", "task", "comment", "PRO-1", "fixture")
			if err == nil || exitCode(err) != want || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}

func TestProjectJSONPageReadsTasksAndCursorInOneRequest(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"items":[{"code":"PRO-1","project_code":"PRO","title":"child","status_category":"todo","revision":7,"url":"https://app.assign.so/app/assign/tasks/PRO-1"}],"next_cursor":"next-page","has_more":true}`)
	}))
	defer server.Close()
	out, diagnostics, err := executeTaskFixture(t, server, "", "project", "tasks", "PRO", "--format", "json")
	var page struct {
		Schema string    `json:"schema"`
		Items  []cliTask `json:"items"`
		Cursor string    `json:"next_cursor"`
		More   bool      `json:"has_more"`
	}
	if err != nil || json.Unmarshal([]byte(out), &page) != nil || diagnostics != "" || calls != 1 || page.Schema != "assign.cli.task-page.v1" || page.Cursor != "next-page" || !page.More || len(page.Items) != 1 || page.Items[0].Revision != 7 {
		t.Fatalf("out=%q diagnostics=%q err=%v calls=%d", out, diagnostics, err, calls)
	}
	for _, args := range [][]string{{"project", "tasks", "PRO", "--format", "json", "--field", "code"}, {"project", "tasks", "PRO", "--format", "yaml"}} {
		_, _, err := executeTaskFixture(t, server, "", args...)
		if err == nil || exitCode(err) != ExitArguments {
			t.Fatalf("format err=%v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid output flags fetched pages: %d", calls)
	}
}

func TestTaskWritesNeverFollowRedirects(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	redirected := 0
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++; w.WriteHeader(http.StatusCreated) }))
	defer destination.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	out, _, err := executeTaskFixture(t, origin, "", "task", "comment", "PRO-1", "fixture")
	if err == nil || out != "" || redirected != 0 {
		t.Fatalf("out=%q err=%v forwarded requests=%d", out, err, redirected)
	}
}

func TestTaskCommentsFirstNextAndFinalPages(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	requests := 0
	cursor := "opaque/+cursor?=&"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/cli/tasks/PRO-1/comments" || r.Header.Get("Authorization") != "Bearer "+os.Getenv("ASSIGN_TOKEN") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		if r.URL.Query().Get("cursor") == "" {
			if r.URL.Query().Get("limit") != "2" {
				t.Errorf("unexpected limit %s", r.URL)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{
				map[string]any{"body_markdown": "First **note**", "actor": "user", "actor_name": "Ada", "revision": 1, "created_at": "2026-09-26T10:00:00Z"},
				map[string]any{"body_markdown": "Second note", "actor": "user:mcp", "actor_name": nil, "revision": 2, "created_at": "2026-09-26T11:00:00Z"},
			}, "has_more": true, "next_cursor": cursor})
			return
		}
		if r.URL.Query().Get("cursor") != cursor {
			t.Errorf("cursor corrupted: %q", r.URL.Query().Get("cursor"))
		}
		_, _ = io.WriteString(w, `{"items":[{"body_markdown":"Final note","actor":"agent","actor_name":"Helper","revision":3,"created_at":"2026-09-26T12:00:00Z"}],"has_more":false,"next_cursor":null}`)
	}))
	defer server.Close()
	out, diagnostics, err := executeTaskFixture(t, server, "", "task", "comments", "pro-1", "--limit", "2")
	want := "## Ada · 2026-09-26T10:00:00Z\n\nFirst **note**\n\n## user:mcp · 2026-09-26T11:00:00Z\n\nSecond note\n"
	if err != nil || out != want || !strings.Contains(diagnostics, "--cursor "+cursor) || strings.Contains(out, "assign:") || requests != 1 {
		t.Fatalf("out=%q diagnostics=%q err=%v requests=%d", out, diagnostics, err, requests)
	}
	out, diagnostics, err = executeTaskFixture(t, server, "", "task", "comments", "PRO-1", "--limit", "2", "--cursor", cursor)
	if err != nil || out != "## Helper · 2026-09-26T12:00:00Z\n\nFinal note\n" || diagnostics != "" || requests != 2 {
		t.Fatalf("out=%q diagnostics=%q err=%v requests=%d", out, diagnostics, err, requests)
	}
}

func TestTaskCommentsEmptyPageAndTombstones(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "50" {
			t.Errorf("default limit not sent: %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"items":[],"has_more":false,"next_cursor":null}`)
	}))
	defer server.Close()
	out, diagnostics, err := executeTaskFixture(t, server, "", "task", "comments", "PRO-1")
	if err != nil || out != "" || diagnostics != "" {
		t.Fatalf("empty page out=%q diagnostics=%q err=%v", out, diagnostics, err)
	}
	tombstones := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"body_markdown":"","actor":"user","actor_name":"Ada","revision":4,"created_at":"2026-09-26T13:00:00Z"}],"has_more":false,"next_cursor":null}`)
	}))
	defer tombstones.Close()
	out, _, err = executeTaskFixture(t, tombstones, "", "task", "comments", "PRO-1")
	if err != nil || out != "## Ada · 2026-09-26T13:00:00Z\n\n[deleted comment]\n" {
		t.Fatalf("tombstone out=%q err=%v", out, err)
	}
}

func TestTaskCommentsInvalidInputMakesNoRequest(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	for _, args := range [][]string{
		{"task", "comments", "PRO-1", "--limit", "0"},
		{"task", "comments", "PRO-1", "--limit", "101"},
		{"task", "comments", "PRO-1", "--cursor", strings.Repeat("c", 2049)},
		{"task", "comments", "PRO-1", "--cursor", "bad\x1bcurse"},
		{"task", "comments", "  "},
	} {
		out, _, err := executeTaskFixture(t, server, "", args...)
		if err == nil || exitCode(err) != ExitArguments || out != "" {
			t.Fatalf("args=%v out=%q err=%v", args, out, err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid input made %d requests", calls)
	}
}

func TestTaskCommentsInvalidContinuationFailsWithoutOutput(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	bodies := []string{
		`{"items":[],"has_more":true}`,
		`{"items":[],"has_more":false,"next_cursor":"unexpected"}`,
		`{"items":[{"body_markdown":"a","actor":"user","actor_name":null,"revision":1,"created_at":"2026-09-26T10:00:00Z"},{"body_markdown":"b","actor":"user","actor_name":null,"revision":2,"created_at":"2026-09-26T11:00:00Z"}],"has_more":false}`,
		`{"items":[{"body_markdown":"a","actor":"","actor_name":null,"revision":1,"created_at":"2026-09-26T10:00:00Z"}],"has_more":false}`,
		`{"items":null,"has_more":false}`,
		`{"items":[],"has_more":true,"next_cursor":"` + strings.Repeat("c", 2049) + `"}`,
	}
	for _, body := range bodies {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		out, _, err := executeTaskFixture(t, server, "", "task", "comments", "PRO-1", "--limit", "1")
		server.Close()
		if err == nil || out != "" {
			t.Fatalf("body=%q out=%q err=%v", body, out, err)
		}
	}
}

func TestTaskCommentsErrorCategories(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	for status, want := range map[int]int{400: ExitOperation, 401: ExitAuthentication, 404: ExitContext, 429: ExitOperation, 500: ExitOperation, 503: ExitOperation} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			out, _, err := executeTaskFixture(t, server, "", "task", "comments", "PRO-1")
			if err == nil || exitCode(err) != want || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}

func TestTaskCommentsOutputSafety(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{
			map[string]any{"body_markdown": "Note\x1b[31m red\n\x00binary", "actor": "user", "actor_name": "Ada\nInjected", "revision": 1, "created_at": "2026-09-26T10:00:00Z"},
		}, "has_more": false, "next_cursor": nil})
	}))
	defer server.Close()
	out, _, err := executeTaskFixture(t, server, "", "task", "comments", "PRO-1")
	if err != nil || strings.Contains(out, "\x1b") || strings.Contains(out, "\x00") || !strings.HasPrefix(out, "## Ada Injected · 2026-09-26T10:00:00Z\n\n") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestTaskCommentsDiscoveryPreservesCreate(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	out, _, err := executeTaskFixture(t, server, "", "task", "--help")
	if err != nil || !strings.Contains(out, "comments") || !strings.Contains(out, "comment") {
		t.Fatalf("task help out=%q err=%v", out, err)
	}
	out, _, err = executeTaskFixture(t, server, "", "task", "comments", "--help")
	if err != nil || !strings.Contains(out, "--limit") || !strings.Contains(out, "--cursor") {
		t.Fatalf("comments help out=%q err=%v", out, err)
	}
}

func TestCreateDescriptionAndPriorityReachCanonicalRequest(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["description_markdown"] != "**Prepared**\né\n" || body["priority"] != "high" {
			t.Errorf("body=%v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"code":"PRO-1","title":"Child"}`)
	}))
	defer server.Close()
	out, _, err := executeTaskFixture(t, server, "**Prepared**\r\né\r\n", "task", "create", "Child", "--project", "PRO", "--description", "-", "--priority", "high")
	if err != nil || out != "PRO-1  Child\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

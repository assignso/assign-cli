package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	canonicalrichtext "github.com/assignso/assign-cli/internal/generated/richtext"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func executeTaskFixture(t *testing.T, server *httptest.Server, input string, args ...string) (string, string, error) {
	t.Helper()
	return executePreparedTaskFixture(t, &preparedHTTPS{URL: server.URL, client: server.Client()}, input, args...)
}

func executePreparedTaskFixture(t *testing.T, server *preparedHTTPS, input string, args ...string) (string, string, error) {
	t.Helper()
	var output, diagnostics bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(input), &output, &diagnostics, server.Client())
	command.SetArgs(append([]string{"--host", server.URL}, args...))
	err := command.ExecuteContext(context.Background())
	return output.String(), diagnostics.String(), err
}

func canonicalTaskContextFixture(t *testing.T, code, token string, packet http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected context flow request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth-context":
			writeWorkspaceBinding(w, taskActionFixtureWorkspace)
		case "/api/v1/workspaces/" + taskActionFixtureWorkspace:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": taskActionFixtureWorkspace, "slug": "prepared", "name": "Prepared"})
		case "/api/v1/workspaces/" + taskActionFixtureWorkspace + "/tasks":
			if r.URL.Query().Get("code") != code {
				t.Errorf("unexpected context Task lookup: %s", r.URL)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{taskActionFixture(code, "1")}, "next_cursor": nil, "coverage": "complete"})
		case "/api/v1/tasks/" + taskActionFixtureTask + "/context":
			if r.URL.Query().Get("format") != "markdown" {
				t.Errorf("unexpected context format: %s", r.URL)
			}
			packet(w, r)
		default:
			t.Errorf("unexpected context route: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestTaskContextProfilesAndSafeStreams(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	calls := 0
	server := newPreparedHTTPS(t, canonicalTaskContextFixture(t, "PRO-1", "apt_fixture", func(w http.ResponseWriter, r *http.Request) {
		calls++
		want := "compact"
		if calls == 2 {
			want = "full"
		}
		if r.URL.Query().Get("profile") != want {
			t.Errorf("unexpected context profile: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"format": "markdown", "task_code": "PRO-1", "profile": want, "markdown": "# PRO-1\n\nUntrusted content\n\x1b[31mtext\n", "truncated": true, "omissions": []string{"unsupported"}})
	}))
	defer server.Close()
	for _, flag := range []string{"--compact", "--full"} {
		out, diagnostics, err := executePreparedTaskFixture(t, server, "", "task", "context", "pro-1", flag)
		if err != nil || strings.Contains(out, "\x1b") || !strings.Contains(out, "Untrusted content") || strings.Contains(out, "assign:") || !strings.Contains(diagnostics, "truncated") || !strings.Contains(diagnostics, "unsupported") {
			t.Fatalf("out=%q diagnostics=%q err=%v", out, diagnostics, err)
		}
	}
	_, _, err := executePreparedTaskFixture(t, server, "", "task", "context", "PRO-1", "--compact", "--full")
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
		calls := 0
		server := newPreparedHTTPS(t, canonicalTaskContextFixture(t, "PRO-1", "apt_fixture", func(w http.ResponseWriter, r *http.Request) {
			calls++
			packet["format"] = "markdown"
			packet["truncated"] = false
			_ = json.NewEncoder(w).Encode(packet)
		}))
		out, _, err := executePreparedTaskFixture(t, server, "", "task", "context", "PRO-1")
		server.Close()
		if calls != 1 || err == nil || !strings.Contains(err.Error(), "Task context response is incomplete or exceeds its profile limit") || out != "" {
			t.Fatalf("out=%q err=%v", out, err)
		}
	}
}

func canonicalCommentFixture(t *testing.T, comments http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	discover := canonicalTaskContextFixture(t, "PRO-1", "apt_fixture", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected context request: %s", r.URL)
		w.WriteHeader(http.StatusNotFound)
	})
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tasks/"+taskActionFixtureTask+"/comments" {
			discover(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", taskActionFixtureETag)
		comments(w, r)
	}
}

func canonicalComment(t *testing.T, markdown, actor string, name *string, revision int64, timestamp string) canonicalCLIComment {
	t.Helper()
	item := canonicalCLIComment{ID: "10000000-0000-4000-8000-000000000015", WorkspaceID: taskActionFixtureWorkspace, TaskID: taskActionFixtureTask, Actor: actor, ActorName: name, Revision: json.Number(fmt.Sprint(revision)), CreatedAt: timestamp}
	if markdown == "" {
		item.DeletedAt = &timestamp
	} else {
		body, err := canonicalrichtext.FromMarkdown([]byte(markdown))
		if err != nil {
			t.Errorf("invalid prepared Markdown: %v", err)
		}
		item.Body = body
	}
	return item
}

func writeCommentPage(w http.ResponseWriter, cursor *string, comments ...canonicalCLIComment) {
	type commentWire struct {
		canonicalCLIComment
		Body json.RawMessage `json:"body,omitempty"`
	}
	items := make([]commentWire, 0, len(comments))
	for _, comment := range comments {
		items = append(items, commentWire{canonicalCLIComment: comment, Body: comment.Body})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": items, "next_cursor": cursor, "has_more": cursor != nil})
}

func canonicalCreateFixture(t *testing.T, write http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth-context":
			writeWorkspaceBinding(w, taskActionFixtureWorkspace)
		case "/api/v1/workspaces/" + taskActionFixtureWorkspace:
			_, _ = fmt.Fprintf(w, `{"id":%q,"name":"Prepared","slug":"prepared"}`, taskActionFixtureWorkspace)
		case "/api/v1/workspaces/" + taskActionFixtureWorkspace + "/projects":
			if r.Method != http.MethodGet || r.URL.Query().Get("code") != "PRO" {
				t.Errorf("unexpected Project lookup: %s %s", r.Method, r.URL)
			}
			_, _ = fmt.Fprintf(w, `{"data":[{"id":%q,"workspace_id":%q,"key":"PRO","revision":"7"}],"next_cursor":null}`, taskActionFixtureProject, taskActionFixtureWorkspace)
		case "/api/v1/projects/" + taskActionFixtureProject + "/tasks":
			write(w, r)
		default:
			t.Errorf("unexpected create flow request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func writeCreateReceipt(w http.ResponseWriter, code, title string) {
	w.Header().Set("ETag", taskActionFixtureETag)
	w.Header().Set("Location", "/api/v1/tasks/"+taskActionFixtureTask)
	w.WriteHeader(http.StatusCreated)
	item := taskActionFixture(code, "9007199254740993")
	item["title"] = title
	_ = json.NewEncoder(w).Encode(item)
}

func TestTaskCommentPreservesTextAndRetryIdentity(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	key := "comment-fixture-0001"
	calls := 0
	server := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tasks/"+taskActionFixtureTask+"/comments" || r.Header.Get("Idempotency-Key") != key || r.Header.Get("If-Match") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request %v", r)
		}
		var body struct {
			Body json.RawMessage `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		markdown, err := canonicalrichtext.ToMarkdown(body.Body)
		if err != nil || string(markdown) != "Prepared evidence é next\n" {
			t.Errorf("body=%q err=%v", markdown, err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(canonicalComment(t, "Prepared evidence é\nnext", "user", nil, 1, "2026-09-26T10:00:00Z"))
	}))
	defer server.Close()
	for range 2 {
		out, diagnostics, err := executePreparedTaskFixture(t, server, "Prepared evidence é\r\nnext\r\n", "task", "comment", "pro-1", "-", "--idempotency-key", key)
		if err != nil || out != "PRO-1\n" || !strings.Contains(diagnostics, "--task-id "+taskActionFixtureTask+" --idempotency-key "+key) {
			t.Fatalf("out=%q diagnostics=%q err=%v", out, diagnostics, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestTaskWriteInputErrorsMakeNoRequest(t *testing.T) {
	calls := 0
	server := newPreparedHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	for _, text := range []string{"", " \n", "a\x00b", "\xff", strings.Repeat("a", maxTaskTextBytes+1), "\x1b[31m"} {
		out, _, err := executePreparedTaskFixture(t, server, text, "task", "comment", "PRO-1", "-")
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
		_, _, err := executePreparedTaskFixture(t, server, "", args...)
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
	server := newPreparedHTTPS(t, canonicalCreateFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects/"+taskActionFixtureProject+"/tasks" || r.Method != http.MethodPost {
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
		if _, exists := body["description"]; exists {
			t.Error("invented description")
		}
		writeCreateReceipt(w, "PRO-2", "Build batch")
	}))
	defer server.Close()
	out, _, err := executePreparedTaskFixture(t, server, "", "task", "create", "Build batch", "--project", "pro", "--field", "code")
	if err != nil || out != "PRO-2\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}

	out, _, err = executePreparedTaskFixture(t, server, "", "task", "create", "Build batch", "--project", "pro", "--json")
	var receipt struct {
		Data struct {
			Code     string `json:"code"`
			Revision string `json:"revision"`
		} `json:"data"`
		ETag     string `json:"etag"`
		Location string `json:"location"`
	}
	if err != nil || json.Unmarshal([]byte(out), &receipt) != nil || receipt.Data.Code != "PRO-2" ||
		receipt.Data.Revision != "9007199254740993" || receipt.ETag != taskActionFixtureETag ||
		receipt.Location != "/api/v1/tasks/"+taskActionFixtureTask {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestProjectTaskPagesExposeOpaqueContinuation(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	requests := 0
	cursor := "opaque/+cursor?=&"
	server := newPreparedHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/11111111-1111-4111-8111-111111111111/tasks" {
			canonicalNavigationTestHandler(t, "assign", "https://app.assign.so")(w, r)
			return
		}
		requests++
		if r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("project_id") != "22222222-2222-4222-8222-222222222222" {
			t.Errorf("wrong scoped query %s", r.URL)
		}
		if got := r.URL.Query().Get("cursor"); got != "" && got != cursor {
			t.Errorf("corrupted cursor %q", got)
		}
		canonicalNavigationTestHandler(t, "assign", "https://app.assign.so")(w, r)
	}))
	defer server.Close()
	out, _, err := executePreparedTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", "1", "--field", "next_cursor")
	if err != nil || out != cursor+"\n" || requests != 1 {
		t.Fatalf("out=%q err=%v requests=%d", out, err, requests)
	}
	out, diagnostics, err := executePreparedTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", "1", "--cursor", cursor, "--field", "code")
	if err != nil || out != "PRO-2\n" || diagnostics != "" || requests != 2 {
		t.Fatalf("out=%q diagnostics=%q err=%v requests=%d", out, diagnostics, err, requests)
	}
	out, _, err = executePreparedTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", "1", "--cursor", cursor, "--field", "next_cursor")
	if err != nil || out != "\n" {
		t.Fatalf("end cursor=%q err=%v", out, err)
	}
	for _, flag := range []string{"0", "101"} {
		_, _, err := executePreparedTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", flag)
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
	for _, body := range []string{`{"data":[]}`, `{"data":[],"next_cursor":""}`, `{"data":[{},{}],"next_cursor":null}`} {
		server := newPreparedHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/workspaces/11111111-1111-4111-8111-111111111111/tasks" {
				canonicalNavigationTestHandler(t, "assign", "https://app.assign.so")(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}))
		out, _, err := executePreparedTaskFixture(t, server, "", "project", "tasks", "PRO", "--limit", "1")
		server.Close()
		if err == nil || out != "" {
			t.Fatalf("out=%q err=%v", out, err)
		}
	}
}

func TestLifecycleIdenticalRetryRetainsKeyAndRevision(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	key := "lifecycle-fixture-001"
	calls := 0
	server := newPreparedHTTPS(t, taskActionFixtureHandler(t, "PRO-1", "7", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Idempotency-Key") != key || r.Header.Get("If-Match") != taskActionFixtureETag || r.URL.Path != "/api/v1/tasks/"+taskActionFixtureTask+"/complete" {
			t.Errorf("headers=%v", r.Header)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		taskActionFixtureResult(w, true, false)
	}))
	defer server.Close()
	args := []string{"task", "done", "PRO-1", "--revision", "7", "--idempotency-key", key}
	out, _, err := executePreparedTaskFixture(t, server, "", args...)
	if err == nil || out != "" || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "uncertain") || calls != 1 {
		t.Fatalf("out=%q err=%v calls=%d", out, err, calls)
	}
	out, _, err = executePreparedTaskFixture(t, server, "", args...)
	if err != nil || out != "PRO-1  Ship now\n" || calls != 2 {
		t.Fatalf("out=%q err=%v calls=%d", out, err, calls)
	}
}

func TestGeneratedMutationKeyIsRecoverableOnMalformedResponse(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	var key string
	server := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"revision":1} trailing`)
	}))
	defer server.Close()
	out, _, err := executePreparedTaskFixture(t, server, "", "task", "comment", "PRO-1", "fixture")
	if err == nil || out != "" || len(key) < 16 || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "apt_fixture") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestMutationErrorCategories(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	for status, want := range map[int]int{401: ExitAuthentication, 403: ExitAuthentication, 404: ExitContext, 409: ExitConflict, 429: ExitOperation} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			out, _, err := executePreparedTaskFixture(t, server, "", "task", "comment", "PRO-1", "fixture")
			if err == nil || exitCode(err) != want || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}

func TestProjectJSONPageReadsTasksAndCursorInOneRequest(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	calls := 0
	server := newPreparedHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/11111111-1111-4111-8111-111111111111/tasks" {
			canonicalNavigationTestHandler(t, "assign", "https://app.assign.so")(w, r)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"44444444-4444-4444-8444-444444444444","workspace_id":"11111111-1111-4111-8111-111111111111","project_id":"22222222-2222-4222-8222-222222222222","status_id":"66666666-6666-4666-8666-666666666666","task_number":"1","code":"PRO-1","title":"child","revision":"7","web_url":"https://app.assign.so/app/assign/tasks/PRO-1"}],"included":{"statuses":[{"id":"66666666-6666-4666-8666-666666666666","category":"todo"}]},"coverage":"complete","next_cursor":"next-page"}`)
	}))
	defer server.Close()
	out, diagnostics, err := executePreparedTaskFixture(t, server, "", "project", "tasks", "PRO", "--format", "json")
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
		_, _, err := executePreparedTaskFixture(t, server, "", args...)
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
	writes := 0
	origin := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writes++
		http.Redirect(w, r, "https://redirect.example.invalid/forbidden", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	out, _, err := executePreparedTaskFixture(t, origin, "", "task", "comment", "PRO-1", "fixture")
	if err == nil || out != "" || writes != 1 {
		t.Fatalf("out=%q err=%v original writes=%d", out, err, writes)
	}
}

func TestTaskCommentsFirstNextAndFinalPages(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	requests := 0
	cursor := "opaque/+cursor?=&"
	server := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/tasks/"+taskActionFixtureTask+"/comments" || r.Header.Get("Authorization") != "Bearer "+os.Getenv("ASSIGN_TOKEN") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		if r.URL.Query().Get("cursor") == "" {
			if r.URL.Query().Get("limit") != "2" {
				t.Errorf("unexpected limit %s", r.URL)
			}
			ada := "Ada"
			writeCommentPage(w, &cursor,
				canonicalComment(t, "First **note**", "user", &ada, 1, "2026-09-26T10:00:00Z"),
				canonicalComment(t, "Second note", "user:mcp", nil, 2, "2026-09-26T11:00:00Z"))
			return
		}
		if r.URL.Query().Get("cursor") != cursor {
			t.Errorf("cursor corrupted: %q", r.URL.Query().Get("cursor"))
		}
		helper := "Helper"
		writeCommentPage(w, nil, canonicalComment(t, "Final note", "agent", &helper, 3, "2026-09-26T12:00:00Z"))
	}))
	defer server.Close()
	out, diagnostics, err := executePreparedTaskFixture(t, server, "", "task", "comments", "pro-1", "--limit", "2")
	want := "## Ada · 2026-09-26T10:00:00Z\n\nFirst **note**\n\n## user:mcp · 2026-09-26T11:00:00Z\n\nSecond note\n"
	if err != nil || out != want || !strings.Contains(diagnostics, "--cursor "+cursor) || strings.Contains(out, "assign:") || requests != 1 {
		t.Fatalf("out=%q diagnostics=%q err=%v requests=%d", out, diagnostics, err, requests)
	}
	out, diagnostics, err = executePreparedTaskFixture(t, server, "", "task", "comments", "PRO-1", "--limit", "2", "--cursor", cursor)
	if err != nil || out != "## Helper · 2026-09-26T12:00:00Z\n\nFinal note\n" || diagnostics != "" || requests != 2 {
		t.Fatalf("out=%q diagnostics=%q err=%v requests=%d", out, diagnostics, err, requests)
	}
}

func TestTaskCommentsEmptyPageAndTombstones(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	server := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "50" {
			t.Errorf("default limit not sent: %s", r.URL)
		}
		writeCommentPage(w, nil)
	}))
	defer server.Close()
	out, diagnostics, err := executePreparedTaskFixture(t, server, "", "task", "comments", "PRO-1")
	if err != nil || out != "" || diagnostics != "" {
		t.Fatalf("empty page out=%q diagnostics=%q err=%v", out, diagnostics, err)
	}
	tombstones := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) {
		ada := "Ada"
		writeCommentPage(w, nil, canonicalComment(t, "", "user", &ada, 4, "2026-09-26T13:00:00Z"))
	}))
	defer tombstones.Close()
	out, _, err = executePreparedTaskFixture(t, tombstones, "", "task", "comments", "PRO-1")
	if err != nil || out != "## Ada · 2026-09-26T13:00:00Z\n\n[deleted comment]\n" {
		t.Fatalf("tombstone out=%q err=%v", out, err)
	}
}

func TestTaskCommentsInvalidInputMakesNoRequest(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	calls := 0
	server := newPreparedHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	for _, args := range [][]string{
		{"task", "comments", "PRO-1", "--limit", "0"},
		{"task", "comments", "PRO-1", "--limit", "101"},
		{"task", "comments", "PRO-1", "--cursor", strings.Repeat("c", 4097)},
		{"task", "comments", "PRO-1", "--cursor", "bad\x1bcurse"},
		{"task", "comments", "  "},
	} {
		out, _, err := executePreparedTaskFixture(t, server, "", args...)
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
	valid, err := json.Marshal(canonicalComment(t, "note", "user", nil, 1, "2026-09-26T10:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	invalidActor := strings.Replace(string(valid), `"actor":"user"`, `"actor":""`, 1)
	bodies := []string{
		`{"data":[],"has_more":true}`,
		`{"data":[],"has_more":false,"next_cursor":"unexpected"}`,
		fmt.Sprintf(`{"data":[%s,%s],"has_more":false,"next_cursor":null}`, valid, valid),
		fmt.Sprintf(`{"data":[%s],"has_more":false,"next_cursor":null}`, invalidActor),
		`{"data":null,"has_more":false,"next_cursor":null}`,
		`{"data":[],"has_more":true,"next_cursor":"` + strings.Repeat("c", 4097) + `"}`,
		`{"data":[],"has_more":true,"next_cursor":"old-cursor"}`,
	}
	for _, body := range bodies {
		server := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		out, _, err := executePreparedTaskFixture(t, server, "", "task", "comments", "PRO-1", "--limit", "1", "--cursor", "old-cursor")
		server.Close()
		if err == nil || out != "" {
			t.Fatalf("body=%q out=%q err=%v", body, out, err)
		}
	}
}

func TestTaskCommentsErrorCategories(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	for status, want := range map[int]int{400: ExitArguments, 401: ExitAuthentication, 404: ExitContext, 429: ExitOperation, 500: ExitOperation, 503: ExitOperation} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			out, _, err := executePreparedTaskFixture(t, server, "", "task", "comments", "PRO-1")
			if err == nil || exitCode(err) != want || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}

func TestTaskCommentsOutputSafety(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	server := newPreparedHTTPS(t, canonicalCommentFixture(t, func(w http.ResponseWriter, r *http.Request) {
		name := "Ada\nInjected"
		comment := canonicalComment(t, "Note red\nbinary", "user", &name, 1, "2026-09-26T10:00:00Z")
		comment.Body = bytes.Replace(comment.Body, []byte("Note red"), []byte(`Note\u001b[31m red`), 1)
		writeCommentPage(w, nil, comment)
	}))
	defer server.Close()
	out, _, err := executePreparedTaskFixture(t, server, "", "task", "comments", "PRO-1")
	if err != nil || strings.Contains(out, "\x1b") || strings.Contains(out, "\x00") || !strings.HasPrefix(out, "## Ada Injected · 2026-09-26T10:00:00Z\n\n") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestTaskCommentsDiscoveryPreservesCreate(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	server := newPreparedHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	out, _, err := executePreparedTaskFixture(t, server, "", "task", "--help")
	if err != nil || !strings.Contains(out, "comments") || !strings.Contains(out, "comment") {
		t.Fatalf("task help out=%q err=%v", out, err)
	}
	out, _, err = executePreparedTaskFixture(t, server, "", "task", "comments", "--help")
	if err != nil || !strings.Contains(out, "--limit") || !strings.Contains(out, "--cursor") {
		t.Fatalf("comments help out=%q err=%v", out, err)
	}
}

func TestCreateDescriptionAndPriorityReachCanonicalRequest(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	server := newPreparedHTTPS(t, canonicalCreateFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Description json.RawMessage `json:"description"`
			Priority    string          `json:"priority"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		markdown, err := canonicalrichtext.ToMarkdown(body.Description)
		if err != nil || string(markdown) != "**Prepared** é\n" || body.Priority != "high" {
			t.Errorf("description=%q priority=%q err=%v", markdown, body.Priority, err)
		}
		writeCreateReceipt(w, "PRO-1", "Child")
	}))
	defer server.Close()
	out, _, err := executePreparedTaskFixture(t, server, "**Prepared**\r\né\r\n", "task", "create", "Child", "--project", "PRO", "--description", "-", "--priority", "high")
	if err != nil || out != "PRO-1  Child\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

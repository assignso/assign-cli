package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanonicalNavigationResources(t *testing.T) {
	const workspace = "11111111-1111-4111-8111-111111111111"
	const project = "22222222-2222-4222-8222-222222222222"
	const document = "33333333-3333-4333-8333-333333333333"
	const milestone = "44444444-4444-4444-8444-444444444444"
	const task = "55555555-5555-4555-8555-555555555555"
	contentReads := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer apt_prepared" {
			t.Errorf("credential changed")
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/cli/") {
			t.Errorf("legacy resource request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/dev/workspace":
			fmt.Fprintf(w, `{"id":%q,"name":"Assign","slug":"demo","role":"owner"}`, workspace)
		case "/api/v1/workspaces/" + workspace + "/projects":
			fmt.Fprintf(w, `{"data":[{"id":%q,"workspace_id":%q,"key":"PRO","name":"Product","path":"product","revision":"9223372036854775807","url":"https://external.invalid","web_url":"https://app.assign.so/app/demo/projects/product"}],"next_cursor":null}`, project, workspace)
		case "/api/v1/workspaces/" + workspace + "/documents":
			if r.URL.Query().Get("project_id") != "" && r.URL.Query().Get("project_id") != project {
				t.Errorf("wrong Project selector")
			}
			fmt.Fprintf(w, `{"data":[{"id":%q,"workspace_id":%q,"path":"plan","title":"Plan","visibility":"project","revision":"9007199254740993","web_url":"https://app.assign.so/app/demo/documents/plan"}],"next_cursor":null}`, document, workspace)
		case "/api/v1/documents/" + document + "/content":
			contentReads++
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			io.WriteString(w, "# Plan\n\n**Keep formatting**\n")
		case "/api/v1/workspaces/" + workspace + "/milestones":
			if r.URL.Query().Get("project_id") != project || r.URL.Query().Get("code") != "PRO-M1" {
				t.Errorf("wrong exact Milestone selector")
			}
			fmt.Fprintf(w, `{"data":[{"id":%q,"workspace_id":%q,"project_id":%q,"code":"PRO-M1","milestone_number":"1","name":"Release","status":"active","revision":"9007199254740993","progress":{"total_tasks":"2","completed_tasks":"1","incomplete_tasks":"1","tasks_by_category":{"todo":"1","done":"1"}}}],"next_cursor":null}`, milestone, workspace, project)
		case "/api/v1/workspaces/" + workspace + "/work":
			fmt.Fprintf(w, `{"data":[{"id":%q,"workspace_id":%q,"project_id":%q,"code":"PRO-1","title":"Ship","revision":"9007199254740993"}],"next_cursor":null}`, task, workspace, project)
		case "/api/v1/workspaces/" + workspace + "/tasks":
			if r.URL.Query().Get("project_id") != project {
				t.Errorf("wrong Task Project selector")
			}
			fmt.Fprintf(w, `{"data":[{"id":%q,"workspace_id":%q,"project_id":%q,"status_id":"66666666-6666-4666-8666-666666666666","task_number":"1","code":"PRO-1","title":"Ship","revision":"9007199254740993","web_url":"https://app.assign.so/app/demo/tasks/PRO-1"}],"included":{"statuses":[{"id":"66666666-6666-4666-8666-666666666666","category":"todo"}]},"next_cursor":null,"coverage":"complete"}`, task, workspace, project)
		default:
			t.Errorf("unexpected request %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_prepared")
	opts := &options{host: server.URL, credentials: &memoryCredentialStore{}}
	item, err := getProject(context.Background(), opts, server.Client(), "PRO")
	if err != nil || item.Revision != 9223372036854775807 || strings.Contains(item.URL, "external") {
		t.Fatalf("Project: %+v %v", item, err)
	}
	var output bytes.Buffer
	if err = runDocumentURL(context.Background(), &output, opts, server.Client(), "plan", false); err != nil || contentReads != 0 {
		t.Fatalf("metadata navigation: %v reads%d", err, contentReads)
	}
	doc, err := getDocument(context.Background(), opts, server.Client(), "plan")
	if err != nil || doc.Revision != 9007199254740993 || doc.Body != "# Plan\n\n**Keep formatting**\n" {
		t.Fatalf("Document: %+v %v", doc, err)
	}
	mile, err := readMilestoneResource(context.Background(), opts, server.Client(), "PRO-M1")
	if err != nil || mile.Revision != 9007199254740993 || *mile.Progress.CompletedTasks != 1 {
		t.Fatalf("Milestone: %+v %v", mile, err)
	}
	output.Reset()
	if err = runMyWork(context.Background(), &output, server.URL, server.Client(), opts.credentials); err != nil || output.String() != "PRO-1  Ship\n" {
		t.Fatalf("My Work: %q %v", output.String(), err)
	}
	output.Reset()
	if err = runProjectTaskPage(context.Background(), &output, io.Discard, opts, server.Client(), "PRO", taskPageOptions{limit: 100, format: "json"}); err != nil || !strings.Contains(output.String(), `"revision":9007199254740993`) {
		t.Fatalf("Project Task page: %s %v", output.String(), err)
	}
}

func TestCanonicalNavigationRejectsUntrustedMetadata(t *testing.T) {
	workspace := cliWorkspace{Slug: "demo"}
	for _, raw := range []string{"https://external.invalid", "https://app.assign.so/app/other/projects/product", "https://user:secret@app.assign.so/app/demo/projects/product", "https://app.assign.so/app/demo/projects/product?token=bad"} {
		if navigationURL(raw, workspace, "projects", "product") == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestCanonicalMilestoneTaskDecimals(t *testing.T) {
	var item milestoneTaskQueryItem
	if err := json.Unmarshal([]byte(`{"revision":"9007199254740993","task_number":"9223372036854775807"}`), &item); err != nil || item.Revision != 9007199254740993 || item.TaskNumber == nil || *item.TaskNumber != 9223372036854775807 {
		t.Fatalf("decimal binding: %+v %v", item, err)
	}
	if err := json.Unmarshal([]byte(`{"revision":"9223372036854775808","task_number":"1"}`), &item); err == nil {
		t.Fatal("accepted overflowing revision")
	}
}

func canonicalNavigationTestHandler(t *testing.T, slug, origin string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const workspace = "11111111-1111-4111-8111-111111111111"
		const project = "22222222-2222-4222-8222-222222222222"
		switch r.URL.Path {
		case "/api/v1/dev/workspace":
			fmt.Fprintf(w, `{"id":%q,"name":"Assign","slug":%q,"role":"owner"}`, workspace, slug)
		case "/api/v1/workspaces/" + workspace + "/projects":
			fmt.Fprintf(w, `{"data":[{"id":%q,"workspace_id":%q,"key":"PRO","name":"Product","path":"product","revision":"3","web_url":%q}],"next_cursor":null}`, project, workspace, origin+"/app/"+slug+"/projects/product")
		case "/api/v1/workspaces/" + workspace + "/documents":
			fmt.Fprintf(w, `{"data":[{"id":"33333333-3333-4333-8333-333333333333","workspace_id":%q,"path":"release-plan","title":"Release plan","visibility":"project","revision":"2","web_url":%q}],"next_cursor":null}`, workspace, origin+"/app/"+slug+"/documents/release-plan")
		case "/api/v1/documents/33333333-3333-4333-8333-333333333333/content":
			w.Header().Set("Content-Type", "text/markdown")
			io.WriteString(w, "Ship on Friday.")
		case "/api/v1/workspaces/" + workspace + "/tasks":
			code, title, next := "PRO-1", "Ship navigation", any(nil)
			if r.URL.Query().Get("limit") == "1" {
				title = "first"
				next = "opaque/+cursor?=&"
				if r.URL.Query().Get("cursor") != "" {
					code, title, next = "PRO-2", "second", nil
				}
			}
			fmt.Fprintf(w, `{"data":[{"id":"44444444-4444-4444-8444-444444444444","workspace_id":%q,"project_id":%q,"status_id":"55555555-5555-4555-8555-555555555555","code":%q,"title":%q,"task_number":%q,"revision":"3","web_url":%q}],"included":{"statuses":[{"id":"55555555-5555-4555-8555-555555555555","category":"todo"}]},"next_cursor":%s}`, workspace, project, code, title, strings.TrimPrefix(code, "PRO-"), origin+"/app/"+slug+"/tasks/"+code, mustFixtureJSON(next))
		default:
			t.Errorf("unexpected canonical resource %s", r.URL)
			http.NotFound(w, r)
		}
	}
}

func mustFixtureJSON(value any) string { data, _ := json.Marshal(value); return string(data) }

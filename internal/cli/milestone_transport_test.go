package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fixtureMilestoneID = "10000000-0000-4000-8000-000000000015"

func fixtureMilestone(revision string) map[string]any {
	return map[string]any{
		"id": fixtureMilestoneID, "workspace_id": taskActionFixtureWorkspace,
		"project_id": taskActionFixtureProject, "code": "PRO-M1", "milestone_number": "1",
		"name": "Delivery", "revision": revision, "status": "active",
		"progress": map[string]any{
			"total_tasks": "1", "completed_tasks": "0", "incomplete_tasks": "1",
			"tasks_by_category": map[string]string{"todo": "1"},
		},
	}
}

func fixtureMilestoneTask(revision string) map[string]any {
	task := taskActionFixture("PRO-123", revision)
	task["milestone_id"] = fixtureMilestoneID
	task["priority"] = "medium"
	task["lifecycle"] = "active"
	return task
}

func TestMilestoneCheckedCanonicalPages(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	for _, tc := range []struct {
		name, coverage, reason, count string
		inputCursor, outputCursor     string
		limit                         string
		empty, wrongMember            bool
		httpStatus, exit              int
		stdout                        string
	}{
		{name: "complete", coverage: "complete", count: "1", stdout: `"coverage":"complete"`},
		{name: "continuation", coverage: "partial", reason: "more_pages", count: "1", outputCursor: "opaque/+cursor?=&", stdout: `"has_more":true`},
		{name: "resume", coverage: "complete", count: "2", inputCursor: "opaque/+cursor?=&", stdout: `"returned_count":2`},
		{name: "empty continuation", coverage: "partial", reason: "more_pages", count: "0", outputCursor: "opaque", empty: true, stdout: `"items":[]`},
		{name: "empty complete", coverage: "complete", count: "0", empty: true, stdout: `"items":[]`},
		{name: "capped", coverage: "partial", reason: "scan_limit", count: "901", limit: "100", inputCursor: "opaque", exit: ExitOperation, stdout: `"limit_reason":"scan_limit"`},
		{name: "stale receipt", coverage: "stale", reason: "collection_or_visibility_changed", count: "0", empty: true, inputCursor: "opaque", exit: ExitOperation, stdout: `"coverage":"stale"`},
		{name: "invalid cursor", inputCursor: "opaque", httpStatus: 400, exit: ExitArguments},
		{name: "denied", httpStatus: 403, exit: ExitAuthentication},
		{name: "unavailable", httpStatus: 404, exit: ExitContext},
		{name: "wrong membership", coverage: "complete", count: "1", wrongMember: true, exit: ExitOperation},
		{name: "malformed count", coverage: "complete", count: "01", exit: ExitOperation},
		{name: "repeated cursor", coverage: "partial", reason: "more_pages", count: "2", inputCursor: "opaque", outputCursor: "opaque", exit: ExitOperation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			limit := tc.limit
			if limit == "" {
				limit = "1"
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer apt_fixture" {
					t.Error("wrong credential")
				}
				w.Header().Set("Content-Type", "application/json")
				q := r.URL.Query()
				switch r.URL.Path {
				case "/api/v1/workspaces/" + taskActionFixtureWorkspace + "/projects":
					if q.Get("code") != "PRO" || q.Get("limit") != "100" {
						t.Errorf("Project selector %v", q)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": taskActionFixtureProject, "workspace_id": taskActionFixtureWorkspace, "key": "PRO", "revision": "1"}}, "next_cursor": nil})
				case "/api/v1/workspaces/" + taskActionFixtureWorkspace + "/milestones":
					if q.Get("project_id") != taskActionFixtureProject || q.Get("code") != "PRO-M1" || q.Get("limit") != "1" || q.Get("include_archived") != "true" {
						t.Errorf("Milestone selector %v", q)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{fixtureMilestone("9007199254740993")}, "next_cursor": nil})
				case "/api/v1/workspaces/" + taskActionFixtureWorkspace + "/tasks":
					reads++
					if q.Get("project_id") != taskActionFixtureProject || q.Get("milestone_id") != fixtureMilestoneID || q.Get("consistency") != "checked" || q.Get("order") != "updated_at" || q.Get("state") != "all" || q.Get("include_archived") != "false" || q.Get("limit") != limit || q.Get("cursor") != tc.inputCursor {
						t.Errorf("Task selector %v", q)
					}
					if tc.httpStatus != 0 {
						w.WriteHeader(tc.httpStatus)
						return
					}
					items := []any{}
					if !tc.empty {
						task := fixtureMilestoneTask("9007199254740995")
						if tc.wrongMember {
							task["milestone_id"] = nil
						}
						items = append(items, task)
					}
					var next any
					if tc.outputCursor != "" {
						next = tc.outputCursor
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"data": items, "included": map[string]any{}, "next_cursor": next, "coverage": tc.coverage,
						"limit_reason": tc.reason, "returned_count": tc.count, "query_digest": strings.Repeat("a", 64),
						"visibility_revision": strings.Repeat("b", 64), "collection_version": taskActionFixtureProject,
						"observed_at": "2026-11-24T00:00:00Z",
					})
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			args := []string{"milestone", "tasks", "pro-m1", "--workspace-id", taskActionFixtureWorkspace, "--limit", limit, "--format", "json"}
			if tc.inputCursor != "" {
				args = append(args, "--cursor", tc.inputCursor)
			}
			out, _, err := executeTaskFixture(t, server, "", args...)
			if reads != 1 || exitCode(err) != tc.exit || (tc.stdout == "" && out != "") || !strings.Contains(out, tc.stdout) {
				t.Fatalf("reads=%d exit=%d error=%v stdout=%q", reads, exitCode(err), err, out)
			}
			if !tc.empty && tc.stdout != "" && (!strings.Contains(out, `"milestone_revision":9007199254740993`) || !strings.Contains(out, `"revision":9007199254740995`)) {
				t.Fatalf("inexact revision: %s", out)
			}
		})
	}
}

func TestMilestoneScopedContextCanonicalReadFence(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	for _, scenario := range []string{"unchanged", "task revision", "Milestone revision", "membership", "partial Milestone", "invalid selector"} {
		t.Run(scenario, func(t *testing.T) {
			var taskReads, milestoneReads, contexts int
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/workspaces/" + taskActionFixtureWorkspace + "/tasks":
					taskReads++
					if r.URL.Query().Get("code") != "PRO-123" {
						t.Errorf("Task scope %s", r.URL)
					}
					task := fixtureMilestoneTask("5")
					if contexts > 0 && scenario == "task revision" {
						task["revision"] = "6"
					}
					if contexts > 0 && scenario == "membership" {
						task["milestone_id"] = nil
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{task}, "next_cursor": nil, "coverage": "complete"})
				case "/api/v1/workspaces/" + taskActionFixtureWorkspace + "/milestones":
					milestoneReads++
					q := r.URL.Query()
					if q.Get("project_id") != taskActionFixtureProject || q.Get("code") != "PRO-M1" || q.Get("limit") != "1" || q.Get("include_archived") != "true" || q.Get("cursor") != "" {
						t.Errorf("Milestone scope %s", r.URL)
					}
					milestone := fixtureMilestone("7")
					if contexts > 0 && scenario == "Milestone revision" {
						milestone["revision"] = "8"
					}
					var cursor any
					if scenario == "partial Milestone" {
						cursor = "incomplete"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{milestone}, "next_cursor": cursor})
				case "/api/v1/tasks/" + taskActionFixtureTask + "/context":
					contexts++
					_ = json.NewEncoder(w).Encode(map[string]any{"task_code": "PRO-123", "format": "markdown", "profile": "compact", "markdown": "# Prepared Task\n", "truncated": false})
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			code := "pro-m1"
			if scenario == "invalid selector" {
				code = "PRO-M01"
			}
			out, _, err := executeTaskFixture(t, server, "", "task", "context", "PRO-123", "--workspace-id", taskActionFixtureWorkspace, "--milestone", code)
			switch scenario {
			case "unchanged":
				if err != nil || !strings.Contains(out, "PRO-M1 (revision 7); Task PRO-123 (revision 5)") {
					t.Fatalf("context output %q error %v", out, err)
				}
			case "invalid selector":
				if out != "" || exitCode(err) != ExitArguments || taskReads+milestoneReads+contexts != 0 {
					t.Fatalf("unexpected output, error or reads: %q %v (%d %d %d)", out, err, taskReads, milestoneReads, contexts)
				}
				return
			default:
				if out != "" || exitCode(err) != ExitOperation {
					t.Fatalf("fence output %q error %v", out, err)
				}
			}
			if scenario == "partial Milestone" {
				if taskReads != 1 || milestoneReads != 1 || contexts != 0 {
					t.Fatalf("continued after ambiguous Milestone: %d %d %d", taskReads, milestoneReads, contexts)
				}
			} else if taskReads != 2 || milestoneReads != 2 || contexts != 1 {
				t.Fatalf("unexpected fence reads: %d %d %d", taskReads, milestoneReads, contexts)
			}
		})
	}
}

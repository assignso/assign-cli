package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTaskBranchSuggestionUsesAuthorizedProjectAndTask(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	for _, test := range []struct {
		name, branch string
		status       int
	}{
		{"default", "assign/PRO-123-ship-now", http.StatusOK},
		{"custom", "work/PRO-123/ship-now", http.StatusOK},
		{"denied", "", http.StatusForbidden},
		{"invalid receipt", "work/PRO-123\ninjected", http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			suggestions := 0
			base := taskActionFixtureHandler(t, "PRO-123", "7", func(w http.ResponseWriter, r *http.Request) {
				t.Error("a branch suggestion must be read-only")
			})
			client := taskActionFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/projects/"+taskActionFixtureProject {
					base(w, r)
					return
				}
				suggestions++
				if r.Method != http.MethodGet || r.URL.Query().Get("task_id") != taskActionFixtureTask ||
					r.URL.Query().Get("view") != "task-branch-template" || r.Header.Get("Authorization") != "Bearer prepared-task-token" {
					t.Errorf("unexpected suggestion request %s %s", r.Method, r.URL)
				}
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"task_id": taskActionFixtureTask, "branch": test.branch})
			}))
			var out, diagnostics bytes.Buffer
			root := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostics, client)
			root.SetArgs([]string{"--host", taskActionFixtureHost, "task", "branch-suggestion", "pro-123"})
			err := root.ExecuteContext(context.Background())
			if suggestions != 1 {
				t.Fatalf("suggestions = %d, error %v", suggestions, err)
			}
			if test.status == http.StatusOK && test.name != "invalid receipt" {
				if err != nil || out.String() != test.branch+"\n" {
					t.Fatalf("output = %q, error %v", out.String(), err)
				}
			} else if err == nil || out.Len() != 0 {
				t.Fatalf("unsafe or unauthorized suggestion output = %q, error %v", out.String(), err)
			}
		})
	}
}

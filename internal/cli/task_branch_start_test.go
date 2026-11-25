package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	branchFixtureOrigin = "https://github.com/prepared/repository"
	branchFixtureID     = "10000000-0000-4000-8000-000000000015"
	branchFixtureRef    = "work+review/PRO-123-ship-now"
)

func branchFixtureGit(t *testing.T) func(...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("Git required for the branch workflow check")
	}
	t.Chdir(t.TempDir())
	git := func(args ...string) string {
		t.Helper()
		got, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, got, err)
		}
		return strings.TrimSpace(string(got))
	}
	git("init", "-b", "main")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "Base")
	git("remote", "add", "origin", "git@github.com:prepared/repository.git")
	return git
}

type branchWorkflowFixture struct {
	startRequests, startEffects                           int
	linkRequests, linkEffects                             int
	lostStart, lostLink, foreign, staleBinding, unsafeRef bool
	linkBody                                              taskBranchPlan
	linkKey                                               string
	afterStart                                            func()
}

func (f *branchWorkflowFixture) handle(t *testing.T) http.HandlerFunc {
	t.Helper()
	canonical := taskActionFixtureHandler(t, "PRO-123", "7", func(w http.ResponseWriter, r *http.Request) {
		f.startRequests++
		if r.URL.Path != "/api/v1/tasks/"+taskActionFixtureTask+"/start" || r.Header.Get("Idempotency-Key") != taskActionFixtureKey || r.Header.Get("If-Match") != taskActionFixtureETag {
			t.Errorf("changed start intent %s", r.URL.Path)
		}
		if f.startEffects == 0 {
			f.startEffects++
			if f.afterStart != nil {
				f.afterStart()
			}
			if f.lostStart {
				w.WriteHeader(503)
				return
			}
		}
		taskActionFixtureResult(w, "PRO-123", f.startRequests > 1)
	})
	return func(w http.ResponseWriter, r *http.Request) {
		projectPath := "/api/v1/projects/" + taskActionFixtureProject
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case projectPath:
			if r.URL.Query().Get("view") != "task-branch-template" || r.URL.Query().Get("task_id") != taskActionFixtureTask {
				t.Error("incorrect suggestion scope")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"task_id": taskActionFixtureTask, "branch": branchFixtureRef})
		case projectPath + "/task-branch-start":
			if r.Method == http.MethodGet {
				if r.URL.Query().Get("repository") != branchFixtureOrigin || r.URL.Query().Get("task_id") != taskActionFixtureTask || f.foreign {
					w.WriteHeader(404)
					return
				}
				plan := taskBranchPlan{taskActionFixtureWorkspace, taskActionFixtureProject, taskActionFixtureTask, branchFixtureID, branchFixtureOrigin, branchFixtureRef, "5", "7", "2:3:4"}
				if f.unsafeRef {
					plan.Branch = "unsafe/contains..dots"
				}
				if f.startEffects > 0 {
					plan.TaskRevision = "8"
					if f.staleBinding {
						plan.BindingRevision = "3:3:4"
					}
				}
				_ = json.NewEncoder(w).Encode(plan)
				return
			}
			f.linkRequests++
			var input taskBranchPlan
			if r.Method != http.MethodPut || json.NewDecoder(r.Body).Decode(&input) != nil ||
				input.TaskID != taskActionFixtureTask || input.BindingID != branchFixtureID || input.Branch != branchFixtureRef ||
				input.TaskRevision != "8" || input.ProjectRevision != "5" || input.BindingRevision != "2:3:4" ||
				input.Repository != branchFixtureOrigin || input.WorkspaceID != "" || input.ProjectID != "" ||
				r.Header.Get("Idempotency-Key") == "" {
				t.Errorf("invalid association request: %+v", input)
			}
			if f.linkEffects == 0 {
				f.linkEffects++
				f.linkBody, f.linkKey = input, r.Header.Get("Idempotency-Key")
				if f.lostLink {
					w.WriteHeader(503)
					return
				}
			} else if input != f.linkBody || r.Header.Get("Idempotency-Key") != f.linkKey {
				t.Error("association recovery changed the original intent")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"task_id": taskActionFixtureTask, "project_id": taskActionFixtureProject, "binding_id": branchFixtureID, "branch": branchFixtureRef})
		default:
			canonical(w, r)
		}
	}
}

func testBranchCLI(t *testing.T, f *branchWorkflowFixture) func(...string) (string, string, error) {
	t.Helper()
	t.Setenv("ASSIGN_TOKEN", "prepared-task-token")
	server := httptest.NewTLSServer(f.handle(t))
	t.Cleanup(server.Close)
	return func(args ...string) (string, string, error) {
		var out, diag bytes.Buffer
		command := newRootCommandWithClient(strings.NewReader(""), &out, &diag, server.Client())
		command.SetArgs(append([]string{"--host", server.URL}, args...))
		err := command.ExecuteContext(context.Background())
		return out.String(), diag.String(), err
	}
}

func TestTaskBranchSuggestionUsesAuthorizedProjectAndTask(t *testing.T) {
	run := testBranchCLI(t, &branchWorkflowFixture{})
	out, _, err := run("task", "branch-suggestion", "PRO-123")
	if err != nil || out != branchFixtureRef+"\n" {
		t.Fatalf("suggestion=%q err=%v", out, err)
	}
}

func TestTaskStartBranchPreflight(t *testing.T) {
	for _, mode := range []string{"dirty", "detached", "operation", "foreign", "unsafe-ref", "existing", "parent", "child", "plain"} {
		t.Run(mode, func(t *testing.T) {
			git := branchFixtureGit(t)
			f := &branchWorkflowFixture{}
			switch mode {
			case "dirty":
				if err := os.WriteFile("draft", []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "detached":
				git("switch", "--detach")
			case "operation":
				if err := os.WriteFile(filepath.Join(".git", "MERGE_HEAD"), []byte(git("rev-parse", "HEAD")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				f.foreign = true
			case "unsafe-ref":
				f.unsafeRef = true
			case "existing":
				git("branch", branchFixtureRef)
			case "parent":
				git("branch", "work+review")
			case "child":
				git("branch", branchFixtureRef+"/child")
			}
			run := testBranchCLI(t, f)
			args := []string{"task", "start", "PRO-123", "--revision", "7", "--idempotency-key", taskActionFixtureKey}
			if mode != "plain" {
				args = append(args, "--branch")
			}
			out, _, err := run(args...)
			if mode == "plain" {
				if err != nil || f.startRequests != 1 || f.linkRequests != 0 || git("branch", "--show-current") != "main" {
					t.Fatalf("ordinary start changed Git: %v", err)
				}
			} else if err == nil || out != "" || f.startRequests != 0 || f.linkRequests != 0 {
				t.Fatalf("%s wrote a Task or association: %q %v %+v", mode, out, err, f)
			}
		})
	}
}

func TestTaskStartBranchRecoversEachPartialEffect(t *testing.T) {
	for _, mode := range []string{"success", "lost-start", "create-ref", "switch", "lost-association", "stale-authority"} {
		t.Run(mode, func(t *testing.T) {
			git := branchFixtureGit(t)
			base := git("rev-parse", "HEAD")
			f := &branchWorkflowFixture{lostStart: mode == "lost-start", lostLink: mode == "lost-association", staleBinding: mode == "stale-authority"}
			var injected string
			switch mode {
			case "create-ref":
				injected = filepath.Join(".git", "refs", "heads", branchFixtureRef+".lock")
				if err := os.MkdirAll(filepath.Dir(injected), 0700); err != nil {
					t.Fatal(err)
				}
				f.afterStart = func() {
					if err := os.WriteFile(injected, []byte("prepared lock"), 0600); err != nil {
						t.Error(err)
					}
				}
			case "switch":
				if runtime.GOOS == "windows" {
					t.Skip("prepared Git switch fault injection uses an executable shell fixture")
				}
				real, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				wrapper := t.TempDir()
				injected = filepath.Join(wrapper, "fail-switch")
				if err = os.WriteFile(injected, nil, 0600); err != nil {
					t.Fatal(err)
				}
				script := fmt.Sprintf("#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = switch ] && [ -f %s ]; then exit 1; fi\ndone\nexec %s \"$@\"\n", taskActionArgument(injected), taskActionArgument(real))
				if err = os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			run := testBranchCLI(t, f)
			out, diag, err := run("task", "start", "PRO-123", "--branch", "--revision", "7", "--idempotency-key", taskActionFixtureKey)
			if mode != "success" {
				if err == nil || out != "" || !strings.Contains(fmt.Sprint(err), "--branch") || !strings.Contains(diag, taskActionFixtureKey) {
					t.Fatalf("missing partial result: err=%v out=%q diag=%q", err, out, diag)
				}
				if mode == "lost-association" {
					if git("branch", "--show-current") != branchFixtureRef {
						t.Fatal("lost association rolled back Git")
					}
				} else if git("branch", "--show-current") != "main" {
					t.Fatal("failed preparation or switch changed HEAD")
				}
				if mode == "switch" && git("rev-parse", "refs/heads/"+branchFixtureRef) != base {
					t.Fatal("branch creation was not retained")
				}
				if injected != "" {
					if err := os.Remove(injected); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "stale-authority" {
					if f.linkRequests != 0 || git("branch", "--list", branchFixtureRef) != "" {
						t.Fatal("stale branch authority changed Git")
					}
					f.staleBinding = false
				}
				out, diag, err = run("task", "start", "PRO-123", "--branch", "--task-id", taskActionFixtureTask, "--if-match", taskActionFixtureETag, "--idempotency-key", taskActionFixtureKey)
			}
			if err != nil || out != "PRO-123  Ship now\n" || !strings.Contains(diag, "recorded the Task branch association") ||
				git("branch", "--show-current") != branchFixtureRef || git("rev-parse", "HEAD") != base ||
				f.startEffects != 1 || f.linkEffects != 1 {
				t.Fatalf("out=%q diag=%q err=%v fixture=%+v", out, diag, err, f)
			}
		})
	}
}

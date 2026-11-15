package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceCommandsListCurrentAndShow(t *testing.T) {
	server := newPreparedHTTPS(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/dev/workspace":
			_, _ = io.WriteString(response, `{"id":"11111111-1111-4111-8111-111111111111","name":"Assign","slug":"assign","role":"owner"}`)
		case "/api/v1/dev/workspaces":
			_, _ = io.WriteString(response, `{"data":[{"name":"Assign","slug":"assign","role":"owner"},{"name":"Labs","slug":"labs","role":"member"}],"next_cursor":null,"has_more":false}`)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "")
	opts := &options{host: server.URL, credentials: &memoryCredentialStore{value: storedCredential{AccessToken: "cli_at_fixture", ExpiresAt: time.Now().Add(time.Hour)}}, contexts: &fileProjectContextStore{path: filepath.Join(t.TempDir(), "context.json")}}

	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"current"}, want: "assign\n"},
		{args: []string{"show"}, want: "# Assign\n\nSlug: assign\nRole: owner\n"},
		{args: []string{"list"}, want: "assign  Assign\nlabs  Labs\n"},
	} {
		var output bytes.Buffer
		command := newWorkspaceCommand(opts, server.Client())
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(test.args)
		if err := command.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("workspace %v: %v", test.args, err)
		}
		if output.String() != test.want {
			t.Fatalf("workspace %v output = %q", test.args, output.String())
		}
	}
}

func TestWorkspaceSwitchStoresRotatedCredential(t *testing.T) {
	server := newPreparedHTTPS(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.URL.Path != "/api/v1/dev/workspace" || request.Header.Get("Authorization") != "Bearer cli_at_old" {
			t.Fatalf("request = %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(request.Body)
		if strings.TrimSpace(string(body)) != `{"slug":"labs"}` {
			t.Fatalf("body = %s", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"workspace":{"name":"Labs","slug":"labs","role":"member"},"access_token":"cli_at_new","refresh_token":"cli_rt_new","token_type":"Bearer","expires_in":900}`)
	}))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "")
	store := &memoryCredentialStore{value: storedCredential{AccessToken: "cli_at_old", RefreshToken: "cli_rt_old", ExpiresAt: time.Now().Add(time.Hour)}}
	opts := &options{host: server.URL, credentials: store, contexts: &fileProjectContextStore{path: filepath.Join(t.TempDir(), "context.json")}}
	var output bytes.Buffer
	command := newWorkspaceCommand(opts, server.Client())
	command.SetOut(&output)
	command.SetArgs([]string{"switch", "labs"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if output.String() != "labs\n" || store.value.AccessToken != "cli_at_new" || store.value.RefreshToken != "cli_rt_new" || store.saveCalls != 1 {
		t.Fatalf("switch output=%q credential=%+v saves=%d", output.String(), store.value, store.saveCalls)
	}
}

func TestWorkspaceSwitchRefusesEnvironmentTokenBeforeRequest(t *testing.T) {
	requests := 0
	server := newPreparedHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "cli_at_misconfigured")
	opts := &options{host: server.URL, credentials: &memoryCredentialStore{}, contexts: &fileProjectContextStore{path: filepath.Join(t.TempDir(), "context.json")}}
	command := newWorkspaceCommand(opts, server.Client())
	command.SetArgs([]string{"switch", "labs"})
	if err := command.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "ASSIGN_TOKEN") {
		t.Fatalf("switch err = %v", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestProjectCommandsListSwitchShowAndTasks(t *testing.T) {
	server := newPreparedHTTPS(t, canonicalNavigationTestHandler(t, "assign", "https://app.assign.so"))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	contextPath := filepath.Join(t.TempDir(), "context.json")
	opts := &options{host: server.URL, credentials: &memoryCredentialStore{}, contexts: &fileProjectContextStore{path: contextPath}}

	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"list"}, want: "PRO  Product\n"},
		{args: []string{"switch", "pro"}, want: "PRO\n"},
		{args: []string{"current"}, want: "PRO\n"},
		{args: []string{"show"}, want: "# PRO  Product\n\nPath: product\nURL: https://app.assign.so/app/assign/projects/product\nRevision: 3\n"},
		{args: []string{"tasks"}, want: "PRO-1  Ship navigation\n"},
		{args: []string{"documents"}, want: "release-plan  Release plan\n"},
	} {
		var output bytes.Buffer
		command := newProjectCommand(opts, server.Client())
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(test.args)
		if err := command.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("project %v: %v", test.args, err)
		}
		if output.String() != test.want {
			t.Fatalf("project %v output = %q", test.args, output.String())
		}
	}
}

func TestDocumentCommandsListAndShow(t *testing.T) {
	server := newPreparedHTTPS(t, canonicalNavigationTestHandler(t, "assign", "https://app.assign.so"))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	opts := &options{host: server.URL, credentials: &memoryCredentialStore{}, contexts: &fileProjectContextStore{path: filepath.Join(t.TempDir(), "context.json")}}

	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"list", "--project", "pro"}, want: "release-plan  Release plan\n"},
		{args: []string{"show", "release-plan"}, want: "# Release plan\n\nPath: release-plan\nVisibility: project\nURL: https://app.assign.so/app/assign/documents/release-plan\nRevision: 2\n\nShip on Friday.\n"},
	} {
		var output bytes.Buffer
		command := newDocumentCommand(opts, server.Client())
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(test.args)
		if err := command.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("document %v: %v", test.args, err)
		}
		if output.String() != test.want {
			t.Fatalf("document %v output = %q", test.args, output.String())
		}
	}
}

func TestProjectAndDocumentBrowserHandoff(t *testing.T) {
	server := newPreparedHTTPS(t, canonicalNavigationTestHandler(t, "acme", "https://assign.so"))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	opened := ""
	opts := &options{
		host:        server.URL,
		credentials: &memoryCredentialStore{},
		contexts:    &fileProjectContextStore{path: filepath.Join(t.TempDir(), "context.json")},
		openURL: func(target string) error {
			opened = target
			return nil
		},
	}

	var output bytes.Buffer
	project := newProjectCommand(opts, server.Client())
	project.SetOut(&output)
	project.SetArgs([]string{"url", "pro"})
	if err := project.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if output.String() != "https://assign.so/app/acme/projects/product\n" {
		t.Fatalf("project URL output = %q", output.String())
	}

	document := newDocumentCommand(opts, server.Client())
	document.SetArgs([]string{"open", "release-plan"})
	if err := document.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opened != "https://assign.so/app/acme/documents/release-plan" {
		t.Fatalf("opened URL = %q", opened)
	}
}

func TestBrowserHandoffRejectsUnsafeURLsAndPreservesSafeURLOnLaunchFailure(t *testing.T) {
	for _, target := range []string{
		"http://assign.so/acme/projects/product",
		"https://user:secret@assign.so/acme/projects/product",
		"/acme/projects/product",
	} {
		var output bytes.Buffer
		err := printOrOpenURL(&output, &options{}, target, false)
		if err == nil || !strings.Contains(err.Error(), "invalid browser URL") {
			t.Fatalf("target %q error = %v", target, err)
		}
		if output.Len() != 0 {
			t.Fatalf("unsafe target %q was printed as %q", target, output.String())
		}
	}

	var output bytes.Buffer
	safe := "https://assign.so/app/acme/projects/product"
	err := printOrOpenURL(&output, &options{openURL: func(string) error { return errors.New("unavailable") }}, safe, true)
	if err == nil || !strings.Contains(err.Error(), "open browser") {
		t.Fatalf("launch error = %v", err)
	}
	if output.String() != safe+"\n" {
		t.Fatalf("launch failure output = %q", output.String())
	}
}

func writeWorkspaceBinding(response http.ResponseWriter, id string) {
	_, _ = fmt.Fprintf(response, `{"workspace_id":%q,"authority_context":"workspace","effective_scopes":["assign:read"]}`, id)
}

func TestCanonicalReadPageContinuation(t *testing.T) {
	const workspace = "11111111-1111-4111-8111-111111111111"
	const cursor = "opaque/+cursor?=&"
	for _, tc := range []struct {
		name, path, item, row string
		args                  []string
	}{
		{"my work", "/work", `"id":"44444444-4444-4444-8444-444444444444","code":"PRO-1","title":"Ship"`, "PRO-1  Ship\n", nil},
		{"search", "/search", `"resource_id":"44444444-4444-4444-8444-444444444444","resource_type":"task","identifier":"PRO-1","title":"Ship\nnow"`, "PRO-1  Ship now\n", []string{"search", "ship"}},
		{"projects", "/projects", `"id":"44444444-4444-4444-8444-444444444444","key":"PRO","name":"Product"`, "PRO  Product\n", []string{"project", "list"}},
		{"documents", "/documents", `"id":"44444444-4444-4444-8444-444444444444","path":"release-plan","title":"Release plan"`, "release-plan  Release plan\n", []string{"document", "list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ASSIGN_TOKEN", "apt_fixture")
			var cursors []string
			base := "/api/v1/workspaces/" + workspace
			client := taskActionFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer apt_fixture" {
					t.Error("credential changed")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/auth-context":
					writeWorkspaceBinding(w, workspace)
				case base + tc.path:
					got := r.URL.Query().Get("cursor")
					cursors = append(cursors, got)
					if got == "" {
						fmt.Fprintf(w, `{"data":[{"workspace_id":%q,"revision":"7",%s}],"next_cursor":%q,"has_more":true}`, workspace, tc.item, cursor)
					} else {
						fmt.Fprint(w, `{"data":[],"next_cursor":null,"has_more":false}`)
					}
				default:
					t.Errorf("unexpected request: %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			for _, next := range []string{"", cursor} {
				var out, diagnostics bytes.Buffer
				command := newRootCommandWithClient(strings.NewReader(""), &out, &diagnostics, client)
				args := append([]string{"--host", taskActionFixtureHost}, tc.args...)
				if next != "" {
					args = append(args, "--cursor", next)
				}
				command.SetArgs(args)
				if err := command.ExecuteContext(context.Background()); err != nil {
					t.Fatal(err)
				}
				if next == "" && (out.String() != tc.row || diagnostics.String() != "assign: next cursor: "+cursor+"\n") {
					t.Fatalf("first page: stdout=%q stderr=%q", out.String(), diagnostics.String())
				}
				if next != "" && (out.Len() != 0 || diagnostics.Len() != 0) {
					t.Fatalf("last page: stdout=%q stderr=%q", out.String(), diagnostics.String())
				}
			}
			if len(cursors) != 2 || cursors[0] != "" || cursors[1] != cursor {
				t.Fatalf("continuations = %q", cursors)
			}
		})
	}
}

func TestWorkspaceListContinuesWithThePinnedGrant(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "")
	const cursor = "opaque/+cursor?=&"
	var cursors []string
	client := taskActionFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/dev/workspaces" || r.Header.Get("Authorization") != "Bearer cli_at_fixture" || r.URL.Query().Get("limit") != "100" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		got := r.URL.Query().Get("cursor")
		cursors = append(cursors, got)
		if got == "" {
			fmt.Fprintf(w, `{"data":[{"slug":"first","name":"First"}],"next_cursor":%q,"has_more":true}`, cursor)
			return
		}
		fmt.Fprint(w, `{"data":[{"slug":"second","name":"Second"}],"next_cursor":null,"has_more":false}`)
	}))
	opts := &options{host: taskActionFixtureHost, credentials: &memoryCredentialStore{value: storedCredential{AccessToken: "cli_at_fixture", ExpiresAt: time.Now().Add(time.Hour)}}}
	for _, tc := range []struct {
		args            []string
		row, diagnostic string
	}{
		{[]string{"list"}, "first  First\n", "assign: next cursor: " + cursor + "\n"},
		{[]string{"list", "--cursor", cursor}, "second  Second\n", ""},
	} {
		var out, diagnostics bytes.Buffer
		command := newWorkspaceCommand(opts, client)
		command.SetOut(&out)
		command.SetErr(&diagnostics)
		command.SetArgs(tc.args)
		if err := command.ExecuteContext(context.Background()); err != nil || out.String() != tc.row || diagnostics.String() != tc.diagnostic {
			t.Fatalf("stdout=%q stderr=%q err=%v", out.String(), diagnostics.String(), err)
		}
	}
	if len(cursors) != 2 || cursors[1] != cursor {
		t.Fatalf("continuations = %q", cursors)
	}
}

func TestWorkspaceCredentialContextStopsAtDeniedOrMismatchedAuthority(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	const workspace = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name, context, item    string
		status, exit, requests int
	}{
		{name: "denied", status: 403, exit: ExitAuthentication, requests: 1},
		{name: "unbound", context: `{"authority_context":"account","workspace_id":null}`, status: 200, exit: ExitAuthentication, requests: 1},
		{name: "mismatch", context: `{"authority_context":"workspace","workspace_id":"` + workspace + `"}`, item: `{"id":"22222222-2222-4222-8222-222222222222","slug":"other","name":"Other"}`, status: 200, exit: ExitOperation, requests: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := taskActionFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/api/v1/auth-context":
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.context)
				case "/api/v1/workspaces/" + workspace:
					fmt.Fprint(w, tc.item)
				default:
					t.Errorf("unexpected fallback or resource request %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			var out bytes.Buffer
			command := newRootCommandWithClient(strings.NewReader(""), &out, io.Discard, client)
			command.SetArgs([]string{"--host", taskActionFixtureHost, "workspace", "show"})
			err := command.ExecuteContext(context.Background())
			if exitCode(err) != tc.exit || out.Len() != 0 || calls != tc.requests {
				t.Fatalf("stdout=%q requests=%d err=%v", out.String(), calls, err)
			}
		})
	}
}

func TestDiscussRequiresTheInteractiveGrant(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_fixture")
	client := taskActionFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s", r.URL)
	}))
	opts := &options{host: taskActionFixtureHost, isTerminal: func(io.Reader, io.Writer) bool { return true }}
	err := runDiscuss(context.Background(), strings.NewReader(""), io.Discard, io.Discard, opts, client)
	if exitCode(err) != ExitAuthentication || !strings.Contains(err.Error(), "assign login") {
		t.Fatalf("expected interactive login requirement, got %v", err)
	}
}

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceCommandsListCurrentAndShow(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/cli/workspace":
			_, _ = io.WriteString(response, `{"name":"Assign","slug":"assign","role":"owner"}`)
		case "/api/v1/cli/workspaces":
			_, _ = io.WriteString(response, `{"items":[{"name":"Assign","slug":"assign","role":"owner"},{"name":"Labs","slug":"labs","role":"member"}],"next_cursor":null,"has_more":false}`)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	opts := &options{host: server.URL, credentials: &memoryCredentialStore{}, contexts: &fileProjectContextStore{path: filepath.Join(t.TempDir(), "context.json")}}

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
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.URL.Path != "/api/v1/cli/workspace" || request.Header.Get("Authorization") != "Bearer cli_at_old" {
			t.Fatalf("request = %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(request.Body)
		if strings.TrimSpace(string(body)) != `{"slug":"labs"}` {
			t.Fatalf("body = %s", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"workspace":{"name":"Labs","slug":"labs","role":"member"},"access_token":"cli_at_new","refresh_token":"cli_rt_new","expires_in":900}`)
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
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
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
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/cli/workspace":
			_, _ = io.WriteString(response, `{"name":"Assign","slug":"assign","role":"owner"}`)
		case "/api/v1/cli/projects":
			_, _ = io.WriteString(response, `{"items":[{"code":"PRO","name":"Product","path":"product","revision":3,"url":"https://app.assign.so/app/assign/projects/product"}],"next_cursor":null,"has_more":false}`)
		case "/api/v1/cli/projects/PRO":
			_, _ = io.WriteString(response, `{"code":"PRO","name":"Product","path":"product","revision":3,"url":"https://app.assign.so/app/assign/projects/product"}`)
		case "/api/v1/cli/projects/PRO/tasks":
			_, _ = io.WriteString(response, `{"items":[{"code":"PRO-1","title":"Ship navigation","status_category":"todo"}],"next_cursor":null,"has_more":false}`)
		case "/api/v1/cli/documents":
			if request.URL.Query().Get("project_code") != "PRO" {
				t.Fatalf("project_code = %q", request.URL.Query().Get("project_code"))
			}
			_, _ = io.WriteString(response, `{"items":[{"path":"release-plan","title":"Release plan","visibility":"project","revision":2,"url":"https://app.assign.so/app/assign/documents/release-plan"}],"next_cursor":null,"has_more":false}`)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
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
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/cli/documents":
			if request.URL.Query().Get("project_code") != "PRO" || request.URL.Query().Get("limit") != "100" {
				t.Fatalf("query = %s", request.URL.RawQuery)
			}
			_, _ = io.WriteString(response, `{"items":[{"path":"release-plan","title":"Release plan","visibility":"project","revision":2,"url":"https://app.assign.so/app/assign/documents/release-plan"}],"next_cursor":null,"has_more":false}`)
		case "/api/v1/cli/documents/release-plan":
			_, _ = io.WriteString(response, `{"path":"release-plan","title":"Release plan","visibility":"project","revision":2,"url":"https://app.assign.so/app/assign/documents/release-plan","body":"Ship on Friday."}`)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
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
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/cli/projects/PRO":
			_, _ = io.WriteString(response, `{"code":"PRO","name":"Product","path":"product","revision":3,"url":"https://assign.so/acme/projects/product"}`)
		case "/api/v1/cli/documents/release-plan":
			_, _ = io.WriteString(response, `{"path":"release-plan","title":"Release plan","visibility":"project","revision":2,"url":"https://assign.so/acme/documents/release-plan"}`)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
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
	if output.String() != "https://assign.so/acme/projects/product\n" {
		t.Fatalf("project URL output = %q", output.String())
	}

	document := newDocumentCommand(opts, server.Client())
	document.SetArgs([]string{"open", "release-plan"})
	if err := document.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opened != "https://assign.so/acme/documents/release-plan" {
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
	safe := "https://assign.so/acme/projects/product"
	err := printOrOpenURL(&output, &options{openURL: func(string) error { return errors.New("unavailable") }}, safe, true)
	if err == nil || !strings.Contains(err.Error(), "open browser") {
		t.Fatalf("launch error = %v", err)
	}
	if output.String() != safe+"\n" {
		t.Fatalf("launch failure output = %q", output.String())
	}
}

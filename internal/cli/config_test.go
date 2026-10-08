package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIConfigPersistsHostAndExplicitFlagWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assign", "settings.json")
	t.Setenv("ASSIGN_CONFIG_FILE", path)
	run := func(args ...string) string {
		t.Helper()
		var output bytes.Buffer
		root := newRootCommand(strings.NewReader(""), &output, &output)
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}
	if output := run("config", "show"); !strings.Contains(output, "API host: "+defaultHost+"\nHost source: default") {
		t.Fatal(output)
	}
	run("config", "set", "host", "https://api.example.test/")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private settings file: %v %v", info, err)
	}
	if output := run("config"); !strings.Contains(output, "API host: https://api.example.test\nHost source: stored") {
		t.Fatal(output)
	}
	if output := run("--host", "https://override.example.test/", "config", "show"); !strings.Contains(output, "API host: https://override.example.test\nHost source: flag") {
		t.Fatal(output)
	}
	if output := run("config"); strings.Contains(output, "override.example.test") {
		t.Fatal("command override was persisted")
	}
	run("config", "unset", "host")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reset: %v", err)
	}
	run("config", "unset", "host")
}

func TestCLIConfigRejectsUnsafeHostsAndUntrustedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := &fileCLISettingsStore{path: path}
	for _, host := range []string{"http://api.example.test", "https://token@api.example.test", "https://api.example.test/path", "https://api.example.test?token=secret"} {
		if err := store.SaveHost(host); err == nil {
			t.Fatalf("accepted unsafe host %q", host)
		}
	}
	for _, body := range []string{`{"schema":2,"host":"https://example.test"}`, `{"schema":1,"host":"http://example.test"}`, `{"schema":1,"host":"https://example.test","token":"never-display"}`, `{"schema":1,"host":"https://example.test"} {}`, strings.Repeat("x", 4097)} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadHost(); err == nil || strings.Contains(err.Error(), "never-display") {
			t.Fatalf("invalid settings error: %v", err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"schema":1,"host":"https://example.test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadHost(); err == nil {
		t.Fatal("accepted public settings file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadHost(); err == nil {
		t.Fatal("read symlink")
	}
	if err := store.SaveHost("https://example.test"); err == nil {
		t.Fatal("replaced symlink")
	}
	if err := store.ResetHost(); err == nil {
		t.Fatal("removed symlink")
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "preserve me" {
		t.Fatalf("target was changed: %v", err)
	}
}

func TestCLIConfigMalformedFileCanBeRepairedWithoutCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	t.Setenv("ASSIGN_CONFIG_FILE", path)
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"version"}, {"config", "set", "host", "https://example.test"}, {"config", "show"}} {
		var output bytes.Buffer
		root := newRootCommand(strings.NewReader(""), &output, &output)
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestInitUsesStoredHostAndSavesOnlyAuthorizedProject(t *testing.T) {
	denied := false
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("Authorization") != "Bearer apt_test" {
			t.Error("missing test credential")
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/cli/projects/ASG":
			if denied {
				response.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(response, `{"code":"ASG","name":"Assign","path":"assign","url":"https://assign.example.test/projects/assign","revision":1}`)
		case "/api/v1/dev/workspace":
			_, _ = io.WriteString(response, `{"name":"Assign","slug":"assign","role":"owner"}`)
		default:
			t.Errorf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	settingsPath, contextPath := filepath.Join(directory, "settings.json"), filepath.Join(directory, "context.json")
	t.Setenv("ASSIGN_CONFIG_FILE", settingsPath)
	t.Setenv("ASSIGN_CONTEXT_FILE", contextPath)
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	if err := (&fileCLISettingsStore{path: settingsPath}).SaveHost(server.URL); err != nil {
		t.Fatal(err)
	}
	run := func() (string, error) {
		var output bytes.Buffer
		root := newRootCommandWithClient(strings.NewReader(""), &output, &output, server.Client())
		root.SetArgs([]string{"init", "asg"})
		err := root.ExecuteContext(context.Background())
		return output.String(), err
	}
	if output, err := run(); err != nil || output != "ASG\n" {
		t.Fatalf("init: %q %v", output, err)
	}
	store := &fileProjectContextStore{path: contextPath}
	if project, err := store.LoadProject(server.URL, "assign"); err != nil || project != "ASG" {
		t.Fatalf("selection: %s %v", project, err)
	}
	if _, err := store.LoadProject(defaultHost, "assign"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host isolation: %v", err)
	}
	before, _ := os.ReadFile(contextPath)
	denied = true
	if output, err := run(); err == nil || output != "" {
		t.Fatalf("denied init: %q %v", output, err)
	}
	after, _ := os.ReadFile(contextPath)
	if !bytes.Equal(before, after) {
		t.Fatal("denied Project changed stored selection")
	}
	if requests != 3 {
		t.Fatalf("expected two successful reads and one denial, got %d", requests)
	}
}

func TestRelatedTaskLookupReusesBoundedContextAndOmissionWarnings(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/cli/tasks/ASG-362/context" || request.URL.Query().Get("profile") != "compact" {
			t.Errorf("unexpected request %s", request.URL)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"task_code":"ASG-362","profile":"compact","markdown":"# Task ASG-362\n\n## Relations\n\n- related ASG-234\n","truncated":true,"omissions":["relations: more available"]}`)
	}))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	t.Setenv("ASSIGN_CONFIG_FILE", filepath.Join(t.TempDir(), "absent.json"))
	var output, diagnostics bytes.Buffer
	root := newRootCommandWithClient(strings.NewReader(""), &output, &diagnostics, server.Client())
	root.SetArgs([]string{"--host", server.URL, "task", "related", "asg-362"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "related ASG-234") || !strings.Contains(diagnostics.String(), "truncated") || !strings.Contains(diagnostics.String(), "relations: more available") {
		t.Fatalf("output=%q diagnostics=%q", output.String(), diagnostics.String())
	}
}

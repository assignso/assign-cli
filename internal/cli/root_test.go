package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestVersionCommands(t *testing.T) {
	code, stdout, stderr := run(t, "version")
	if code != ExitOK || stdout != "assign dev\ncommit none\nbuilt unknown\n" || stderr != "" {
		t.Fatalf("version result = (%d, %q, %q)", code, stdout, stderr)
	}

	code, stdout, stderr = run(t, "--version")
	if code != ExitOK || stdout != "assign version dev\n" || stderr != "" {
		t.Fatalf("--version result = (%d, %q, %q)", code, stdout, stderr)
	}
}

func TestUpdateCheckSelectsTheCorrectReleaseChannel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("User-Agent") != "assign-cli/1.0.0-rc.1" {
			t.Fatalf("user agent = %q", request.Header.Get("User-Agent"))
		}
		_, _ = response.Write([]byte(`[{"tag_name":"1.0.0-rc.3","prerelease":true},{"tag_name":"1.0.0","prerelease":false}]`))
	}))
	defer server.Close()
	var output bytes.Buffer
	if err := runUpdateCheck(context.Background(), &output, server.Client(), server.URL, "1.0.0-rc.1"); err != nil {
		t.Fatalf("check: %v", err)
	}
	if output.String() != "assign 1.0.0 is available (installed 1.0.0-rc.1)\nRun: assign update install\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestUpdateCheckKeepsStableClientsOnStableChannel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`[{"tag_name":"2.0.0-rc.1","prerelease":true},{"tag_name":"1.0.1","prerelease":false}]`))
	}))
	defer server.Close()
	var output bytes.Buffer
	if err := runUpdateCheck(context.Background(), &output, server.Client(), server.URL, "1.0.0"); err != nil {
		t.Fatalf("check: %v", err)
	}
	if output.String() != "assign 1.0.1 is available (installed 1.0.0)\nRun: assign update install\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestSemverPrereleaseOrdering(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"1.0.0-rc.10", "1.0.0-rc.2", 1},
		{"1.0.0-rc.2", "1.0.0-rc.beta", -1},
		{"1.0.0-rc.2.more", "1.0.0-rc.2", 1},
	} {
		left, _ := parseSemver(test.left)
		right, _ := parseSemver(test.right)
		if got := compareSemver(left, right); got != test.want {
			t.Fatalf("compareSemver(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestUpdateInstallPrintsReleaseInstallerEndpoint(t *testing.T) {
	var output bytes.Buffer
	if err := runUpdateInstall(&output); err != nil {
		t.Fatalf("install guidance: %v", err)
	}
	if output.String() != "curl https://assign.so/install.sh | sh\n" {
		t.Fatalf("install guidance = %q", output.String())
	}
}

func TestCommandAliases(t *testing.T) {
	for _, test := range []struct {
		alias    string
		contains string
	}{
		{alias: "v", contains: "assign dev"},
		{alias: "completions", contains: "compdef"},
		{alias: "diag", contains: "credential ok ASSIGN_TOKEN"},
		{alias: "diagnose", contains: "credential ok ASSIGN_TOKEN"},
	} {
		t.Run(test.alias, func(t *testing.T) {
			t.Setenv("ASSIGN_TOKEN", "test-token")
			args := []string{test.alias}
			if test.alias == "completions" {
				args = append(args, "zsh")
			}
			code, stdout, stderr := run(t, args...)
			if code != ExitOK || !strings.Contains(stdout, test.contains) || stderr != "" {
				t.Fatalf("alias %q result = (%d, %q, %q)", test.alias, code, stdout, stderr)
			}
		})
	}
}

func TestAliasesAreDiscoverable(t *testing.T) {
	code, stdout, stderr := run(t, "aliases")
	if code != ExitOK || stderr != "" {
		t.Fatalf("aliases result = (%d, %q, %q)", code, stdout, stderr)
	}
	for _, mapping := range []string{"signin  login", "find  search", "t  task", "ws  workspace", "p  project", "doc  document", "diag, diagnose  doctor"} {
		if !strings.Contains(stdout, mapping) {
			t.Fatalf("aliases output missing %q: %q", mapping, stdout)
		}
	}

	code, stdout, stderr = run(t, "task", "--help")
	if code != ExitOK || stderr != "" || !strings.Contains(stdout, "Aliases:") || !strings.Contains(stdout, "task, t") {
		t.Fatalf("task help result = (%d, %q, %q)", code, stdout, stderr)
	}
	for _, resource := range []struct{ command, alias string }{{"workspace", "ws"}, {"project", "p"}, {"document", "doc"}} {
		code, stdout, stderr = run(t, resource.command, "--help")
		if code != ExitOK || stderr != "" || !strings.Contains(stdout, resource.command+", "+resource.alias) {
			t.Fatalf("%s help result = (%d, %q, %q)", resource.command, code, stdout, stderr)
		}
	}
}

func TestEveryRegisteredAliasResolvesToCanonicalCommand(t *testing.T) {
	root := newRootCommand(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	for _, definition := range commandAliases {
		for _, alias := range definition.aliases {
			t.Run(alias, func(t *testing.T) {
				command, _, err := root.Find([]string{alias})
				if err != nil {
					t.Fatalf("find alias %q: %v", alias, err)
				}
				if command.Name() != definition.command {
					t.Fatalf("alias %q resolved to %q, want %q", alias, command.Name(), definition.command)
				}
			})
		}
	}
}

func TestShellCompletionIncludesAliases(t *testing.T) {
	root := newRootCommand(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	completions, directive := root.ValidArgsFunction(root, nil, "si")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("completion directive = %v", directive)
	}
	if len(completions) != 2 || completions[0] != "signin\tAlias for login" || completions[1] != "signout\tAlias for logout" {
		t.Fatalf("alias completions = %#v", completions)
	}
}

func TestBareCommandRequiresPersonalAPIToken(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "")
	code, stdout, stderr := run(t)
	if code != ExitAuthentication || stdout != "" || !strings.Contains(stderr, "set ASSIGN_TOKEN") {
		t.Fatalf("bare result = (%d, %q, %q)", code, stdout, stderr)
	}
}

func TestBareCommandListsMyWorkWithBearerToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/cli/my-work" || request.Header.Get("Authorization") != "Bearer apt_test" {
			t.Fatalf("request = %s %q", request.URL.Path, request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"items":[{"code":"PRO-123","title":"Ship the slice","status_category":"started"}],"next_cursor":null,"has_more":false}`))
	}))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	var stdout, stderr bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(""), &stdout, &stderr, server.Client())
	command.SetArgs([]string{"--host", server.URL})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stdout.String() != "PRO-123  Ship the slice\n" || stderr.String() != "" {
		t.Fatalf("output = (%q, %q)", stdout.String(), stderr.String())
	}
}

func TestSearchUsesBearerAndRendersCode(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/cli/search" || request.URL.Query().Get("q") != "ship now" || request.Header.Get("Authorization") != "Bearer apt_test" {
			t.Fatalf("request = %s %q %q", request.URL.String(), request.URL.Query().Get("q"), request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"items":[{"resource_type":"task","code":"PRO-123","title":"Ship now","url":"https://app.assign.so/app/acme"}],"next_cursor":null,"has_more":false}`))
	}))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	var stdout, stderr bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(""), &stdout, &stderr, server.Client())
	command.SetArgs([]string{"--host", server.URL, "search", "ship now"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stdout.String() != "PRO-123  Ship now\n" || stderr.String() != "" {
		t.Fatalf("output = (%q, %q)", stdout.String(), stderr.String())
	}
}

func TestFindAliasUsesSearch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/cli/search" || request.URL.Query().Get("q") != "ship now" {
			t.Fatalf("request = %s", request.URL.String())
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"items":[{"resource_type":"task","code":"PRO-123","title":"Ship now"}],"next_cursor":null,"has_more":false}`))
	}))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	var stdout, stderr bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(""), &stdout, &stderr, server.Client())
	command.SetArgs([]string{"--host", server.URL, "find", "ship now"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stdout.String() != "PRO-123  Ship now\n" || stderr.String() != "" {
		t.Fatalf("output = (%q, %q)", stdout.String(), stderr.String())
	}
}

func TestTaskAliasUsesNamespacedAction(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/cli/tasks/PRO-123/done" || request.Header.Get("If-Match") != `"7"` {
			t.Fatalf("request = %s If-Match=%q", request.URL.Path, request.Header.Get("If-Match"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"task":{"code":"PRO-123","title":"Ship now"}}`))
	}))
	defer server.Close()
	t.Setenv("ASSIGN_TOKEN", "apt_test")
	var stdout, stderr bytes.Buffer
	command := newRootCommandWithClient(strings.NewReader(""), &stdout, &stderr, server.Client())
	command.SetArgs([]string{"--host", server.URL, "t", "done", "PRO-123", "--revision", "7"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stdout.String() != "PRO-123  Ship now\n" || stderr.String() != "" {
		t.Fatalf("output = (%q, %q)", stdout.String(), stderr.String())
	}
}

func TestHostMustBeHTTPSOrigin(t *testing.T) {
	for _, host := range []string{"http://api.assign.so", "https://api.assign.so/path", "https://user@example.com"} {
		code, _, stderr := run(t, "--host", host, "version")
		if code != ExitArguments || !strings.Contains(stderr, "--host") {
			t.Fatalf("host %q result = (%d, %q)", host, code, stderr)
		}
	}
}

func TestCompletionSupportsLaunchShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		code, stdout, stderr := run(t, "completion", shell)
		if code != ExitOK || stdout == "" || stderr != "" {
			t.Fatalf("completion %s result = (%d, %d bytes, %q)", shell, code, len(stdout), stderr)
		}
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	code, _, stderr := run(t, "completion", "powershell")
	if code != ExitArguments || !strings.Contains(stderr, "unsupported shell") {
		t.Fatalf("completion result = (%d, %q)", code, stderr)
	}
}

func TestArgumentErrorsUseStableExitCode(t *testing.T) {
	for _, args := range [][]string{{"missing"}, {"completion"}, {"version", "extra"}, {"--unknown"}} {
		code, _, stderr := run(t, args...)
		if code != ExitArguments || stderr == "" {
			t.Fatalf("arguments %q result = (%d, %q)", args, code, stderr)
		}
	}
}

func TestDoctorDoesNotPrintToken(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "super-secret")
	code, stdout, stderr := run(t, "doctor")
	if code != ExitOK || stderr != "" || strings.Contains(stdout, "super-secret") {
		t.Fatalf("doctor result = (%d, %q, %q)", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "credential ok ASSIGN_TOKEN") {
		t.Fatalf("doctor output missing credential source: %q", stdout)
	}
}

func TestDoctorRequiresAuthentication(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "")
	code, stdout, stderr := run(t, "doctor")
	if code != ExitAuthentication || !strings.Contains(stdout, "credential failed") || !strings.Contains(stderr, "authentication is not configured") {
		t.Fatalf("doctor result = (%d, %q, %q)", code, stdout, stderr)
	}
}

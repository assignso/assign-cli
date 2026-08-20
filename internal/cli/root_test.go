package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
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

func TestBareCommandReportsPendingHomeContract(t *testing.T) {
	code, stdout, stderr := run(t)
	if code != ExitOperation || stdout != "" || !strings.Contains(stderr, "bounded My Work API contract") {
		t.Fatalf("bare result = (%d, %q, %q)", code, stdout, stderr)
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

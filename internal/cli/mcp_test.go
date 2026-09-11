package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recordedCommand struct {
	name string
	args []string
}

func TestMCPSetupCommandUsesStoredInteractiveLogin(t *testing.T) {
	store := &memoryCredentialStore{value: storedCredential{
		AccessToken: "cli-access", RefreshToken: "cli-refresh", ExpiresAt: time.Now().Add(time.Hour),
	}}
	var commands []recordedCommand
	runner := func(_ context.Context, _ io.Reader, output, _ io.Writer, name string, args ...string) error {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		if strings.Join(args, " ") == "mcp get assign --json" {
			_, _ = io.WriteString(output, `{"enabled":true,"transport":{"type":"streamable_http","url":"https://mcp.assign.so/","bearer_token_env_var":null}}`)
		}
		return nil
	}
	var output, diagnostic bytes.Buffer
	command := newMCPCommand(&options{host: defaultHost, credentials: store}, http.DefaultClient, runner)
	command.SetOut(&output)
	command.SetErr(&diagnostic)
	command.SetIn(strings.NewReader(""))
	command.SetArgs([]string{"setup", "codex", "--scopes", "assign:read"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if diagnostic.String() != "" {
		t.Fatalf("diagnostic = %q", diagnostic.String())
	}
	want := []recordedCommand{
		{name: "codex", args: []string{"mcp", "get", "assign", "--json"}},
		{name: "codex", args: []string{"mcp", "login", "assign", "--scopes", "assign:read", "--oauth-client-registration", assignMCPOAuthRegistration}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if strings.Contains(strings.Join(commands[1].args, " "), "cli-access") || strings.Contains(strings.Join(commands[1].args, " "), "cli-refresh") {
		t.Fatalf("Codex arguments contain CLI credential material: %#v", commands)
	}
}

func TestMCPSetupAddsAndAuthorizesCodexWithoutSharingCLIToken(t *testing.T) {
	var commands []recordedCommand
	loginChecks := 0
	runner := func(_ context.Context, _ io.Reader, output, diagnostic io.Writer, name string, args ...string) error {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		switch strings.Join(args, " ") {
		case "mcp get assign --json":
			_, _ = io.WriteString(diagnostic, "Error: No MCP server named 'assign' found.\n")
			return errors.New("exit status 1")
		case "mcp add assign --url https://mcp.assign.so/ --oauth-resource https://mcp.assign.so/ --oauth-client-registration cimd":
			_, _ = io.WriteString(output, "Added global MCP server 'assign'.\n")
			return nil
		case "mcp login assign --scopes assign:read,assign:write --oauth-client-registration cimd":
			_, _ = io.WriteString(output, "Successfully logged in.\n")
			return nil
		default:
			return errors.New("unexpected command")
		}
	}
	var output, diagnostic bytes.Buffer
	err := runMCPSetup(context.Background(), strings.NewReader(""), &output, &diagnostic, defaultScopes, func(context.Context) error {
		loginChecks++
		return nil
	}, runner)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if loginChecks != 1 {
		t.Fatalf("login checks = %d", loginChecks)
	}
	want := []recordedCommand{
		{name: "codex", args: []string{"mcp", "get", "assign", "--json"}},
		{name: "codex", args: []string{"mcp", "add", "assign", "--url", assignMCPURL, "--oauth-resource", assignMCPURL, "--oauth-client-registration", assignMCPOAuthRegistration}},
		{name: "codex", args: []string{"mcp", "login", "assign", "--scopes", defaultScopes, "--oauth-client-registration", assignMCPOAuthRegistration}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if strings.Contains(output.String(), "access_token") || strings.Contains(output.String(), "refresh_token") {
		t.Fatalf("output contains token material: %q", output.String())
	}
	if !strings.Contains(output.String(), "separate, revocable MCP credential") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestMCPSetupKeepsCompatibleCodexConfiguration(t *testing.T) {
	var commands []recordedCommand
	runner := func(_ context.Context, _ io.Reader, output, _ io.Writer, name string, args ...string) error {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		if strings.Join(args, " ") == "mcp get assign --json" {
			_, _ = io.WriteString(output, `{"enabled":true,"transport":{"type":"streamable_http","url":"https://mcp.assign.so/","bearer_token_env_var":null}}`)
		}
		return nil
	}
	var output bytes.Buffer
	if err := runMCPSetup(context.Background(), strings.NewReader(""), &output, io.Discard, "assign:read", func(context.Context) error { return nil }, runner); err != nil {
		t.Fatalf("setup: %v", err)
	}
	want := []recordedCommand{
		{name: "codex", args: []string{"mcp", "get", "assign", "--json"}},
		{name: "codex", args: []string{"mcp", "login", "assign", "--scopes", "assign:read", "--oauth-client-registration", assignMCPOAuthRegistration}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestMCPSetupRefusesIncompatibleCodexConfiguration(t *testing.T) {
	configurations := map[string]string{
		"different URL": `{"enabled":true,"transport":{"type":"streamable_http","url":"https://example.com/mcp","bearer_token_env_var":null}}`,
		"stdio":         `{"enabled":true,"transport":{"type":"stdio"}}`,
		"bearer env":    `{"enabled":true,"transport":{"type":"streamable_http","url":"https://mcp.assign.so/","bearer_token_env_var":"ASSIGN_MCP_TOKEN"}}`,
		"disabled":      `{"enabled":false,"transport":{"type":"streamable_http","url":"https://mcp.assign.so/","bearer_token_env_var":null}}`,
	}
	for name, configuration := range configurations {
		t.Run(name, func(t *testing.T) {
			runner := func(_ context.Context, _ io.Reader, output, _ io.Writer, _ string, _ ...string) error {
				_, _ = io.WriteString(output, configuration)
				return nil
			}
			loginChecks := 0
			err := runMCPSetup(context.Background(), strings.NewReader(""), io.Discard, io.Discard, defaultScopes, func(context.Context) error {
				loginChecks++
				return nil
			}, runner)
			if exitCode(err) != ExitConflict || !strings.Contains(err.Error(), "incompatible MCP server") {
				t.Fatalf("error = %v", err)
			}
			if loginChecks != 0 {
				t.Fatalf("login checks = %d", loginChecks)
			}
		})
	}
}

func TestMCPSetupValidatesScopesAndReportsMissingCodex(t *testing.T) {
	if _, err := canonicalMCPScopes("assign:write"); exitCode(err) != ExitArguments {
		t.Fatalf("scope error = %v", err)
	}
	runner := func(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error {
		return &exec.Error{Name: "codex", Err: exec.ErrNotFound}
	}
	err := runMCPSetup(context.Background(), strings.NewReader(""), io.Discard, io.Discard, defaultScopes, func(context.Context) error { return nil }, runner)
	if exitCode(err) != ExitOperation || !strings.Contains(err.Error(), "Codex CLI is not installed") {
		t.Fatalf("error = %v", err)
	}
}

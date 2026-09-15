package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestMCPSetupAddsCodexStdioServer(t *testing.T) {
	var commands []recordedCommand
	runner := func(_ context.Context, _ io.Reader, output, diagnostic io.Writer, name string, args ...string) error {
		commands = append(commands, recordedCommand{name, append([]string(nil), args...)})
		switch strings.Join(args, " ") {
		case "mcp get assign --json":
			_, _ = io.WriteString(diagnostic, "Error: No MCP server named 'assign' found.\n")
			return errors.New("exit status 1")
		case "mcp add assign -- assign mcp serve":
			return nil
		default:
			return errors.New("unexpected command")
		}
	}
	checks := 0
	var output bytes.Buffer
	err := runMCPSetup(context.Background(), strings.NewReader(""), &output, io.Discard, func(context.Context) error { checks++; return nil }, runner)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	want := []recordedCommand{{"codex", []string{"mcp", "get", "assign", "--json"}}, {"codex", []string{"mcp", "add", "assign", "--", "assign", "mcp", "serve"}}}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if checks != 1 || !strings.Contains(output.String(), "existing CLI authentication") {
		t.Fatalf("checks=%d output=%q", checks, output.String())
	}
}

func TestMCPSetupKeepsCompatibleStdioConfiguration(t *testing.T) {
	var commands []recordedCommand
	runner := func(_ context.Context, _ io.Reader, output, _ io.Writer, name string, args ...string) error {
		commands = append(commands, recordedCommand{name, append([]string(nil), args...)})
		_, _ = io.WriteString(output, `{"enabled":true,"transport":{"type":"stdio","command":"assign","args":["mcp","serve"]}}`)
		return nil
	}
	if err := runMCPSetup(context.Background(), strings.NewReader(""), io.Discard, io.Discard, func(context.Context) error { return nil }, runner); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestMCPSetupRefusesIncompatibleConfiguration(t *testing.T) {
	for name, configuration := range map[string]string{
		"remote":        `{"enabled":true,"transport":{"type":"streamable_http","url":"https://mcp.assign.so/"}}`,
		"other command": `{"enabled":true,"transport":{"type":"stdio","command":"other","args":["mcp","serve"]}}`,
		"disabled":      `{"enabled":false,"transport":{"type":"stdio","command":"assign","args":["mcp","serve"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			runner := func(_ context.Context, _ io.Reader, output, _ io.Writer, _ string, _ ...string) error {
				_, _ = io.WriteString(output, configuration)
				return nil
			}
			err := runMCPSetup(context.Background(), strings.NewReader(""), io.Discard, io.Discard, func(context.Context) error { t.Fatal("login should not run"); return nil }, runner)
			if exitCode(err) != ExitConflict {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMCPStdioExchangesCLIAuthAndForwardsJSON(t *testing.T) {
	store := &memoryCredentialStore{value: storedCredential{AccessToken: "cli-access", RefreshToken: "cli-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli/mcp/token" || r.Header.Get("Authorization") != "Bearer cli-access" {
			t.Fatalf("exchange request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"access_token":"mcp-access","token_type":"Bearer","expires_in":900}`)
	}))
	defer api.Close()
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mcp-access" {
			t.Fatalf("MCP auth=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	}))
	defer mcp.Close()
	var output bytes.Buffer
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n")
	if err := runMCPStdio(context.Background(), input, &output, io.Discard, api.URL, mcp.URL, api.Client(), store); err != nil {
		t.Fatal(err)
	}
	if output.String() != "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[]}}\n" {
		t.Fatalf("output=%q", output.String())
	}
}

func TestMCPSetupReportsMissingCodex(t *testing.T) {
	runner := func(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error {
		return &exec.Error{Name: "codex", Err: exec.ErrNotFound}
	}
	err := runMCPSetup(context.Background(), strings.NewReader(""), io.Discard, io.Discard, func(context.Context) error { return nil }, runner)
	if exitCode(err) != ExitOperation || !strings.Contains(err.Error(), "Codex CLI is not installed") {
		t.Fatalf("error = %v", err)
	}
}

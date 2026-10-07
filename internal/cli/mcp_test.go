package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type recordedCommand struct {
	name string
	args []string
}

func TestMCPSetupPreservesRemoteDefaultAndAddsExplicitStdio(t *testing.T) {
	for _, tc := range []struct {
		name, transport string
		want            []recordedCommand
	}{
		{"remote", "remote", []recordedCommand{{"codex", []string{"mcp", "get", "assign", "--json"}}, {"codex", []string{"mcp", "add", "assign", "--url", assignMCPURL, "--oauth-resource", assignMCPURL, "--oauth-client-registration", "cimd"}}, {"codex", []string{"mcp", "login", "assign", "--scopes", defaultScopes, "--oauth-client-registration", "cimd"}}}},
		{"stdio read only", "stdio", []recordedCommand{{"codex", []string{"mcp", "get", "assign", "--json"}}, {"codex", []string{"mcp", "add", "assign", "--", "assign", "mcp", "serve", "--scopes", "assign:read"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []recordedCommand
			runner := func(_ context.Context, _ io.Reader, _ io.Writer, diagnostic io.Writer, name string, args ...string) error {
				got = append(got, recordedCommand{name, append([]string(nil), args...)})
				if strings.Join(args, " ") == "mcp get assign --json" {
					_, _ = io.WriteString(diagnostic, "No MCP server named 'assign' found")
					return errors.New("missing")
				}
				return nil
			}
			scopes := defaultScopes
			if tc.transport == "stdio" {
				scopes = "assign:read"
			}
			if err := runMCPSetup(context.Background(), strings.NewReader(""), io.Discard, io.Discard, tc.transport, scopes, func(context.Context) error { return nil }, runner); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("commands=%#v want=%#v", got, tc.want)
			}
		})
	}
}

func TestMCPSetupRefusesConflictingTransport(t *testing.T) {
	runner := func(_ context.Context, _ io.Reader, output, _ io.Writer, _ string, _ ...string) error {
		_, _ = io.WriteString(output, `{"enabled":true,"transport":{"type":"streamable_http","url":"https://mcp.assign.so/"}}`)
		return nil
	}
	err := runMCPSetup(context.Background(), strings.NewReader(""), io.Discard, io.Discard, "stdio", "assign:read", func(context.Context) error { t.Fatal("login must not run"); return nil }, runner)
	if exitCode(err) != ExitConflict {
		t.Fatalf("error=%v", err)
	}
}

func TestMCPStdioStreamsSSEAndRetainsSessionHeaders(t *testing.T) {
	store := &memoryCredentialStore{value: storedCredential{AccessToken: "cli-access", RefreshToken: "cli-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cli-access" {
			t.Fatalf("API auth=%q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"access_token":"mcp-access","token_type":"Bearer","scopes":["assign:read"],"expires_in":900,"workspace_id":"11111111-1111-1111-1111-111111111111"}`)
	}))
	defer api.Close()
	var calls atomic.Int32
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer mcp-access" {
			t.Fatalf("MCP auth=%q", r.Header.Get("Authorization"))
		}
		if n == 1 {
			w.Header().Set("Mcp-Session-Id", "session-1")
		} else if r.Header.Get("Mcp-Session-Id") != "session-1" || r.Header.Get("MCP-Protocol-Version") != "2025-11-25" {
			t.Fatalf("headers=%v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\ndata: \"id\":"+string(rune('0'+n))+",\"result\":{}}\n\n")
	}))
	defer mcp.Close()
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\"}}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
	var output bytes.Buffer
	if err := runMCPStdio(context.Background(), input, &output, io.Discard, api.URL, mcp.URL, api.Client(), store, []string{"assign:read"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || strings.Count(output.String(), "\n") != 2 {
		t.Fatalf("calls=%d output=%q", calls.Load(), output.String())
	}
}

func TestMCPStdioRejectsRedirectAndOversizedInput(t *testing.T) {
	if err := runMCPStdio(context.Background(), strings.NewReader("{}\n"), io.Discard, io.Discard, "http://localhost", "http://example.com", http.DefaultClient, &memoryCredentialStore{}, []string{"assign:read"}); exitCode(err) != ExitArguments {
		t.Fatalf("endpoint error=%v", err)
	}
	input := strings.NewReader("{\"value\":\"" + strings.Repeat("x", mcpMessageLimit) + "\"}\n")
	err := runMCPStdio(context.Background(), input, io.Discard, io.Discard, "http://localhost", "http://localhost", http.DefaultClient, &memoryCredentialStore{}, []string{"assign:read"})
	if err == nil {
		t.Fatal("expected scanner limit")
	}
}

func TestMCPStdioCancellationInterruptsActiveUpstreamRequest(t *testing.T) {
	store := &memoryCredentialStore{value: storedCredential{AccessToken: "cli-access", RefreshToken: "cli-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"access_token":"mcp-access","token_type":"Bearer","scopes":["assign:read"],"expires_in":900,"workspace_id":"11111111-1111-1111-1111-111111111111"}`)
	}))
	defer api.Close()
	initialized, active, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope mcpEnvelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decode upstream request: %v", err)
			return
		}
		switch envelope.Method {
		case "initialize":
			close(initialized)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		case "tools/call":
			close(active)
			<-r.Context().Done()
			close(cancelled)
		case "notifications/cancelled":
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected method %q", envelope.Method)
		}
	}))
	defer mcp.Close()
	inputReader, inputWriter := io.Pipe()
	result := make(chan error, 1)
	go func() {
		result <- runMCPStdio(context.Background(), inputReader, io.Discard, io.Discard, api.URL, mcp.URL, api.Client(), store, []string{"assign:read"})
	}()
	_, _ = io.WriteString(inputWriter, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`+"\n")
	waitForSignal(t, initialized, "initialize")
	_, _ = io.WriteString(inputWriter, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{}}`+"\n")
	waitForSignal(t, active, "active call")
	_, _ = io.WriteString(inputWriter, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}`+"\n")
	waitForSignal(t, cancelled, "upstream cancellation")
	_ = inputWriter.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("bridge after cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not shut down after cancellation")
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func TestMCPSetupReportsMissingCodex(t *testing.T) {
	runner := func(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error {
		return &exec.Error{Name: "codex", Err: exec.ErrNotFound}
	}
	err := runMCPSetup(context.Background(), strings.NewReader(""), io.Discard, io.Discard, "remote", defaultScopes, func(context.Context) error { return nil }, runner)
	if exitCode(err) != ExitOperation || !strings.Contains(err.Error(), "Codex CLI is not installed") {
		t.Fatalf("error=%v", err)
	}
}

func TestMCPStdioForwardsEventsWithoutLegacyInitialization(t *testing.T) {
	store := &memoryCredentialStore{value: storedCredential{AccessToken: "prepared-cli", RefreshToken: "prepared-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"access_token":"prepared-mcp","token_type":"Bearer","scopes":["assign:read"],"expires_in":900,"workspace_id":"11111111-1111-1111-1111-111111111111"}`)
	}))
	defer api.Close()
	var calls atomic.Int32
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope mcpEnvelope
		raw, _ := io.ReadAll(r.Body)
		if e := json.Unmarshal(raw, &envelope); e != nil {
			t.Error(e)
		}
		if r.Header.Get("Mcp-Method") != envelope.Method || r.Header.Get("MCP-Protocol-Version") != "2026-07-28" {
			t.Error("missing MCP 2.0 request headers")
		}
		if !bytes.Contains(raw, []byte(`"assign/taskExecutionToken":"prepared-token"`)) {
			t.Error("request metadata changed")
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer mcpServer.Close()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"events/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","assign/taskExecutionToken":"prepared-token"}}}` + "\n")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := runMCPStdio(ctx, input, io.Discard, io.Discard, api.URL, mcpServer.URL, api.Client(), store, []string{"assign:read"}); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

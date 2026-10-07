package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

const (
	assignMCPName              = "assign"
	assignMCPURL               = "https://mcp.assign.so/"
	assignMCPOAuthRegistration = "cimd"
	defaultScopes              = "assign:read,assign:write"
	mcpMessageLimit            = 1024 * 1024
	mcpResponseLimit           = 2 * 1024 * 1024
	mcpConcurrency             = 8
)

type externalCommandRunner func(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error

type codexMCPConfiguration struct {
	Enabled   bool `json:"enabled"`
	Transport struct {
		Type              string   `json:"type"`
		URL               string   `json:"url"`
		BearerTokenEnvVar *string  `json:"bearer_token_env_var"`
		Command           string   `json:"command"`
		Args              []string `json:"args"`
	} `json:"transport"`
}

type mcpBridgeCredential struct {
	AccessToken string   `json:"access_token"`
	TokenType   string   `json:"token_type"`
	Scopes      []string `json:"scopes"`
	ExpiresIn   int64    `json:"expires_in"`
	WorkspaceID string   `json:"workspace_id"`
}

type mcpBridgeTokenRequest struct {
	WorkspaceID string   `json:"workspace_id,omitempty"`
	Scopes      []string `json:"scopes"`
}

type mcpEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

func newMCPCommand(opts *options, client *http.Client, runner externalCommandRunner) *cobra.Command {
	command := &cobra.Command{Use: "mcp", Short: "Set up and diagnose Assign MCP clients", Args: noArgs}
	var setupScopes, transport string
	setup := &cobra.Command{
		Use: "setup codex", Short: "Configure Assign MCP in Codex", Args: exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if !strings.EqualFold(args[0], "codex") {
				return newExitError(ExitArguments, "unsupported MCP client %q; supported client: codex", args[0])
			}
			host, err := validatedHost(opts.host)
			if err != nil {
				return newExitError(ExitArguments, "%s", err)
			}
			if host != defaultHost {
				return newExitError(ExitArguments, "MCP setup currently supports only %s", defaultHost)
			}
			ensureLogin := func(ctx context.Context) error {
				if _, tokenErr := authorizationToken(ctx, host, client, opts.credentials); tokenErr == nil {
					return nil
				}
				if strings.TrimSpace(os.Getenv("ASSIGN_TOKEN")) != "" {
					return newExitError(ExitAuthentication, "ASSIGN_TOKEN was rejected")
				}
				return runLogin(ctx, command.OutOrStdout(), command.ErrOrStderr(), host, client, opts.credentials, openBrowser)
			}
			return runMCPSetup(command.Context(), command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr(), transport, setupScopes, ensureLogin, runner)
		},
	}
	setup.Flags().StringVar(&transport, "transport", "remote", "MCP transport: remote or stdio")
	setup.Flags().StringVar(&setupScopes, "scopes", defaultScopes, "MCP scopes: assign:read or assign:read,assign:write")

	var serveScopes string
	serve := &cobra.Command{
		Use: "serve", Short: "Serve Assign MCP over stdio using CLI authentication", Args: noArgs, Hidden: true,
		RunE: func(command *cobra.Command, _ []string) error {
			host, err := validatedHost(opts.host)
			if err != nil {
				return newExitError(ExitArguments, "%s", err)
			}
			scopes, err := canonicalMCPScopes(serveScopes)
			if err != nil {
				return err
			}
			return runMCPStdio(command.Context(), command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr(), host, assignMCPURL, client, opts.credentials, strings.Split(scopes, ","))
		},
	}
	serve.Flags().StringVar(&serveScopes, "scopes", defaultScopes, "attenuated MCP scopes")
	command.AddCommand(setup, serve)
	return command
}

func runMCPSetup(ctx context.Context, input io.Reader, output, diagnostic io.Writer, transport, rawScopes string, ensureLogin func(context.Context) error, runner externalCommandRunner) error {
	scopes, err := canonicalMCPScopes(rawScopes)
	if err != nil {
		return err
	}
	if transport != "remote" && transport != "stdio" {
		return newExitError(ExitArguments, "--transport must be remote or stdio")
	}
	configured, err := inspectCodexMCP(ctx, runner)
	if err != nil {
		return err
	}
	compatible := configured == nil || configured.Enabled && ((transport == "remote" && configured.Transport.Type == "streamable_http" && configured.Transport.URL == assignMCPURL && configured.Transport.BearerTokenEnvVar == nil) || (transport == "stdio" && compatibleStdio(configured, scopes)))
	if !compatible {
		return newExitError(ExitConflict, "Codex already has an incompatible MCP server named %q; inspect it with `codex mcp get %s` and remove it explicitly before retrying", assignMCPName, assignMCPName)
	}
	if err := ensureLogin(ctx); err != nil {
		return err
	}
	if configured == nil {
		var args []string
		if transport == "remote" {
			args = []string{"mcp", "add", assignMCPName, "--url", assignMCPURL, "--oauth-resource", assignMCPURL, "--oauth-client-registration", assignMCPOAuthRegistration}
		} else {
			args = []string{"mcp", "add", assignMCPName, "--", "assign", "mcp", "serve", "--scopes", scopes}
		}
		if err := runner(ctx, input, output, diagnostic, "codex", args...); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return externalCommandError(err, "configure Assign MCP in Codex")
		}
	}
	if transport == "remote" {
		if err := runner(ctx, input, output, diagnostic, "codex", "mcp", "login", assignMCPName, "--scopes", scopes, "--oauth-client-registration", assignMCPOAuthRegistration); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return externalCommandError(err, "authorize Assign MCP in Codex; the server configuration was preserved, so retry with `assign mcp setup codex`")
		}
		_, err = fmt.Fprintln(output, "Assign MCP is configured in Codex with a separate, revocable MCP credential.")
	} else {
		_, err = fmt.Fprintln(output, "Assign MCP is configured in Codex over stdio using your existing CLI authentication.")
	}
	return err
}

func compatibleStdio(configured *codexMCPConfiguration, scopes string) bool {
	return configured.Transport.Type == "stdio" && configured.Transport.Command == "assign" && slices.Equal(configured.Transport.Args, []string{"mcp", "serve", "--scopes", scopes})
}

func inspectCodexMCP(ctx context.Context, runner externalCommandRunner) (*codexMCPConfiguration, error) {
	var output, diagnostic bytes.Buffer
	err := runner(ctx, strings.NewReader(""), &output, &diagnostic, "codex", "mcp", "get", assignMCPName, "--json")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.Contains(diagnostic.String(), "No MCP server named '"+assignMCPName+"' found") {
			return nil, nil
		}
		return nil, externalCommandError(err, "inspect Codex MCP configuration")
	}
	var configured codexMCPConfiguration
	if err := json.NewDecoder(io.LimitReader(&output, 1024*1024)).Decode(&configured); err != nil {
		return nil, newExitError(ExitOperation, "decode Codex MCP configuration")
	}
	return &configured, nil
}

func canonicalMCPScopes(value string) (string, error) {
	seen := make(map[string]bool, 2)
	for _, scope := range strings.Split(value, ",") {
		scope = strings.TrimSpace(scope)
		if scope == "" || (scope != "assign:read" && scope != "assign:write") {
			return "", newExitError(ExitArguments, "--scopes must be assign:read or assign:read,assign:write")
		}
		seen[scope] = true
	}
	if !seen["assign:read"] || len(seen) > 2 || seen["assign:write"] && len(seen) != 2 {
		return "", newExitError(ExitArguments, "--scopes must be assign:read or assign:read,assign:write")
	}
	if seen["assign:write"] {
		return defaultScopes, nil
	}
	return "assign:read", nil
}

type mcpProxy struct {
	host, endpoint                      string
	client                              *http.Client
	store                               credentialStore
	scopes                              []string
	mu                                  sync.Mutex
	token, workspace, session, protocol string
	active                              map[string]context.CancelFunc
	outputMu                            sync.Mutex
	initialized                         chan struct{}
	initializeOnce                      sync.Once
}

func runMCPStdio(ctx context.Context, input io.Reader, output, _ io.Writer, host, endpoint string, client *http.Client, store credentialStore, scopes []string) error {
	if err := validateMCPEndpoint(endpoint); err != nil {
		return err
	}
	proxy := &mcpProxy{host: host, endpoint: endpoint, client: noRedirectClient(client), store: store, scopes: scopes, active: map[string]context.CancelFunc{}, initialized: make(chan struct{})}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), mcpMessageLimit)
	sem := make(chan struct{}, mcpConcurrency)
	errorsSeen := make(chan error, 1)
	var requests sync.WaitGroup
	for scanner.Scan() {
		message := bytes.TrimSpace(scanner.Bytes())
		if len(message) == 0 {
			continue
		}
		var envelope mcpEnvelope
		if !json.Valid(message) || json.Unmarshal(message, &envelope) != nil {
			return newExitError(ExitArguments, "invalid MCP JSON message on stdin")
		}
		copied := append([]byte(nil), message...)
		if envelope.Method == "notifications/cancelled" {
			proxy.cancelRequest(envelope.Params)
		}
		select {
		case sem <- struct{}{}:
		case err := <-errorsSeen:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
		requestCtx, cancel := context.WithCancel(ctx)
		key := requestKey(envelope.ID)
		if key != "" {
			proxy.track(key, cancel)
		}
		requests.Add(1)
		go func(message []byte, envelope mcpEnvelope, requestCtx context.Context, cancel context.CancelFunc, key string) {
			defer requests.Done()
			defer func() { <-sem }()
			defer cancel()
			if key != "" {
				defer proxy.untrack(key)
			}
			if err := proxy.forward(requestCtx, message, envelope, output); err != nil && !errors.Is(err, context.Canceled) {
				select {
				case errorsSeen <- err:
					stop()
				default:
				}
			}
		}(copied, envelope, requestCtx, cancel, key)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP stdio: %w", err)
	}
	requests.Wait()
	select {
	case err := <-errorsSeen:
		return err
	default:
		return nil
	}
}

func (proxy *mcpProxy) forward(ctx context.Context, message []byte, envelope mcpEnvelope, output io.Writer) error {
	if envelope.Method != "initialize" && envelope.Method != "server/discover" && envelopeProtocol(envelope) != "2026-07-28" {
		select {
		case <-proxy.initialized:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	credential, err := proxy.credential(ctx)
	if err != nil {
		return err
	}
	status, err := proxy.forwardOnce(ctx, credential, message, envelope, output)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		proxy.invalidateToken(credential)
		credential, err = proxy.credential(ctx)
		if err != nil {
			return err
		}
		status, err = proxy.forwardOnce(ctx, credential, message, envelope, output)
	}
	if err != nil {
		return err
	}
	if status == http.StatusNotFound && proxy.sessionID() != "" {
		return newExitError(ExitOperation, "MCP session expired; reconnect the client")
	}
	if status < 200 || status >= 300 {
		return newExitError(ExitOperation, "MCP endpoint returned HTTP %d", status)
	}
	if envelope.Method == "initialize" || envelope.Method == "server/discover" {
		proxy.initializeOnce.Do(func() { close(proxy.initialized) })
	}
	return nil
}

func (proxy *mcpProxy) forwardOnce(ctx context.Context, token string, message []byte, envelope mcpEnvelope, output io.Writer) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.endpoint, bytes.NewReader(message))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Method", envelope.Method)
	proxy.mu.Lock()
	if proxy.session != "" {
		request.Header.Set("Mcp-Session-Id", proxy.session)
	}
	if proxy.protocol != "" {
		request.Header.Set("MCP-Protocol-Version", proxy.protocol)
	}
	proxy.mu.Unlock()
	if version := envelopeProtocol(envelope); version != "" {
		request.Header.Set("MCP-Protocol-Version", version)
	}
	response, err := proxy.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0, context.Canceled
		}
		return 0, newExitError(ExitOperation, "forward MCP message: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return response.StatusCode, nil
	}
	proxy.captureSession(response.Header.Get("Mcp-Session-Id"), envelope)
	if response.StatusCode == http.StatusAccepted || response.Body == http.NoBody {
		return response.StatusCode, nil
	}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return response.StatusCode, proxy.copySSE(response.Body, output)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, mcpResponseLimit+1))
	if err != nil {
		return 0, err
	}
	if len(body) > mcpResponseLimit {
		return 0, newExitError(ExitOperation, "MCP response exceeds %d bytes", mcpResponseLimit)
	}
	if len(bytes.TrimSpace(body)) > 0 {
		return response.StatusCode, proxy.writeLine(output, bytes.TrimSpace(body))
	}
	return response.StatusCode, nil
}

func (proxy *mcpProxy) copySSE(reader io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), mcpResponseLimit)
	var data []string
	total := 0
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := []byte(strings.Join(data, "\n"))
		data = data[:0]
		if string(payload) == "[DONE]" {
			return nil
		}
		if !json.Valid(payload) {
			return newExitError(ExitOperation, "MCP SSE event contains invalid JSON")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, payload); err != nil {
			return newExitError(ExitOperation, "MCP SSE event contains invalid JSON")
		}
		return proxy.writeLine(output, compact.Bytes())
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > mcpResponseLimit {
			return newExitError(ExitOperation, "MCP response exceeds %d bytes", mcpResponseLimit)
		}
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func (proxy *mcpProxy) credential(ctx context.Context) (string, error) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.token != "" {
		return proxy.token, nil
	}
	credential, err := issueMCPBridgeToken(ctx, proxy.host, proxy.client, proxy.store, proxy.scopes, proxy.workspace)
	if err != nil {
		return "", err
	}
	if proxy.workspace != "" && credential.WorkspaceID != proxy.workspace {
		return "", newExitError(ExitAuthentication, "CLI Workspace changed; reconnect the MCP client")
	}
	proxy.workspace, proxy.token = credential.WorkspaceID, credential.AccessToken
	return proxy.token, nil
}

func issueMCPBridgeToken(ctx context.Context, host string, client *http.Client, store credentialStore, scopes []string, workspace string) (mcpBridgeCredential, error) {
	cliToken, err := authorizationToken(ctx, host, client, store)
	if err != nil {
		return mcpBridgeCredential{}, err
	}
	body, err := json.Marshal(mcpBridgeTokenRequest{WorkspaceID: workspace, Scopes: scopes})
	if err != nil {
		return mcpBridgeCredential{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/v1/cli/mcp/token", bytes.NewReader(body))
	if err != nil {
		return mcpBridgeCredential{}, err
	}
	request.Header.Set("Authorization", "Bearer "+cliToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := noRedirectClient(client).Do(request)
	if err != nil {
		return mcpBridgeCredential{}, newExitError(ExitOperation, "request MCP bridge credential: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return mcpBridgeCredential{}, newExitError(ExitAuthentication, "CLI authentication was rejected; run assign login")
	}
	if response.StatusCode == http.StatusConflict {
		return mcpBridgeCredential{}, newExitError(ExitAuthentication, "CLI Workspace changed; reconnect the MCP client")
	}
	if response.StatusCode != http.StatusCreated {
		return mcpBridgeCredential{}, newExitError(ExitOperation, "request MCP bridge credential: HTTP %d", response.StatusCode)
	}
	var credential mcpBridgeCredential
	if json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&credential) != nil || credential.AccessToken == "" || credential.TokenType != "Bearer" || credential.WorkspaceID == "" {
		return mcpBridgeCredential{}, newExitError(ExitOperation, "decode MCP bridge credential")
	}
	return credential, nil
}

func validateMCPEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	local := parsed != nil && parsed.Scheme == "http" && slices.Contains([]string{"127.0.0.1", "localhost", "::1"}, parsed.Hostname())
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" || (parsed.Scheme != "https" && !local) {
		return newExitError(ExitArguments, "invalid MCP endpoint")
	}
	return nil
}

func noRedirectClient(client *http.Client) *http.Client {
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}
func requestKey(id json.RawMessage) string { return string(bytes.TrimSpace(id)) }
func (proxy *mcpProxy) track(key string, cancel context.CancelFunc) {
	proxy.mu.Lock()
	proxy.active[key] = cancel
	proxy.mu.Unlock()
}
func (proxy *mcpProxy) untrack(key string) {
	proxy.mu.Lock()
	delete(proxy.active, key)
	proxy.mu.Unlock()
}
func (proxy *mcpProxy) cancelRequest(params json.RawMessage) {
	var value struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &value) != nil {
		return
	}
	proxy.mu.Lock()
	cancel := proxy.active[requestKey(value.RequestID)]
	proxy.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (proxy *mcpProxy) invalidateToken(token string) {
	proxy.mu.Lock()
	if proxy.token == token {
		proxy.token = ""
	}
	proxy.mu.Unlock()
}
func (proxy *mcpProxy) sessionID() string {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	return proxy.session
}
func (proxy *mcpProxy) captureSession(session string, envelope mcpEnvelope) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if session != "" {
		proxy.session = session
	}
	if envelope.Method == "initialize" {
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil {
			proxy.protocol = params.ProtocolVersion
		}
	}
}
func (proxy *mcpProxy) writeLine(output io.Writer, payload []byte) error {
	proxy.outputMu.Lock()
	defer proxy.outputMu.Unlock()
	_, err := fmt.Fprintln(output, string(payload))
	return err
}

func externalCommandError(err error, action string) error {
	var executableError *exec.Error
	if errors.As(err, &executableError) {
		return newExitError(ExitOperation, "Codex CLI is not installed or not available on PATH")
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return newExitError(ExitOperation, "%s", action)
}
func runExternalCommand(ctx context.Context, input io.Reader, output, diagnostic io.Writer, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin, command.Stdout, command.Stderr = input, output, diagnostic
	return command.Run()
}

// MCP 2.0 carries negotiation per request. Forward the original body unchanged;
// this transport neither owns event subscriptions nor schedules a native agent.
func envelopeProtocol(envelope mcpEnvelope) string {
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(envelope.Params, &p) != nil {
		return ""
	}
	version, _ := p.Meta["io.modelcontextprotocol/protocolVersion"].(string)
	return version
}

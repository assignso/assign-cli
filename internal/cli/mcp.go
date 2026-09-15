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
	"strings"

	"github.com/spf13/cobra"
)

const (
	assignMCPName    = "assign"
	assignMCPURL     = "https://mcp.assign.so/"
	mcpMessageLimit  = 1024 * 1024
	mcpResponseLimit = 2 * 1024 * 1024
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

func newMCPCommand(opts *options, client *http.Client, runner externalCommandRunner) *cobra.Command {
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Set up Assign MCP clients",
		Args:  noArgs,
	}
	setup := &cobra.Command{
		Use:   "setup codex",
		Short: "Configure Assign MCP over the CLI's stdio transport",
		Args:  exactArgs(1),
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
			return runMCPSetup(command.Context(), command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr(), ensureLogin, runner)
		},
	}
	serve := &cobra.Command{
		Use:    "serve",
		Short:  "Serve Assign MCP over stdio using CLI authentication",
		Args:   noArgs,
		Hidden: true,
		RunE: func(command *cobra.Command, _ []string) error {
			host, err := validatedHost(opts.host)
			if err != nil {
				return newExitError(ExitArguments, "%s", err)
			}
			return runMCPStdio(command.Context(), command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr(), host, assignMCPURL, client, opts.credentials)
		},
	}
	command.AddCommand(setup, serve)
	return command
}

func runMCPSetup(ctx context.Context, input io.Reader, output, diagnostic io.Writer, ensureLogin func(context.Context) error, runner externalCommandRunner) error {
	configured, err := inspectCodexMCP(ctx, runner)
	if err != nil {
		return err
	}
	if configured != nil && (!configured.Enabled || configured.Transport.Type != "stdio" || configured.Transport.Command != "assign" || len(configured.Transport.Args) != 2 || configured.Transport.Args[0] != "mcp" || configured.Transport.Args[1] != "serve") {
		return newExitError(ExitConflict, "Codex already has an incompatible MCP server named %q; inspect it with `codex mcp get %s` and remove it explicitly before retrying", assignMCPName, assignMCPName)
	}
	if err := ensureLogin(ctx); err != nil {
		return err
	}
	if configured == nil {
		if err := runner(ctx, input, output, diagnostic, "codex", "mcp", "add", assignMCPName, "--", "assign", "mcp", "serve"); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return externalCommandError(err, "configure Assign MCP in Codex")
		}
	}
	_, err = fmt.Fprintln(output, "Assign MCP is configured in Codex over stdio using your existing CLI authentication.")
	return err
}

func inspectCodexMCP(ctx context.Context, runner externalCommandRunner) (*codexMCPConfiguration, error) {
	var output bytes.Buffer
	var diagnostic bytes.Buffer
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
	decoder := json.NewDecoder(io.LimitReader(&output, 1024*1024))
	if err := decoder.Decode(&configured); err != nil {
		return nil, newExitError(ExitOperation, "decode Codex MCP configuration")
	}
	return &configured, nil
}

func runMCPStdio(ctx context.Context, input io.Reader, output, diagnostic io.Writer, host, endpoint string, client *http.Client, store credentialStore) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), mcpMessageLimit)
	var bridgeToken string
	for scanner.Scan() {
		message := bytes.TrimSpace(scanner.Bytes())
		if len(message) == 0 {
			continue
		}
		if !json.Valid(message) {
			return newExitError(ExitArguments, "invalid MCP JSON message on stdin")
		}
		if bridgeToken == "" {
			var err error
			bridgeToken, err = issueMCPBridgeToken(ctx, host, client, store)
			if err != nil {
				return err
			}
		}
		status, err := forwardMCPMessage(ctx, client, endpoint, bridgeToken, message, output)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized {
			bridgeToken, err = issueMCPBridgeToken(ctx, host, client, store)
			if err != nil {
				return err
			}
			status, err = forwardMCPMessage(ctx, client, endpoint, bridgeToken, message, output)
			if err != nil {
				return err
			}
		}
		if status < 200 || status >= 300 {
			return newExitError(ExitOperation, "MCP endpoint returned HTTP %d", status)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP stdio: %w", err)
	}
	return nil
}

func issueMCPBridgeToken(ctx context.Context, host string, client *http.Client, store credentialStore) (string, error) {
	cliToken, err := authorizationToken(ctx, host, client, store)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/v1/cli/mcp/token", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+cliToken)
	response, err := client.Do(request)
	if err != nil {
		return "", newExitError(ExitOperation, "request MCP bridge credential: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return "", newExitError(ExitAuthentication, "CLI authentication was rejected; run assign login")
	}
	if response.StatusCode != http.StatusCreated {
		return "", newExitError(ExitOperation, "request MCP bridge credential: HTTP %d", response.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&body); err != nil || body.AccessToken == "" || body.TokenType != "Bearer" {
		return "", newExitError(ExitOperation, "decode MCP bridge credential")
	}
	return body.AccessToken, nil
}

func forwardMCPMessage(ctx context.Context, client *http.Client, endpoint, token string, message []byte, output io.Writer) (int, error) {
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return 0, newExitError(ExitArguments, "invalid MCP endpoint")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(message))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return 0, newExitError(ExitOperation, "forward MCP message: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return response.StatusCode, nil
	}
	if response.StatusCode == http.StatusAccepted || response.Body == http.NoBody {
		return response.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, mcpResponseLimit+1))
	if err != nil {
		return 0, err
	}
	if len(body) > mcpResponseLimit {
		return 0, newExitError(ExitOperation, "MCP response exceeds %d bytes", mcpResponseLimit)
	}
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(bytes.NewReader(body))
		scanner.Buffer(make([]byte, 64*1024), mcpResponseLimit)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if data != "" && data != "[DONE]" {
					if _, err := fmt.Fprintln(output, data); err != nil {
						return 0, err
					}
				}
			}
		}
		return response.StatusCode, scanner.Err()
	}
	if len(bytes.TrimSpace(body)) > 0 {
		_, err = fmt.Fprintln(output, string(bytes.TrimSpace(body)))
	}
	return response.StatusCode, err
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
	command.Stdin = input
	command.Stdout = output
	command.Stderr = diagnostic
	return command.Run()
}

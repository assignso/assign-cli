package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

const (
	assignMCPName              = "assign"
	assignMCPURL               = "https://mcp.assign.so/"
	assignMCPOAuthRegistration = "cimd"
	defaultScopes              = "assign:read,assign:write"
)

type externalCommandRunner func(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error

type codexMCPConfiguration struct {
	Enabled   bool `json:"enabled"`
	Transport struct {
		Type              string  `json:"type"`
		URL               string  `json:"url"`
		BearerTokenEnvVar *string `json:"bearer_token_env_var"`
	} `json:"transport"`
}

func newMCPCommand(opts *options, client *http.Client, runner externalCommandRunner) *cobra.Command {
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Set up Assign MCP clients",
		Args:  noArgs,
	}
	var scopes string
	setup := &cobra.Command{
		Use:   "setup codex",
		Short: "Configure and authorize Assign MCP in Codex",
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
				if _, loadErr := opts.credentials.Load(host); loadErr == nil {
					return nil
				} else if !errors.Is(loadErr, os.ErrNotExist) {
					return newExitError(ExitAuthentication, "read interactive CLI credential: %v", loadErr)
				}
				return runLogin(ctx, command.OutOrStdout(), command.ErrOrStderr(), host, client, opts.credentials, openBrowser)
			}
			return runMCPSetup(command.Context(), command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr(), scopes, ensureLogin, runner)
		},
	}
	setup.Flags().StringVar(&scopes, "scopes", defaultScopes, "MCP scopes: assign:read or assign:read,assign:write")
	command.AddCommand(setup)
	return command
}

func runMCPSetup(ctx context.Context, input io.Reader, output, diagnostic io.Writer, rawScopes string, ensureLogin func(context.Context) error, runner externalCommandRunner) error {
	scopes, err := canonicalMCPScopes(rawScopes)
	if err != nil {
		return err
	}
	configured, err := inspectCodexMCP(ctx, runner)
	if err != nil {
		return err
	}
	if configured != nil && (!configured.Enabled || configured.Transport.Type != "streamable_http" || configured.Transport.URL != assignMCPURL || configured.Transport.BearerTokenEnvVar != nil) {
		return newExitError(ExitConflict, "Codex already has an incompatible MCP server named %q; inspect it with `codex mcp get %s` and remove it explicitly before retrying", assignMCPName, assignMCPName)
	}
	if err := ensureLogin(ctx); err != nil {
		return err
	}
	if configured == nil {
		if err := runner(ctx, input, output, diagnostic, "codex", "mcp", "add", assignMCPName, "--url", assignMCPURL, "--oauth-resource", assignMCPURL, "--oauth-client-registration", assignMCPOAuthRegistration); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return externalCommandError(err, "configure Assign MCP in Codex")
		}
	}
	if err := runner(ctx, input, output, diagnostic, "codex", "mcp", "login", assignMCPName, "--scopes", scopes, "--oauth-client-registration", assignMCPOAuthRegistration); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return externalCommandError(err, "authorize Assign MCP in Codex; the server configuration was preserved, so retry with `assign mcp setup codex`")
	}
	_, err = fmt.Fprintln(output, "Assign MCP is configured in Codex with a separate, revocable MCP credential.")
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

func canonicalMCPScopes(value string) (string, error) {
	seen := make(map[string]bool, 2)
	for _, scope := range strings.Split(value, ",") {
		scope = strings.TrimSpace(scope)
		if scope == "" || (scope != "assign:read" && scope != "assign:write") {
			return "", newExitError(ExitArguments, "--scopes must be assign:read or assign:read,assign:write")
		}
		seen[scope] = true
	}
	if !seen["assign:read"] || (seen["assign:write"] && len(seen) != 2) || (!seen["assign:write"] && len(seen) != 1) {
		return "", newExitError(ExitArguments, "--scopes must be assign:read or assign:read,assign:write")
	}
	if seen["assign:write"] {
		return defaultScopes, nil
	}
	return "assign:read", nil
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

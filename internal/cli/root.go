package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

const defaultHost = "https://api.assign.so"

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type options struct {
	host        string
	credentials credentialStore
	contexts    projectContextStore
}

type commandAlias struct {
	command string
	aliases []string
}

var commandAliases = []commandAlias{
	{command: "login", aliases: []string{"signin"}},
	{command: "logout", aliases: []string{"signout"}},
	{command: "search", aliases: []string{"find"}},
	{command: "task", aliases: []string{"t"}},
	{command: "workspace", aliases: []string{"ws"}},
	{command: "project", aliases: []string{"p"}},
	{command: "document", aliases: []string{"doc"}},
	{command: "completion", aliases: []string{"completions"}},
	{command: "version", aliases: []string{"v"}},
	{command: "doctor", aliases: []string{"diag", "diagnose"}},
}

func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCommand(stdin, stdout, stderr)
	root.SetArgs(args)
	if _, _, err := root.Find(args); err != nil {
		fmt.Fprintf(stderr, "assign: %s\n", err)
		return ExitArguments
	}

	err := root.ExecuteContext(ctx)
	if err == nil || isBrokenPipe(err) {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return ExitInterrupt
	}

	fmt.Fprintf(stderr, "assign: %s\n", err)
	return exitCode(err)
}

func newRootCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	return newRootCommandWithClient(stdin, stdout, stderr, http.DefaultClient)
}

func newRootCommandWithClient(stdin io.Reader, stdout, stderr io.Writer, client *http.Client) *cobra.Command {
	opts := &options{credentials: defaultCredentialStore(), contexts: defaultProjectContextStore()}
	if client == nil {
		client = http.DefaultClient
	}
	root := &cobra.Command{
		Use:           "assign",
		Short:         "Work with Assign from the terminal",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runMyWork(command.Context(), command.OutOrStdout(), opts.host, client, opts.credentials)
		},
		PersistentPreRunE: func(*cobra.Command, []string) error {
			if _, err := validatedHost(opts.host); err != nil {
				return newExitError(ExitArguments, "%s", err)
			}
			return nil
		},
	}
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return newExitError(ExitArguments, "%s", err)
	})
	root.CompletionOptions.DisableDefaultCmd = true
	root.CompletionOptions.SetDefaultShellCompDirective(cobra.ShellCompDirectiveNoFileComp)
	root.ValidArgsFunction = completeAliases
	root.PersistentFlags().StringVar(&opts.host, "host", defaultHost, "Assign API host (HTTPS only, for this command)")

	root.AddCommand(withAliases(newVersionCommand()))
	root.AddCommand(withAliases(newCompletionCommand(root)))
	root.AddCommand(withAliases(newDoctorCommand(opts)))
	root.AddCommand(withAliases(newLoginCommand(opts, client)))
	root.AddCommand(withAliases(newLogoutCommand(opts, client)))
	root.AddCommand(withAliases(newSearchCommand(opts, client)))
	root.AddCommand(newAliasesCommand())
	root.AddCommand(withAliases(newTaskCommand(opts, client)))
	root.AddCommand(withAliases(newWorkspaceCommand(opts, client)))
	root.AddCommand(withAliases(newProjectCommand(opts, client)))
	root.AddCommand(withAliases(newDocumentCommand(opts, client)))
	root.AddCommand(newTaskActionCommand("start", "Start and assign a Task", opts, client))
	root.AddCommand(newTaskActionCommand("done", "Complete a Task", opts, client))
	root.AddCommand(newTaskActionCommand("reopen", "Reopen a Task", opts, client))

	return root
}

func completeAliases(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	completions := make([]string, 0, len(commandAliases))
	for _, definition := range commandAliases {
		for _, alias := range definition.aliases {
			if strings.HasPrefix(alias, toComplete) {
				completions = append(completions, cobra.CompletionWithDesc(alias, "Alias for "+definition.command))
			}
		}
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}

func withAliases(command *cobra.Command) *cobra.Command {
	name := command.Name()
	for _, definition := range commandAliases {
		if definition.command == name {
			command.Aliases = append([]string(nil), definition.aliases...)
			break
		}
	}
	return command
}

func newAliasesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "aliases",
		Short: "List command aliases",
		Args:  noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			for _, definition := range commandAliases {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%s  %s\n", strings.Join(definition.aliases, ", "), definition.command); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newTaskCommand(opts *options, client *http.Client) *cobra.Command {
	command := &cobra.Command{
		Use:   "task",
		Short: "Work with Tasks",
		Args:  noArgs,
	}
	command.AddCommand(newTaskActionCommand("start", "Start and assign a Task", opts, client))
	command.AddCommand(newTaskActionCommand("done", "Complete a Task", opts, client))
	command.AddCommand(newTaskActionCommand("reopen", "Reopen a Task", opts, client))
	return command
}

func newTaskActionCommand(action, description string, opts *options, client *http.Client) *cobra.Command {
	var revision int64
	command := &cobra.Command{
		Use:   action + " <task-code>",
		Short: description,
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if revision < 1 {
				return newExitError(ExitArguments, "--revision must be a positive Task revision")
			}
			return runTaskAction(command.Context(), command.OutOrStdout(), opts.host, client, opts.credentials, action, strings.ToUpper(strings.TrimSpace(args[0])), revision)
		},
	}
	command.Flags().Int64Var(&revision, "revision", 0, "Task revision for optimistic concurrency")
	return command
}

func runTaskAction(ctx context.Context, output io.Writer, rawHost string, client *http.Client, store credentialStore, action, code string, revision int64) error {
	host, err := validatedHost(rawHost)
	if err != nil {
		return newExitError(ExitArguments, "%s", err)
	}
	token, err := authorizationToken(ctx, host, client, store)
	if err != nil {
		return err
	}
	key, err := newIdempotencyKey()
	if err != nil {
		return newExitError(ExitOperation, "create idempotency key")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/v1/cli/tasks/"+url.PathEscape(code)+"/"+action, nil)
	if err != nil {
		return newExitError(ExitOperation, "prepare Task request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("If-Match", fmt.Sprintf("\"%d\"", revision))
	request.Header.Set("Idempotency-Key", key)
	response, err := client.Do(request)
	if err != nil {
		return newExitError(ExitOperation, "request Task action: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return newExitError(ExitAuthentication, "authentication was rejected; create or configure a current personal API token")
	}
	if response.StatusCode == http.StatusConflict {
		return newExitError(ExitConflict, "Task changed; refresh its revision and retry")
	}
	if response.StatusCode != http.StatusOK {
		return newExitError(ExitOperation, "Task action failed with HTTP %d", response.StatusCode)
	}
	var result struct {
		Task struct {
			Code  string `json:"code"`
			Title string `json:"title"`
		} `json:"task"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&result); err != nil || result.Task.Code == "" || result.Task.Title == "" {
		return newExitError(ExitOperation, "decode Task action response")
	}
	_, err = fmt.Fprintf(output, "%s  %s\n", result.Task.Code, result.Task.Title)
	return err
}

func newIdempotencyKey() (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func newSearchCommand(opts *options, client *http.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "search <query>",
		Short: "Search Tasks and Documents in the token Workspace",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runSearch(command.Context(), command.OutOrStdout(), opts.host, client, opts.credentials, args[0])
		},
	}
}

type cliSearchResponse struct {
	Items []struct {
		ResourceType string `json:"resource_type"`
		Code         string `json:"code"`
		Title        string `json:"title"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

func runSearch(ctx context.Context, output io.Writer, rawHost string, client *http.Client, store credentialStore, query string) error {
	host, err := validatedHost(rawHost)
	if err != nil {
		return newExitError(ExitArguments, "%s", err)
	}
	token, err := authorizationToken(ctx, host, client, store)
	if err != nil {
		return err
	}
	endpoint := host + "/api/v1/cli/search?q=" + url.QueryEscape(query)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return newExitError(ExitOperation, "prepare search request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return newExitError(ExitOperation, "request search: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return newExitError(ExitAuthentication, "authentication was rejected; create or configure a current personal API token")
	}
	if response.StatusCode != http.StatusOK {
		return newExitError(ExitOperation, "search request failed with HTTP %d", response.StatusCode)
	}
	var page cliSearchResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024))
	if err := decoder.Decode(&page); err != nil {
		return newExitError(ExitOperation, "decode search response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return newExitError(ExitOperation, "decode search response")
	}
	for _, item := range page.Items {
		if item.ResourceType == "" || item.Code == "" || item.Title == "" {
			return newExitError(ExitOperation, "search response is incomplete")
		}
		if _, err := fmt.Fprintf(output, "%s  %s\n", item.Code, item.Title); err != nil {
			return err
		}
	}
	return nil
}

type cliMyWorkResponse struct {
	Items []struct {
		Code           string `json:"code"`
		Title          string `json:"title"`
		StatusCategory string `json:"status_category"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

func runMyWork(ctx context.Context, output io.Writer, rawHost string, client *http.Client, store credentialStore) error {
	host, err := validatedHost(rawHost)
	if err != nil {
		return newExitError(ExitArguments, "%s", err)
	}
	token, err := authorizationToken(ctx, host, client, store)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/api/v1/cli/my-work", nil)
	if err != nil {
		return newExitError(ExitOperation, "prepare My Work request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return newExitError(ExitOperation, "request My Work: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return newExitError(ExitAuthentication, "authentication was rejected; create or configure a current personal API token")
	}
	if response.StatusCode != http.StatusOK {
		return newExitError(ExitOperation, "My Work request failed with HTTP %d", response.StatusCode)
	}
	var page cliMyWorkResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024))
	if err := decoder.Decode(&page); err != nil {
		return newExitError(ExitOperation, "decode My Work response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return newExitError(ExitOperation, "decode My Work response")
	}
	for _, item := range page.Items {
		if item.Code == "" || item.Title == "" || item.StatusCategory == "" {
			return newExitError(ExitOperation, "My Work response is incomplete")
		}
		if _, err := fmt.Fprintf(output, "%s  %s\n", item.Code, item.Title); err != nil {
			return err
		}
	}
	return nil
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "assign %s\ncommit %s\nbuilt %s\n", version, commit, date)
			return err
		},
	}
}

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	completion := &cobra.Command{
		Use:       "completion [bash|zsh|fish]",
		Short:     "Generate shell completion",
		Args:      exactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(cmd.OutOrStdout(), true)
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			default:
				return newExitError(ExitArguments, "unsupported shell %q; expected bash, zsh, or fish", args[0])
			}
		},
	}
	completion.CompletionOptions.DisableDefaultCmd = true
	return completion
}

func newDoctorCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run redacted local diagnostics",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			host, err := validatedHost(opts.host)
			if err != nil {
				return newExitError(ExitArguments, "%s", err)
			}

			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "host ok %s\n", host); err != nil {
				return err
			}

			authenticated := strings.TrimSpace(os.Getenv("ASSIGN_TOKEN")) != ""
			if authenticated {
				if _, err := fmt.Fprintln(out, "credential ok ASSIGN_TOKEN"); err != nil {
					return err
				}
			} else if _, loadErr := opts.credentials.Load(host); loadErr == nil {
				authenticated = true
				if _, err := fmt.Fprintln(out, "credential ok browser login"); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(out, "credential failed not configured"); err != nil {
					return err
				}
			}

			if inGitRepository(cmd.Context()) {
				if _, err := fmt.Fprintln(out, "repository ok detected"); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(out, "repository optional unavailable"); err != nil {
				return err
			}

			if _, err := fmt.Fprintf(out, "runtime ok %s/%s\n", runtime.GOOS, runtime.GOARCH); err != nil {
				return err
			}

			if !authenticated {
				return newExitError(ExitAuthentication, "authentication is not configured; run assign login or set ASSIGN_TOKEN for automation")
			}
			return nil
		},
	}
}

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return newExitError(ExitArguments, "unknown argument %q for %q", args[0], cmd.CommandPath())
}

func exactArgs(expected int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == expected {
			return nil
		}
		return newExitError(ExitArguments, "%q requires exactly %d argument(s)", cmd.CommandPath(), expected)
	}
}

func validatedHost(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("--host must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("--host must not include credentials, a path, query, or fragment")
	}
	parsed.Path = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func inGitRepository(ctx context.Context) bool {
	command := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	return command.Run() == nil
}

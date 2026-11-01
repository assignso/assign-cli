package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
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
	hostSource  string
	settings    cliSettingsStore
	credentials credentialStore
	contexts    projectContextStore
	openURL     func(string) error
	runDiscuss  func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
	isTerminal  func(io.Reader, io.Writer) bool
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
	opts := &options{credentials: defaultCredentialStore(), contexts: defaultProjectContextStore(), settings: defaultCLISettingsStore(), openURL: openBrowser, runDiscuss: runDiscussSidecar, isTerminal: isInteractiveTerminal}
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
		PersistentPreRunE: func(command *cobra.Command, _ []string) error {
			return resolveConfiguredHost(command, opts)
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
	root.AddCommand(newUpdateCommand(releaseHTTPClient()))
	root.AddCommand(withAliases(newCompletionCommand(root)))
	root.AddCommand(withAliases(newDoctorCommand(opts)))
	root.AddCommand(newConfigCommand(opts))
	root.AddCommand(newInitCommand(opts, client))
	root.AddCommand(withAliases(newLoginCommand(opts, client)))
	root.AddCommand(withAliases(newLogoutCommand(opts, client)))
	root.AddCommand(newMCPCommand(opts, client, runExternalCommand))
	root.AddCommand(newDiscussCommand(opts, client))
	root.AddCommand(newHostedAgentCommand(opts, client))
	root.AddCommand(newOperationCommand(opts, client))
	root.AddCommand(withAliases(newSearchCommand(opts, client)))
	root.AddCommand(newAliasesCommand())
	root.AddCommand(withAliases(newTaskCommand(opts, client)))
	root.AddCommand(newMilestoneCommand(opts, client))
	root.AddCommand(newActivityCommand(opts, client))
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
	command.AddCommand(newTaskShowCommand(opts, client))
	command.AddCommand(newTaskContextCommand(opts, client), newTaskCommentCommand(opts, client), newTaskCommentsCommand(opts, client), newTaskCreateCommand(opts, client))
	command.AddCommand(newTaskActionCommand("start", "Start and assign a Task", opts, client))
	command.AddCommand(newTaskActionCommand("done", "Complete a Task", opts, client))
	command.AddCommand(newTaskActionCommand("reopen", "Reopen a Task", opts, client))
	return command
}

type cliTask struct {
	Code           string `json:"code"`
	ProjectCode    string `json:"project_code"`
	Title          string `json:"title"`
	StatusCategory string `json:"status_category"`
	Revision       int64  `json:"revision"`
	URL            string `json:"url"`
}

// taskFields are the scalar values `task show --field` prints. The names are
// a scripting contract, so add to this list rather than renaming entries.
var taskFields = []string{"code", "title", "project", "status", "revision", "url"}

func newTaskShowCommand(opts *options, client *http.Client) *cobra.Command {
	var field string
	command := &cobra.Command{
		Use:     "show <task-code>",
		Aliases: []string{"view"},
		Short:   "Show a Task and its current revision",
		Args:    exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskShow(command.Context(), command.OutOrStdout(), opts, client, args[0], field)
		},
	}
	command.Flags().StringVar(&field, "field", "", "Print only one field: "+strings.Join(taskFields, ", "))
	_ = command.RegisterFlagCompletionFunc("field", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return taskFields, cobra.ShellCompDirectiveNoFileComp
	})
	return command
}

func runTaskShow(ctx context.Context, output io.Writer, opts *options, client *http.Client, code, field string) error {
	field = strings.ToLower(strings.TrimSpace(field))
	if field != "" && !slices.Contains(taskFields, field) {
		return newExitError(ExitArguments, "unsupported --field %q; expected one of %s", field, strings.Join(taskFields, ", "))
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return newExitError(ExitArguments, "Task code must not be empty")
	}
	item, readErr := readResourceTaskShow(ctx, opts, client, code)
	if readErr != nil {
		return readErr
	}
	var err error
	switch field {
	case "":
		_, err = fmt.Fprintf(output, "# %s  %s\n\nProject: %s\nStatus: %s\nURL: %s\nRevision: %d\n", safeInline(item.Code), safeInline(item.Title), safeInline(item.ProjectCode), safeInline(item.StatusCategory), safeInline(item.URL), item.Revision)
	case "code":
		_, err = fmt.Fprintln(output, safeInline(item.Code))
	case "title":
		_, err = fmt.Fprintln(output, safeInline(item.Title))
	case "project":
		_, err = fmt.Fprintln(output, safeInline(item.ProjectCode))
	case "status":
		_, err = fmt.Fprintln(output, safeInline(item.StatusCategory))
	case "revision":
		_, err = fmt.Fprintln(output, item.Revision)
	case "url":
		_, err = fmt.Fprintln(output, safeInline(item.URL))
	}
	return err
}

var statusUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func newTaskActionCommand(action, description string, opts *options, client *http.Client) *cobra.Command {
	var revision int64
	var key, targetStatusID, taskID, condition string
	command := &cobra.Command{
		Use:   action + " <task-code>",
		Short: description,
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if taskID != "" || condition != "" {
				if !canonicalIdentifier(taskID) || !taskActionValidator(condition) || key == "" || revision != 0 {
					return newExitError(ExitArguments, "recovery requires --task-id, the original opaque --if-match and --idempotency-key; omit --revision")
				}
			} else if revision < 1 {
				return newExitError(ExitArguments, "--revision must be a positive Task revision; run assign task show <task-code> to read it")
			}
			requestKey, err := mutationKey(key)
			if err != nil {
				return err
			}
			code, err := taskCode(args[0])
			if err != nil {
				return err
			}
			if safeInline(code) != code {
				return newExitError(ExitArguments, "Task code must not contain terminal control characters")
			}
			if targetStatusID != "" {
				if !statusUUIDPattern.MatchString(targetStatusID) {
					return newExitError(ExitArguments, "--target-status-id must be a Status UUID")
				}
			}
			return runTaskAction(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, action, code, revision, requestKey, targetStatusID, taskID, condition)
		},
	}
	if action == "done" {
		command.Flags().StringVar(&targetStatusID, "target-status-id", "", "Completing Status UUID; required when several Statuses count as completed")
	}
	command.Flags().Int64Var(&revision, "revision", 0, "Current Task revision, as printed by assign task show (optimistic concurrency)")
	command.Flags().StringVar(&taskID, "task-id", "", "Original immutable Task ID for an identical retry")
	command.Flags().StringVar(&condition, "if-match", "", "Original opaque Task validator for an identical retry")
	command.Flags().StringVar(&key, "idempotency-key", "", "Retain a 16–200 character key for identical retries; generated when omitted")
	return command
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
		Short: "Search content in the token Workspace",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runSearch(command.Context(), command.OutOrStdout(), opts.host, client, opts.credentials, args[0])
		},
	}
}

// Canonical SearchResultList: keep identifiers separate from immutable IDs.
// People and other matches without a display identifier use their resource ID.
type cliSearchResponse struct {
	Items []struct {
		ResourceType string `json:"resource_type"`
		ResourceID   string `json:"resource_id"`
		Identifier   string `json:"identifier"`
		Title        string `json:"title"`
	} `json:"data"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

func runSearch(ctx context.Context, output io.Writer, rawHost string, client *http.Client, store credentialStore, query string) error {
	transport, err := newOperationTransport(ctx, &options{host: rawHost, credentials: store}, client)
	if err != nil {
		return err
	}
	workspace, err := resourceWorkspace(ctx, transport, "")
	if err != nil {
		return err
	}
	// Keep the existing single page of up to 50 results, with no resource-type
	// filter or client-side ranking. The canonical service owns query bounds.
	values := url.Values{"q": {query}, "limit": {"50"}}
	endpoint := transport.host + "/api/v1/workspaces/" + workspace.ID + "/search?" + values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return newExitError(ExitOperation, "prepare search request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Authorization", "Bearer "+transport.token)
	response, err := transport.client.Do(request)
	if err != nil {
		return newExitError(ExitOperation, "request search: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return productReadError(response, "search")
	}
	const maxSearchResponseBytes = 2 * 1024 * 1024
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	encoding := response.Header.Get("Content-Encoding")
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxSearchResponseBytes+1))
	if mediaErr != nil || media != "application/json" || (encoding != "" && encoding != "identity") || readErr != nil || len(data) > maxSearchResponseBytes {
		return newExitError(ExitOperation, "search response is incomplete or exceeds its limit")
	}
	var page cliSearchResponse
	if json.Unmarshal(data, &page) != nil || page.Items == nil || len(page.Items) > 50 || page.HasMore != (page.NextCursor != nil) {
		return newExitError(ExitOperation, "decode search response")
	}
	if page.NextCursor != nil && (*page.NextCursor == "" || len(*page.NextCursor) > 8192 || safeInline(*page.NextCursor) != *page.NextCursor) {
		return newExitError(ExitOperation, "search continuation is invalid")
	}
	// Validate the complete page before writing any result to stdout.
	for _, item := range page.Items {
		if item.ResourceType == "" || !canonicalIdentifier(item.ResourceID) || item.Title == "" {
			return newExitError(ExitOperation, "search response is incomplete")
		}
	}
	for _, item := range page.Items {
		identifier := item.Identifier
		if identifier == "" {
			identifier = item.ResourceID
		}
		if _, err := fmt.Fprintf(output, "%s  %s\n", identifier, item.Title); err != nil {
			return err
		}
	}
	return nil
}

func runMyWork(ctx context.Context, output io.Writer, rawHost string, client *http.Client, store credentialStore) error {
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, err := newOperationTransport(ctx, &options{host: rawHost, credentials: store}, client)
	if err != nil {
		return err
	}
	defer transport.client.CloseIdleConnections()
	workspace, err := resourceWorkspace(ctx, transport, "")
	if err != nil {
		return err
	}
	data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/workspaces/"+workspace.ID+"/work?limit=100", "", "", http.StatusOK)
	if err != nil {
		return err
	}
	var page struct {
		Data []canonicalReadTask `json:"data"`
		Next *string             `json:"next_cursor"`
	}
	if json.Unmarshal(data, &page) != nil || page.Data == nil || len(page.Data) > 100 {
		return newExitError(ExitOperation, "My Work response is incomplete")
	}
	for _, item := range page.Data {
		if !canonicalIdentifier(item.ID) || item.WorkspaceID != workspace.ID || item.Code == "" || item.Title == "" || !canonicalPositiveDecimal(item.Revision) {
			return newExitError(ExitOperation, "My Work identity is incomplete")
		}
		if _, err := fmt.Fprintf(output, "%s  %s\n", safeInline(item.Code), safeInline(item.Title)); err != nil {
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

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type cliWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

type cliProject struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Revision int64  `json:"revision"`
	URL      string `json:"url"`
}

type cliDocument struct {
	Path       string `json:"path"`
	Title      string `json:"title"`
	Visibility string `json:"visibility"`
	Revision   int64  `json:"revision"`
	URL        string `json:"url"`
	Body       string `json:"body"`
	Truncated  bool   `json:"truncated"`
}

func safeInline(value string) string {
	return strings.Map(func(character rune) rune {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) {
			return ' '
		}
		return character
	}, value)
}

func safeBlock(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return character
		}
		if character == '\r' {
			return '\n'
		}
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) {
			return '�'
		}
		return character
	}, value)
}

func newWorkspaceCommand(opts *options, client *http.Client) *cobra.Command {
	command := &cobra.Command{
		Use:   "workspace",
		Short: "Inspect and switch Workspaces",
		Args:  noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runWorkspaceCurrent(command.Context(), command.OutOrStdout(), opts, client, false)
		},
	}
	var cursor string
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List available Workspaces", Args: noArgs, RunE: func(command *cobra.Command, _ []string) error {
		return runWorkspaceList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, cursor)
	}}
	list.Flags().StringVar(&cursor, "cursor", "", "Opaque continuation cursor from Workspace discovery")
	current := &cobra.Command{Use: "current", Aliases: []string{"cur"}, Short: "Print the current Workspace", Args: noArgs, RunE: func(command *cobra.Command, _ []string) error {
		return runWorkspaceCurrent(command.Context(), command.OutOrStdout(), opts, client, false)
	}}
	show := &cobra.Command{Use: "show", Aliases: []string{"view"}, Short: "Show the current Workspace", Args: noArgs, RunE: func(command *cobra.Command, _ []string) error {
		return runWorkspaceCurrent(command.Context(), command.OutOrStdout(), opts, client, true)
	}}
	switchCommand := &cobra.Command{Use: "switch <workspace-slug>", Aliases: []string{"use", "sw"}, Short: "Switch the interactive CLI credential to a Workspace", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return runWorkspaceSwitch(command.Context(), command.OutOrStdout(), opts, client, args[0])
	}}
	command.AddCommand(list, current, show, switchCommand)
	return command
}

func newProjectCommand(opts *options, client *http.Client) *cobra.Command {
	command := &cobra.Command{
		Use:   "project [project-code]",
		Short: "Inspect Projects and their Tasks",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			code := ""
			if len(args) == 1 {
				code = args[0]
			}
			return runProjectShow(command.Context(), command.OutOrStdout(), opts, client, code)
		},
	}
	var cursor, documentCursor string
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List Projects in the current Workspace", Args: noArgs, RunE: func(command *cobra.Command, _ []string) error {
		return runProjectList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, cursor)
	}}
	list.Flags().StringVar(&cursor, "cursor", "", "Opaque continuation cursor from Project discovery")
	current := &cobra.Command{Use: "current", Aliases: []string{"cur"}, Short: "Print the selected Project code", Args: noArgs, RunE: func(command *cobra.Command, _ []string) error {
		code, err := currentProject(command.Context(), opts, client)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(command.OutOrStdout(), code)
		return err
	}}
	show := &cobra.Command{Use: "show [project-code]", Aliases: []string{"view"}, Short: "Show a Project", Args: cobra.MaximumNArgs(1), RunE: func(command *cobra.Command, args []string) error {
		code := ""
		if len(args) == 1 {
			code = args[0]
		}
		return runProjectShow(command.Context(), command.OutOrStdout(), opts, client, code)
	}}
	switchCommand := &cobra.Command{Use: "switch <project-code>", Aliases: []string{"use", "sw"}, Short: "Select the current Project", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return runProjectSwitch(command.Context(), command.OutOrStdout(), opts, client, args[0])
	}}
	tasks := newProjectTaskListCommand(opts, client)
	documents := &cobra.Command{Use: "documents [project-code]", Aliases: []string{"docs", "doc"}, Short: "List a Project's Documents", Args: cobra.MaximumNArgs(1), RunE: func(command *cobra.Command, args []string) error {
		code := ""
		if len(args) == 1 {
			code = args[0]
		}
		return runDocumentList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, code, documentCursor, true)
	}}
	documents.Flags().StringVar(&documentCursor, "cursor", "", "Opaque continuation cursor from the same Project")
	urlCommand := &cobra.Command{Use: "url [project-code]", Short: "Print a Project's canonical browser URL", Args: cobra.MaximumNArgs(1), RunE: func(command *cobra.Command, args []string) error {
		code := ""
		if len(args) == 1 {
			code = args[0]
		}
		return runProjectURL(command.Context(), command.OutOrStdout(), opts, client, code, false)
	}}
	openCommand := &cobra.Command{Use: "open [project-code]", Short: "Open a Project in the browser", Args: cobra.MaximumNArgs(1), RunE: func(command *cobra.Command, args []string) error {
		code := ""
		if len(args) == 1 {
			code = args[0]
		}
		return runProjectURL(command.Context(), command.OutOrStdout(), opts, client, code, true)
	}}
	command.AddCommand(list, current, show, switchCommand, tasks, documents, urlCommand, openCommand)
	return command
}

func newDocumentCommand(opts *options, client *http.Client) *cobra.Command {
	command := &cobra.Command{
		Use:   "document [document-path]",
		Short: "Read Documents",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 1 {
				return runDocumentShow(command.Context(), command.OutOrStdout(), opts, client, args[0])
			}
			return runDocumentList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, "", "", false)
		},
	}
	var projectCode, cursor string
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List Documents in the current Workspace", Args: noArgs, RunE: func(command *cobra.Command, _ []string) error {
		return runDocumentList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, projectCode, cursor, false)
	}}
	list.Flags().StringVar(&cursor, "cursor", "", "Opaque continuation cursor from the same Document list")
	list.Flags().StringVarP(&projectCode, "project", "p", "", "Limit Documents to a Project code")
	show := &cobra.Command{Use: "show <document-path>", Aliases: []string{"view"}, Short: "Show a Document as text", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return runDocumentShow(command.Context(), command.OutOrStdout(), opts, client, args[0])
	}}
	urlCommand := &cobra.Command{Use: "url <document-path>", Short: "Print a Document's canonical browser URL", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return runDocumentURL(command.Context(), command.OutOrStdout(), opts, client, args[0], false)
	}}
	openCommand := &cobra.Command{Use: "open <document-path>", Short: "Open a Document in the browser", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return runDocumentURL(command.Context(), command.OutOrStdout(), opts, client, args[0], true)
	}}
	command.AddCommand(list, show, urlCommand, openCommand)
	return command
}

func currentWorkspace(ctx context.Context, opts *options, client *http.Client) (cliWorkspace, error) {
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return cliWorkspace{}, err
	}
	defer transport.client.CloseIdleConnections()
	workspace, err := resourceWorkspace(ctx, transport, "")
	if err != nil {
		return cliWorkspace{}, err
	}
	return workspaceDetails(ctx, transport, workspace)
}

func runWorkspaceCurrent(ctx context.Context, output io.Writer, opts *options, client *http.Client, detail bool) error {
	workspace, err := currentWorkspace(ctx, opts, client)
	if err != nil {
		return err
	}
	if detail {
		_, err = fmt.Fprintf(output, "# %s\n\nSlug: %s\n", safeInline(workspace.Name), safeInline(workspace.Slug))
		if err == nil && workspace.Role != "" {
			_, err = fmt.Fprintf(output, "Role: %s\n", safeInline(workspace.Role))
		}
	} else {
		_, err = fmt.Fprintln(output, safeInline(workspace.Slug))
	}
	return err
}

func runWorkspaceList(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, cursor string) error {
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	if !validReadCursor(cursor) || len(cursor) > 2048 {
		return newExitError(ExitArguments, "cursor is invalid or too long")
	}
	if strings.TrimSpace(os.Getenv("ASSIGN_TOKEN")) != "" {
		return newExitError(ExitAuthentication, "ASSIGN_TOKEN is Workspace-bound and cannot list Workspace memberships")
	}
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return err
	}
	defer transport.client.CloseIdleConnections()
	query := pageQuery("100", cursor)
	var wire json.RawMessage
	var page struct {
		Items   []cliWorkspace `json:"data"`
		Next    *string        `json:"next_cursor"`
		HasMore *bool          `json:"has_more"`
	}
	if err := transport.exchangeJSON(ctx, http.MethodGet, "/api/v1/dev/workspaces?"+query.Encode(), "Workspace", nil, &wire); err != nil {
		return err
	}
	if unmarshalResourcePage(wire, &page) != nil || len(page.Items) > 100 || page.HasMore == nil || *page.HasMore != (page.Next != nil) || !validNextCursor(page.Next, cursor) || (page.Next != nil && len(*page.Next) > 2048) {
		return newExitError(ExitOperation, "Workspace page is incomplete")
	}
	for _, workspace := range page.Items {
		if workspace.Slug == "" || workspace.Name == "" {
			return newExitError(ExitOperation, "Workspace identity is incomplete")
		}
	}
	for _, workspace := range page.Items {
		if _, err := fmt.Fprintf(output, "%s  %s\n", safeInline(workspace.Slug), safeInline(workspace.Name)); err != nil {
			return err
		}
	}
	writeReadContinuation(diagnostics, page.Next)
	return nil
}

func runWorkspaceSwitch(ctx context.Context, output io.Writer, opts *options, client *http.Client, slug string) error {
	if strings.TrimSpace(os.Getenv("ASSIGN_TOKEN")) != "" {
		return newExitError(ExitAuthentication, "ASSIGN_TOKEN is Workspace-bound and cannot switch Workspaces")
	}
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return err
	}
	defer transport.client.CloseIdleConnections()
	var result struct {
		Workspace    cliWorkspace `json:"workspace"`
		AccessToken  string       `json:"access_token"`
		RefreshToken string       `json:"refresh_token"`
		TokenType    string       `json:"token_type"`
		ExpiresIn    int64        `json:"expires_in"`
	}
	slug = strings.TrimSpace(slug)
	if err := transport.exchangeJSON(ctx, http.MethodPut, "/api/v1/dev/workspace", "Workspace", map[string]string{"slug": slug}, &result); err != nil {
		return err
	}
	if result.Workspace.Slug != slug || result.Workspace.Name == "" || result.AccessToken == "" || result.RefreshToken == "" || result.TokenType != "Bearer" || result.ExpiresIn < 1 || result.ExpiresIn > int64(time.Duration(1<<63-1)/time.Second) {
		return newExitError(ExitOperation, "Workspace switch response is incomplete")
	}
	if err := opts.credentials.Save(transport.host, storedCredential{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, ExpiresAt: time.Now().UTC().Add(time.Duration(result.ExpiresIn) * time.Second)}); err != nil {
		return newExitError(ExitOperation, "store switched CLI credential: %v", err)
	}
	_, err = fmt.Fprintln(output, safeInline(result.Workspace.Slug))
	return err
}

func runProjectList(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, cursor string) error {
	if !validReadCursor(cursor) {
		return newExitError(ExitArguments, "cursor is invalid or too long")
	}
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return err
	}
	defer transport.client.CloseIdleConnections()
	workspace, err := resourceWorkspace(ctx, transport, "")
	if err != nil {
		return err
	}
	items, next, err := projectResourcePage(ctx, transport, workspace.ID, "", cursor)
	if err != nil {
		return err
	}
	if !validNextCursor(next, cursor) {
		return newExitError(ExitOperation, "invalid Projects continuation")
	}
	for _, project := range items {
		if _, err := fmt.Fprintf(output, "%s  %s\n", safeInline(project.Key), safeInline(project.Name)); err != nil {
			return err
		}
	}
	writeReadContinuation(diagnostics, next)
	return nil
}

func currentProject(ctx context.Context, opts *options, client *http.Client) (string, error) {
	workspace, err := currentWorkspace(ctx, opts, client)
	if err != nil {
		return "", err
	}
	host, _ := validatedHost(opts.host)
	return loadSelectedProject(opts, host, workspace.Slug)
}

func loadSelectedProject(opts *options, host, slug string) (string, error) {
	code, err := opts.contexts.LoadProject(host, slug)
	if errors.Is(err, os.ErrNotExist) {
		return "", newExitError(ExitContext, "no current Project; run assign project switch <project-code>")
	}
	if err != nil {
		return "", newExitError(ExitOperation, "load Project context: %v", err)
	}
	return code, nil
}

func resolveProjectCode(ctx context.Context, opts *options, transport *operationTransport, workspace cliWorkspace, code string) (string, error) {
	if code = strings.ToUpper(strings.TrimSpace(code)); code != "" {
		return code, nil
	}
	workspace, err := workspaceDetails(ctx, transport, workspace)
	if err != nil {
		return "", err
	}
	return loadSelectedProject(opts, transport.host, workspace.Slug)
}

func getProject(ctx context.Context, opts *options, client *http.Client, code string) (cliProject, cliWorkspace, error) {
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return cliProject{}, cliWorkspace{}, err
	}
	defer transport.client.CloseIdleConnections()
	workspace, err := resourceWorkspace(ctx, transport, "")
	if err != nil {
		return cliProject{}, cliWorkspace{}, err
	}
	workspace, err = workspaceDetails(ctx, transport, workspace)
	if err != nil {
		return cliProject{}, cliWorkspace{}, err
	}
	code, err = resolveProjectCode(ctx, opts, transport, workspace, code)
	if err != nil {
		return cliProject{}, cliWorkspace{}, err
	}
	project, err := findProjectResource(ctx, transport, workspace.ID, code)
	if err != nil {
		return cliProject{}, cliWorkspace{}, err
	}
	result, err := projectNavigation(project, workspace)
	return result, workspace, err
}

func runProjectShow(ctx context.Context, output io.Writer, opts *options, client *http.Client, code string) error {
	project, _, err := getProject(ctx, opts, client, code)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "# %s  %s\n\nPath: %s\nURL: %s\nRevision: %d\n", safeInline(project.Code), safeInline(project.Name), safeInline(project.Path), safeInline(project.URL), project.Revision)
	return err
}

func runProjectURL(ctx context.Context, output io.Writer, opts *options, client *http.Client, code string, launch bool) error {
	project, _, err := getProject(ctx, opts, client, code)
	if err != nil {
		return err
	}
	return printOrOpenURL(output, opts, project.URL, launch)
}

func runProjectSwitch(ctx context.Context, output io.Writer, opts *options, client *http.Client, code string) error {
	project, workspace, err := getProject(ctx, opts, client, code)
	if err != nil {
		return err
	}
	host, _ := validatedHost(opts.host)
	if err := opts.contexts.SaveProject(host, workspace.Slug, project.Code); err != nil {
		return newExitError(ExitOperation, "store Project context: %v", err)
	}
	_, err = fmt.Fprintln(output, project.Code)
	return err
}

func runDocumentList(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, projectCode, cursor string, requireProject bool) error {
	if !validReadCursor(cursor) {
		return newExitError(ExitArguments, "cursor is invalid or too long")
	}
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return err
	}
	defer transport.client.CloseIdleConnections()
	workspace, err := resourceWorkspace(ctx, transport, "")
	if err != nil {
		return err
	}
	if requireProject {
		projectCode, err = resolveProjectCode(ctx, opts, transport, workspace, projectCode)
		if err != nil {
			return err
		}
	}
	query := pageQuery("100", cursor)
	if code := strings.ToUpper(strings.TrimSpace(projectCode)); code != "" {
		id, err := resolveCreateProject(ctx, transport, workspace.ID, code)
		if err != nil {
			return err
		}
		query.Set("project_id", id)
	}
	items, next, err := documentResourcePage(ctx, transport, workspace.ID, query)
	if err != nil {
		return err
	}
	if !validNextCursor(next, cursor) {
		return newExitError(ExitOperation, "invalid Documents continuation")
	}
	for _, item := range items {
		if _, err := fmt.Fprintf(output, "%s  %s\n", safeInline(item.Path), safeInline(item.Title)); err != nil {
			return err
		}
	}
	writeReadContinuation(diagnostics, next)
	return nil
}

func runDocumentShow(ctx context.Context, output io.Writer, opts *options, client *http.Client, path string) error {
	item, err := getDocument(ctx, opts, client, path)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "# %s\n\nPath: %s\nVisibility: %s\nURL: %s\nRevision: %d\n", safeInline(item.Title), safeInline(item.Path), safeInline(item.Visibility), safeInline(item.URL), item.Revision); err != nil {
		return err
	}
	if item.Body != "" {
		marker := ""
		if item.Truncated {
			marker = "\n\n[Document text truncated at 65,536 characters.]"
		}
		_, err := fmt.Fprintf(output, "\n%s%s\n", safeBlock(item.Body), marker)
		return err
	}
	return nil
}

func getDocument(ctx context.Context, opts *options, client *http.Client, path string) (cliDocument, error) {
	return readResourceDocument(ctx, opts, client, path, true)
}

func readResourceDocument(ctx context.Context, opts *options, client *http.Client, path string, readContent bool) (cliDocument, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return cliDocument{}, newExitError(ExitArguments, "Document path is required")
	}
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return cliDocument{}, err
	}
	defer transport.client.CloseIdleConnections()
	workspace, err := resourceWorkspace(ctx, transport, "")
	if err != nil {
		return cliDocument{}, err
	}
	query := url.Values{"path": {path}, "limit": {"1"}}
	items, next, err := documentResourcePage(ctx, transport, workspace.ID, query)
	if err != nil {
		return cliDocument{}, err
	}
	if len(items) > 1 || next != nil {
		return cliDocument{}, newExitError(ExitOperation, "exact Document response is ambiguous or incomplete")
	}
	if len(items) == 0 {
		return cliDocument{}, newExitError(ExitContext, "Document is unavailable in this Workspace")
	}
	resource := items[0]
	if resource.Path != path {
		return cliDocument{}, newExitError(ExitOperation, "Document path does not match")
	}
	workspace, err = workspaceDetails(ctx, transport, workspace)
	if err != nil {
		return cliDocument{}, err
	}
	if err = navigationURL(resource.WebURL, workspace, "documents", resource.Path); err != nil {
		return cliDocument{}, err
	}
	revision, err := strconv.ParseInt(resource.Revision, 10, 64)
	if err != nil || revision < 1 {
		return cliDocument{}, newExitError(ExitOperation, "Document revision is invalid")
	}
	body, truncated := "", false
	if readContent {
		body, truncated, err = documentMarkdown(ctx, transport, resource.ID)
		if err != nil {
			return cliDocument{}, err
		}
	}
	return cliDocument{Path: resource.Path, Title: resource.Title, Visibility: resource.Visibility, Revision: revision, URL: resource.WebURL, Body: body, Truncated: truncated}, nil
}

func runDocumentURL(ctx context.Context, output io.Writer, opts *options, client *http.Client, path string, launch bool) error {
	item, err := readResourceDocument(ctx, opts, client, path, false)
	if err != nil {
		return err
	}
	return printOrOpenURL(output, opts, item.URL, launch)
}

func printOrOpenURL(output io.Writer, opts *options, rawURL string, launch bool) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return newExitError(ExitOperation, "Assign returned an invalid browser URL")
	}
	if !launch {
		_, err = fmt.Fprintln(output, parsed.String())
		return err
	}
	opener := opts.openURL
	if opener == nil {
		opener = openBrowser
	}
	if err := opener(parsed.String()); err != nil {
		_, _ = fmt.Fprintln(output, parsed.String())
		return newExitError(ExitOperation, "open browser: %v", err)
	}
	return nil
}

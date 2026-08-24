package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type cliWorkspace struct {
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
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List available Workspaces", Args: noArgs, RunE: func(command *cobra.Command, _ []string) error {
		return runWorkspaceList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client)
	}}
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
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List Projects in the current Workspace", Args: noArgs, RunE: func(command *cobra.Command, _ []string) error {
		return runProjectList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client)
	}}
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
	tasks := &cobra.Command{Use: "tasks [project-code]", Aliases: []string{"t"}, Short: "List a Project's Tasks", Args: cobra.MaximumNArgs(1), RunE: func(command *cobra.Command, args []string) error {
		code := ""
		if len(args) == 1 {
			code = args[0]
		}
		return runProjectTasks(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, code)
	}}
	documents := &cobra.Command{Use: "documents [project-code]", Aliases: []string{"docs", "doc"}, Short: "List a Project's Documents", Args: cobra.MaximumNArgs(1), RunE: func(command *cobra.Command, args []string) error {
		code := ""
		if len(args) == 1 {
			code = args[0]
		}
		code, err := resolveProjectCode(command.Context(), opts, client, code)
		if err != nil {
			return err
		}
		return runDocumentList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, code)
	}}
	command.AddCommand(list, current, show, switchCommand, tasks, documents)
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
			return runDocumentList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, "")
		},
	}
	var projectCode string
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List Documents in the current Workspace", Args: noArgs, RunE: func(command *cobra.Command, _ []string) error {
		return runDocumentList(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, projectCode)
	}}
	list.Flags().StringVarP(&projectCode, "project", "p", "", "Limit Documents to a Project code")
	show := &cobra.Command{Use: "show <document-path>", Aliases: []string{"view"}, Short: "Show a Document as text", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return runDocumentShow(command.Context(), command.OutOrStdout(), opts, client, args[0])
	}}
	command.AddCommand(list, show)
	return command
}

func authorizedJSON(ctx context.Context, opts *options, client *http.Client, method, path string, body any, target any) error {
	host, err := validatedHost(opts.host)
	if err != nil {
		return newExitError(ExitArguments, "%s", err)
	}
	token, err := authorizationToken(ctx, host, client, opts.credentials)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		encoded, encodeErr := json.Marshal(body)
		if encodeErr != nil {
			return newExitError(ExitOperation, "encode request")
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, host+path, reader)
	if err != nil {
		return newExitError(ExitOperation, "prepare request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return newExitError(ExitOperation, "request Assign: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return newExitError(ExitAuthentication, "authentication was rejected; run assign login again")
	}
	if response.StatusCode == http.StatusForbidden {
		return newExitError(ExitAuthentication, "this command requires interactive CLI login")
	}
	if response.StatusCode == http.StatusNotFound {
		return newExitError(ExitContext, "the requested Workspace, Project, or Document was not found")
	}
	if response.StatusCode != http.StatusOK {
		return newExitError(ExitOperation, "Assign request failed with HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024))
	if err := decoder.Decode(target); err != nil {
		return newExitError(ExitOperation, "decode Assign response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return newExitError(ExitOperation, "decode Assign response")
	}
	return nil
}

func currentWorkspace(ctx context.Context, opts *options, client *http.Client) (cliWorkspace, error) {
	var workspace cliWorkspace
	err := authorizedJSON(ctx, opts, client, http.MethodGet, "/api/v1/cli/workspace", nil, &workspace)
	if err == nil && (workspace.Slug == "" || workspace.Name == "") {
		err = newExitError(ExitOperation, "Workspace response is incomplete")
	}
	return workspace, err
}

func runWorkspaceCurrent(ctx context.Context, output io.Writer, opts *options, client *http.Client, detail bool) error {
	workspace, err := currentWorkspace(ctx, opts, client)
	if err != nil {
		return err
	}
	if detail {
		_, err = fmt.Fprintf(output, "# %s\n\nSlug: %s\nRole: %s\n", safeInline(workspace.Name), safeInline(workspace.Slug), safeInline(workspace.Role))
	} else {
		_, err = fmt.Fprintln(output, workspace.Slug)
	}
	return err
}

func runWorkspaceList(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client) error {
	var page struct {
		Items   []cliWorkspace `json:"items"`
		HasMore bool           `json:"has_more"`
	}
	if err := authorizedJSON(ctx, opts, client, http.MethodGet, "/api/v1/cli/workspaces?limit=100", nil, &page); err != nil {
		return err
	}
	for _, workspace := range page.Items {
		if _, err := fmt.Fprintf(output, "%s  %s\n", safeInline(workspace.Slug), safeInline(workspace.Name)); err != nil {
			return err
		}
	}
	if page.HasMore {
		_, _ = fmt.Fprintln(diagnostics, "assign: more Workspaces are available; only the first 100 are shown")
	}
	return nil
}

func runWorkspaceSwitch(ctx context.Context, output io.Writer, opts *options, client *http.Client, slug string) error {
	if strings.TrimSpace(os.Getenv("ASSIGN_TOKEN")) != "" {
		return newExitError(ExitAuthentication, "ASSIGN_TOKEN is Workspace-bound and cannot switch Workspaces")
	}
	var result struct {
		Workspace    cliWorkspace `json:"workspace"`
		AccessToken  string       `json:"access_token"`
		RefreshToken string       `json:"refresh_token"`
		ExpiresIn    int64        `json:"expires_in"`
	}
	if err := authorizedJSON(ctx, opts, client, http.MethodPut, "/api/v1/cli/workspace", map[string]string{"slug": strings.TrimSpace(slug)}, &result); err != nil {
		return err
	}
	if result.Workspace.Slug == "" || result.AccessToken == "" || result.RefreshToken == "" || result.ExpiresIn < 1 {
		return newExitError(ExitOperation, "Workspace switch response is incomplete")
	}
	host, _ := validatedHost(opts.host)
	if err := opts.credentials.Save(host, storedCredential{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, ExpiresAt: time.Now().UTC().Add(time.Duration(result.ExpiresIn) * time.Second)}); err != nil {
		return newExitError(ExitOperation, "store switched CLI credential: %v", err)
	}
	_, err := fmt.Fprintln(output, result.Workspace.Slug)
	return err
}

func runProjectList(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client) error {
	var page struct {
		Items   []cliProject `json:"items"`
		HasMore bool         `json:"has_more"`
	}
	if err := authorizedJSON(ctx, opts, client, http.MethodGet, "/api/v1/cli/projects?limit=100", nil, &page); err != nil {
		return err
	}
	for _, project := range page.Items {
		if _, err := fmt.Fprintf(output, "%s  %s\n", safeInline(project.Code), safeInline(project.Name)); err != nil {
			return err
		}
	}
	if page.HasMore {
		_, _ = fmt.Fprintln(diagnostics, "assign: more Projects are available; only the first 100 are shown")
	}
	return nil
}

func currentProject(ctx context.Context, opts *options, client *http.Client) (string, error) {
	workspace, err := currentWorkspace(ctx, opts, client)
	if err != nil {
		return "", err
	}
	host, _ := validatedHost(opts.host)
	code, err := opts.contexts.LoadProject(host, workspace.Slug)
	if errors.Is(err, os.ErrNotExist) {
		return "", newExitError(ExitContext, "no current Project; run assign project switch <project-code>")
	}
	if err != nil {
		return "", newExitError(ExitOperation, "load Project context: %v", err)
	}
	return code, nil
}

func resolveProjectCode(ctx context.Context, opts *options, client *http.Client, code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code != "" {
		return code, nil
	}
	return currentProject(ctx, opts, client)
}

func getProject(ctx context.Context, opts *options, client *http.Client, code string) (cliProject, error) {
	code, err := resolveProjectCode(ctx, opts, client, code)
	if err != nil {
		return cliProject{}, err
	}
	var project cliProject
	err = authorizedJSON(ctx, opts, client, http.MethodGet, "/api/v1/cli/projects/"+url.PathEscape(code), nil, &project)
	if err == nil && (project.Code == "" || project.Name == "" || project.URL == "") {
		err = newExitError(ExitOperation, "Project response is incomplete")
	}
	return project, err
}

func runProjectShow(ctx context.Context, output io.Writer, opts *options, client *http.Client, code string) error {
	project, err := getProject(ctx, opts, client, code)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "# %s  %s\n\nPath: %s\nURL: %s\nRevision: %d\n", safeInline(project.Code), safeInline(project.Name), safeInline(project.Path), safeInline(project.URL), project.Revision)
	return err
}

func runProjectSwitch(ctx context.Context, output io.Writer, opts *options, client *http.Client, code string) error {
	project, err := getProject(ctx, opts, client, code)
	if err != nil {
		return err
	}
	workspace, err := currentWorkspace(ctx, opts, client)
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

func runProjectTasks(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, code string) error {
	code, err := resolveProjectCode(ctx, opts, client, code)
	if err != nil {
		return err
	}
	var page cliMyWorkResponse
	if err := authorizedJSON(ctx, opts, client, http.MethodGet, "/api/v1/cli/projects/"+url.PathEscape(code)+"/tasks?limit=100", nil, &page); err != nil {
		return err
	}
	for _, task := range page.Items {
		if _, err := fmt.Fprintf(output, "%s  %s\n", safeInline(task.Code), safeInline(task.Title)); err != nil {
			return err
		}
	}
	if page.HasMore {
		_, _ = fmt.Fprintln(diagnostics, "assign: more Tasks are available; only the first 100 are shown")
	}
	return nil
}

func runDocumentList(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, projectCode string) error {
	query := url.Values{"limit": []string{"100"}}
	if code := strings.ToUpper(strings.TrimSpace(projectCode)); code != "" {
		query.Set("project_code", code)
	}
	var page struct {
		Items   []cliDocument `json:"items"`
		HasMore bool          `json:"has_more"`
	}
	if err := authorizedJSON(ctx, opts, client, http.MethodGet, "/api/v1/cli/documents?"+query.Encode(), nil, &page); err != nil {
		return err
	}
	for _, item := range page.Items {
		if _, err := fmt.Fprintf(output, "%s  %s\n", safeInline(item.Path), safeInline(item.Title)); err != nil {
			return err
		}
	}
	if page.HasMore {
		_, _ = fmt.Fprintln(diagnostics, "assign: more Documents are available; only the first 100 are shown")
	}
	return nil
}

func runDocumentShow(ctx context.Context, output io.Writer, opts *options, client *http.Client, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return newExitError(ExitArguments, "Document path is required")
	}
	var item cliDocument
	if err := authorizedJSON(ctx, opts, client, http.MethodGet, "/api/v1/cli/documents/"+url.PathEscape(path), nil, &item); err != nil {
		return err
	}
	if item.Path == "" || item.Title == "" || item.URL == "" {
		return newExitError(ExitOperation, "Document response is incomplete")
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

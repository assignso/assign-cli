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
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

const maxTaskTextBytes = 256 * 1024

func taskCode(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "", newExitError(ExitArguments, "Task code must not be empty")
	}
	return value, nil
}

// Read one bounded input before making any remote request. Never evaluate it.
func taskText(value string, input io.Reader) (string, error) {
	if value == "-" {
		data, err := io.ReadAll(io.LimitReader(input, maxTaskTextBytes+1))
		if err != nil {
			return "", newExitError(ExitOperation, "read stdin: %v", err)
		}
		value = string(data)
	}
	if len(value) > maxTaskTextBytes {
		return "", newExitError(ExitArguments, "text exceeds 256 KiB")
	}
	if !utf8.ValidString(value) {
		return "", newExitError(ExitArguments, "text must be valid UTF-8")
	}
	for _, r := range value {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			return "", newExitError(ExitArguments, "text must not contain binary or terminal control characters")
		}
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	encoded, _ := json.Marshal(value)
	if len(encoded) > maxTaskTextBytes+4*1024 {
		return "", newExitError(ExitArguments, "encoded text exceeds the Task write payload limit")
	}
	if strings.TrimSpace(value) == "" {
		return "", newExitError(ExitArguments, "text must not be empty")
	}
	return value, nil
}

func mutationKey(value string) (string, error) {
	if value == "" {
		return newIdempotencyKey()
	}
	if len(value) < 16 || len(value) > 200 {
		return "", newExitError(ExitArguments, "--idempotency-key must contain 16–200 printable ASCII characters without spaces")
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return "", newExitError(ExitArguments, "--idempotency-key must contain 16–200 printable ASCII characters without spaces")
		}
	}
	return value, nil
}

// There are no automatic mutation retries. The caller retains the same key and
// identical arguments to reconcile an uncertain outcome with Core.
func taskMutation(ctx context.Context, opts *options, client *http.Client, path string, body any, key string, revision int64, status int, target any) error {
	host, err := validatedHost(opts.host)
	if err != nil {
		return newExitError(ExitArguments, "%s", err)
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return newExitError(ExitOperation, "encode Task request")
		}
		if len(data) > maxTaskTextBytes+8*1024 {
			return newExitError(ExitArguments, "encoded Task write exceeds the payload limit")
		}
		reader = bytes.NewReader(data)
	}
	token, err := authorizationToken(ctx, host, client, opts.credentials)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, host+path, reader)
	if err != nil {
		return newExitError(ExitOperation, "prepare Task request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", key)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if revision > 0 {
		request.Header.Set("If-Match", fmt.Sprintf("\"%d\"", revision))
	}
	uncertain := func() error {
		return newExitError(ExitOperation, "Task write outcome is uncertain; reconcile by retrying identical arguments with --idempotency-key %s (including the original revision, if supplied)", key)
	}
	writeClient := *client
	writeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := writeClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("%w; %s", err, uncertain())
		}
		return uncertain()
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return newExitError(ExitAuthentication, "authentication was rejected; run assign login again")
	case http.StatusForbidden:
		return newExitError(ExitAuthentication, "the credential does not have permission for this Task write")
	case http.StatusNotFound:
		return newExitError(ExitContext, "the requested Project or Task was not found")
	case http.StatusConflict:
		if err := lifecycleConflict(response.Body); err != nil {
			return err
		}
		return newExitError(ExitConflict, "Task write conflict; reconcile current state before a new operation; --idempotency-key %s is retained only for an identical retry", key)
	}
	if response.StatusCode >= 500 {
		return uncertain()
	}
	if response.StatusCode != status {
		return newExitError(ExitOperation, "Task write failed with HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024))
	if err := decoder.Decode(target); err != nil {
		return uncertain()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return uncertain()
	}
	return nil
}

func newTaskContextCommand(opts *options, client *http.Client) *cobra.Command {
	var compact, full bool
	var milestoneScope, workspaceID string
	command := &cobra.Command{Use: "context <task-code>", Aliases: []string{"related"}, Short: "Print bounded, untrusted Task context and related Tasks as Markdown", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		code, err := taskCode(args[0])
		if err != nil {
			return err
		}
		profile, cap := "compact", 16*1024
		if full {
			profile, cap = "full", 64*1024
		}
		ctx, cancel := context.WithTimeout(command.Context(), operationDeadline)
		defer cancel()
		transport, workspace, item, err := readResourceTask(ctx, opts, client, code, workspaceID)
		if err != nil {
			return err
		}
		defer transport.client.CloseIdleConnections()
		var fence *milestoneContextFence
		if command.Flags().Changed("milestone") {
			fence, err = readResourceMilestoneContextFence(ctx, transport, workspace.ID, code, milestoneScope)
			if err != nil {
				return err
			}
			if fence.task.ID != item.ID {
				return newExitError(ExitOperation, "Task identity changed while acquiring Milestone scope")
			}
		}
		var packet struct {
			Format    string   `json:"format"`
			TaskCode  string   `json:"task_code"`
			Profile   string   `json:"profile"`
			Markdown  string   `json:"markdown"`
			Truncated *bool    `json:"truncated"`
			Omissions []string `json:"omissions"`
		}
		data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/tasks/"+item.ID+"/context?format=markdown&profile="+profile, "", "", http.StatusOK)
		if err != nil {
			return err
		}
		if json.Unmarshal(data, &packet) != nil {
			return newExitError(ExitOperation, "Task context representation is invalid")
		}
		if packet.Format != "markdown" || packet.TaskCode != code || packet.Profile != profile || packet.Markdown == "" || packet.Truncated == nil || len(packet.Markdown) > cap {
			return newExitError(ExitOperation, "Task context response is incomplete or exceeds its profile limit")
		}
		if fence != nil {
			current, err := readResourceMilestoneContextFence(ctx, transport, workspace.ID, code, milestoneScope)
			if err != nil {
				return err
			}
			if !fence.sameObservation(current) {
				return newExitError(ExitOperation, "Milestone or Task scope changed while reading context; discard this packet and refresh selection")
			}
			packet.Markdown = fence.markdownHeader() + packet.Markdown
			if len(packet.Markdown) > cap {
				return newExitError(ExitOperation, "scoped Task context exceeds its profile limit; request --full or read smaller canonical resources")
			}
		}
		if *packet.Truncated {
			_, _ = fmt.Fprintln(command.ErrOrStderr(), "assign: Task context is truncated; missing context must not imply readiness")
		}
		for _, omission := range packet.Omissions {
			_, _ = fmt.Fprintf(command.ErrOrStderr(), "assign: Task context omission: %s\n", safeInline(omission))
		}
		_, err = io.WriteString(command.OutOrStdout(), safeBlock(packet.Markdown))
		return err
	}}
	command.Flags().StringVar(&workspaceID, "workspace-id", "", "Credential Workspace UUID for personal/service tokens")
	command.Flags().BoolVar(&compact, "compact", false, "Use the compact 16 KiB profile (default)")
	command.Flags().BoolVar(&full, "full", false, "Use the full 64 KiB profile")
	command.Flags().StringVar(&milestoneScope, "milestone", "", "Require observed membership in a Milestone code before and after the context read")
	command.MarkFlagsMutuallyExclusive("compact", "full")
	return command
}

func newTaskCommentCommand(opts *options, client *http.Client) *cobra.Command {
	var key, workspaceID, taskID string
	command := &cobra.Command{Use: "comment <task-code> <message|->", Short: "Add an explicit Task comment; - reads bounded UTF-8 stdin", Args: exactArgs(2), RunE: func(command *cobra.Command, args []string) error {
		code, err := taskCode(args[0])
		if err != nil {
			return err
		}
		text, err := taskText(args[1], command.InOrStdin())
		if err != nil {
			return err
		}
		requestKey, err := mutationKey(key)
		if err != nil {
			return err
		}
		if taskID != "" && key == "" {
			return newExitError(ExitArguments, "recovery requires the original --idempotency-key")
		}
		ctx, cancel := context.WithTimeout(command.Context(), operationDeadline)
		defer cancel()
		if err := createResourceComment(ctx, command.ErrOrStderr(), opts, client, code, text, requestKey, workspaceID, taskID); err != nil {
			return err
		}
		_, err = fmt.Fprintln(command.OutOrStdout(), code)
		return err
	}}
	command.Flags().StringVar(&key, "idempotency-key", "", "Retain a 16–200 character key for identical retries; generated when omitted")
	command.Flags().StringVar(&workspaceID, "workspace-id", "", "Credential Workspace UUID and original recovery target")
	command.Flags().StringVar(&taskID, "task-id", "", "Original Task UUID printed before an uncertain comment")
	return command
}

type taskPageOptions struct {
	cursor string
	limit  int
	field  string
	format string
}

func (p taskPageOptions) validate() error {
	if p.format != "" && p.format != "text" && p.format != "json" {
		return newExitError(ExitArguments, "--format must be text or json")
	}
	if p.format == "json" && p.field != "" {
		return newExitError(ExitArguments, "--format json cannot be combined with --field")
	}
	if p.limit < 1 || p.limit > 100 {
		return newExitError(ExitArguments, "--limit must be between 1 and 100")
	}
	if len(p.cursor) > 8192 || safeInline(p.cursor) != p.cursor {
		return newExitError(ExitArguments, "cursor is invalid or too long")
	}
	if p.field != "" && p.field != "code" && p.field != "next_cursor" && p.field != "has_more" {
		return newExitError(ExitArguments, "--field must be code, next_cursor, or has_more")
	}
	return nil
}

func newProjectTaskListCommand(opts *options, client *http.Client) *cobra.Command {
	page := taskPageOptions{limit: 100}
	command := &cobra.Command{Use: "tasks [project-code]", Aliases: []string{"t"}, Short: "Read one bounded page of a Project's Tasks", Args: cobra.MaximumNArgs(1), RunE: func(command *cobra.Command, args []string) error {
		code := ""
		if len(args) == 1 {
			code = args[0]
		}
		return runProjectTaskPage(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, code, page)
	}}
	command.Flags().StringVar(&page.format, "format", "text", "Page output: text or bounded JSON with Tasks and continuation")
	command.Flags().IntVar(&page.limit, "limit", 100, "Tasks per page (1–100)")
	command.Flags().StringVar(&page.cursor, "cursor", "", "Opaque continuation cursor from the same Project")
	command.Flags().StringVar(&page.field, "field", "", "Print code values, next_cursor (blank at end), or has_more")
	return command
}

type commentPageOptions struct {
	workspaceID string
	cursor      string
	limit       int
}

func (p commentPageOptions) validate() error {
	if p.limit < 1 || p.limit > 100 {
		return newExitError(ExitArguments, "--limit must be between 1 and 100")
	}
	if len(p.cursor) > 4096 || safeInline(p.cursor) != p.cursor {
		return newExitError(ExitArguments, "cursor is invalid or too long")
	}
	return nil
}

type cliComment struct {
	BodyMarkdown string  `json:"body_markdown"`
	Actor        string  `json:"actor"`
	ActorName    *string `json:"actor_name"`
	Revision     int64   `json:"revision"`
	CreatedAt    string  `json:"created_at"`
}

func newTaskCommentsCommand(opts *options, client *http.Client) *cobra.Command {
	page := commentPageOptions{limit: 50}
	command := &cobra.Command{Use: "comments <task-code>", Short: "Read one bounded page of a Task's comments", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return runTaskCommentsPage(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, args[0], page)
	}}
	command.Flags().IntVar(&page.limit, "limit", 50, "Comments per page (1–100)")
	command.Flags().StringVar(&page.cursor, "cursor", "", "Opaque continuation cursor from the same Task")
	command.Flags().StringVar(&page.workspaceID, "workspace-id", "", "Credential Workspace UUID for personal/service tokens")
	return command
}

// Read-only: one bounded page, no fetch-all, no retries, no writes.
func runTaskCommentsPage(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, code string, options commentPageOptions) error {
	if err := options.validate(); err != nil {
		return err
	}
	code, err := taskCode(code)
	if err != nil {
		return err
	}
	query := url.Values{"limit": {fmt.Sprint(options.limit)}}
	if options.cursor != "" {
		query.Set("cursor", options.cursor)
	}
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, workspace, item, err := readResourceTask(ctx, opts, client, code, options.workspaceID)
	if err != nil {
		return err
	}
	defer transport.client.CloseIdleConnections()
	data, headers, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/tasks/"+item.ID+"/comments?"+query.Encode(), "", "", http.StatusOK)
	if err != nil {
		return err
	}
	var canonical struct {
		Data       []canonicalCLIComment `json:"data"`
		NextCursor *string               `json:"next_cursor"`
		HasMore    *bool                 `json:"has_more"`
	}
	if json.Unmarshal(data, &canonical) != nil || canonical.Data == nil || !strongTaskValidator(headers.Get("ETag")) {
		return newExitError(ExitOperation, "Comment page representation is incomplete")
	}
	page := struct {
		Items      []cliComment
		NextCursor *string
		HasMore    *bool
	}{Items: make([]cliComment, 0, len(canonical.Data)), NextCursor: canonical.NextCursor, HasMore: canonical.HasMore}
	for _, comment := range canonical.Data {
		projected, e := comment.legacy(workspace.ID, item.ID)
		if e != nil {
			return e
		}
		page.Items = append(page.Items, projected)
	}
	if page.Items == nil || page.HasMore == nil {
		return newExitError(ExitOperation, "comment page response is incomplete")
	}
	if len(page.Items) > options.limit || (*page.HasMore && (page.NextCursor == nil || *page.NextCursor == "")) || (!*page.HasMore && page.NextCursor != nil && *page.NextCursor != "") {
		return newExitError(ExitOperation, "comment page response has invalid continuation or exceeds its limit")
	}
	if page.NextCursor != nil && (len(*page.NextCursor) > 4096 || safeInline(*page.NextCursor) != *page.NextCursor) {
		return newExitError(ExitOperation, "comment page response has an invalid cursor")
	}
	for _, item := range page.Items {
		if item.Actor == "" || item.CreatedAt == "" || item.Revision < 1 {
			return newExitError(ExitOperation, "comment page response is incomplete")
		}
	}
	for index, item := range page.Items {
		display := safeInline(item.Actor)
		if item.ActorName != nil && *item.ActorName != "" {
			display = safeInline(*item.ActorName)
		}
		body := safeBlock(item.BodyMarkdown)
		if item.BodyMarkdown == "" {
			body = "[deleted comment]"
		}
		if index > 0 {
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(output, "## %s · %s\n\n%s\n", display, safeInline(item.CreatedAt), body); err != nil {
			return err
		}
	}
	if *page.HasMore {
		_, _ = fmt.Fprintf(diagnostics, "assign: more comments are available; continue with --cursor %s\n", *page.NextCursor)
	}
	return nil
}

func runProjectTaskPage(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, code string, options taskPageOptions) error {
	if err := options.validate(); err != nil {
		return err
	}
	code, err := resolveProjectCode(ctx, opts, client, code)
	if err != nil {
		return err
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
	project, err := resolveCreateProject(ctx, transport, workspace.ID, code)
	if err != nil {
		return err
	}
	query := url.Values{"project_id": {project}, "limit": {fmt.Sprint(options.limit)}, "state": {"all"}, "include_archived": {"false"}}
	if options.cursor != "" {
		query.Set("cursor", options.cursor)
	}
	data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/workspaces/"+workspace.ID+"/tasks?"+query.Encode(), "", "", http.StatusOK)
	if err != nil {
		return err
	}
	var wire struct {
		Data       []canonicalReadTask `json:"data"`
		NextCursor *string             `json:"next_cursor"`
		Coverage   string              `json:"coverage"`
		Included   struct {
			Statuses []struct {
				ID       string `json:"id"`
				Category string `json:"category"`
			} `json:"statuses"`
		} `json:"included"`
	}
	if unmarshalResourcePage(data, &wire) != nil || wire.Data == nil || len(wire.Data) > options.limit {
		return newExitError(ExitOperation, "canonical Task page is incomplete")
	}
	if wire.Coverage != "" && wire.Coverage != "complete" {
		return newExitError(ExitOperation, "Task page is partial; absence is unknown")
	}
	more := wire.NextCursor != nil
	page := struct {
		Items      []cliTask
		NextCursor *string
		HasMore    *bool
	}{Items: make([]cliTask, 0, len(wire.Data)), NextCursor: wire.NextCursor, HasMore: &more}
	if wire.NextCursor != nil && (*wire.NextCursor == "" || len(*wire.NextCursor) > 8192 || safeInline(*wire.NextCursor) != *wire.NextCursor) {
		return newExitError(ExitOperation, "Task continuation is invalid")
	}
	for _, item := range wire.Data {
		if !canonicalIdentifier(item.ID) || item.WorkspaceID != workspace.ID || item.ProjectID != project {
			return newExitError(ExitOperation, "Task identity does not match the selected Project")
		}
		for _, status := range wire.Included.Statuses {
			if status.ID == item.StatusID {
				item.Category = status.Category
			}
		}
		projected, err := canonicalTaskNavigation(item, workspace)
		if err != nil {
			return err
		}
		page.Items = append(page.Items, projected)
	}
	if options.format == "json" {
		for _, item := range page.Items {
			if item.Code == "" || item.Title == "" || item.ProjectCode == "" || item.StatusCategory == "" || item.Revision < 1 || item.URL == "" {
				return newExitError(ExitOperation, "Task page response is incomplete")
			}
		}
		return json.NewEncoder(output).Encode(struct {
			Schema     string    `json:"schema"`
			Items      []cliTask `json:"items"`
			NextCursor *string   `json:"next_cursor"`
			HasMore    bool      `json:"has_more"`
		}{"assign.cli.task-page.v1", page.Items, page.NextCursor, *page.HasMore})
	}
	switch options.field {
	case "next_cursor":
		cursor := ""
		if page.NextCursor != nil {
			cursor = *page.NextCursor
		}
		_, err = fmt.Fprintln(output, cursor)
		return err
	case "has_more":
		_, err = fmt.Fprintln(output, *page.HasMore)
		return err
	}
	for _, item := range page.Items {
		if item.Code == "" || item.Title == "" {
			return newExitError(ExitOperation, "Task page response is incomplete")
		}
	}
	for _, item := range page.Items {
		if options.field == "code" {
			_, err = fmt.Fprintln(output, safeInline(item.Code))
		} else {
			_, err = fmt.Fprintf(output, "%s  %s\n", safeInline(item.Code), safeInline(item.Title))
		}
		if err != nil {
			return err
		}
	}
	if *page.HasMore {
		_, _ = fmt.Fprintf(diagnostics, "assign: more Tasks are available; continue with --cursor %s\n", *page.NextCursor)
	}
	return nil
}

// lifecycleConflictError marks a 409 whose body names a lifecycle rule rather
// than a stale Task revision, so callers keep the server's explanation.
type lifecycleConflictError struct{ *exitError }

func (e lifecycleConflictError) Unwrap() error { return e.exitError }

// lifecycleConflict decodes a bounded 409 body. Unknown or unreadable bodies
// return nil and keep the generic revision-conflict handling, which also covers
// servers that predate lifecycle error codes.
func lifecycleConflict(body io.Reader) error {
	var payload struct {
		Reason string `json:"reason"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
		Choices []struct {
			StatusID string `json:"status_id"`
			Name     string `json:"name"`
		} `json:"choices"`
	}
	if json.NewDecoder(io.LimitReader(body, 64*1024)).Decode(&payload) != nil {
		return nil
	}
	var message string
	code := payload.Reason
	if code == "" {
		code = payload.Error.Code
	}
	switch code {
	case "completion_target_required":
		message = "several Statuses count as completed for this Task; completion needs an explicit destination Status; select an applicable --target-status-id before a new operation"
		for _, choice := range payload.Choices {
			if canonicalIdentifier(choice.StatusID) {
				message += "\n  " + choice.StatusID + "  " + safeInline(choice.Name)
			}
		}
	case "workflow_status_unavailable":
		message = "the Project has no Status for this action; ask a Workspace administrator to review the Project lifecycle"
	case "lifecycle_migration_pending":
		message = "a lifecycle migration is still applying; retry once it completes"
	case "task_inactive":
		message = "the Task is archived or otherwise inactive and cannot change Status"
	default:
		return nil
	}
	return lifecycleConflictError{newExitError(ExitConflict, "%s (%s)", message, code).(*exitError)}
}

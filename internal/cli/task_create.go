package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	canonicalrichtext "github.com/assignso/assign-cli/internal/generated/richtext"
	"github.com/spf13/cobra"
)

const (
	// FromMarkdown already enforces the canonical document byte/complexity
	// limits. Reserve scalar and JSON overhead separately from that document.
	taskCreateRequestLimit = canonicalrichtext.MaxDocumentBytes + 8*1024
	// Match task-mutation's logical response budget, including the complete
	// Task and receipt metadata rather than only its description.
	taskCreateResponseLimit = 12*1024*1024 + 64*1024
	// Discovery retains its previous bound; only the create receipt needs the
	// complete Task-content allowance.
	taskCreateDiscoveryResponseLimit = 2 * 1024 * 1024
)

var projectCreateCode = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,7}$`)

// Keep exact transport decimals and the acquired representation condition. This
// projection is separate from legacy cliTask, whose numeric revision is still
// used by unrelated commands awaiting their original migration owner.
type canonicalCreatedTask struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	Code        string `json:"code"`
	Title       string `json:"title"`
	TaskNumber  string `json:"task_number"`
	Revision    string `json:"revision"`
}

func canonicalPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' || len(value) > 19 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func canonicalIdentifier(value string) bool {
	parsed, err := operationID(value)
	return err == nil && parsed == value
}

func strongTaskValidator(value string) bool {
	if len(value) < 3 || len(value) > 8192 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, c := range value[1 : len(value)-1] {
		if c < 0x21 || c > 0x7e || c == '"' {
			return false
		}
	}
	return true
}

// One exchange on the existing command-owned, credential-pinned transport.
// Never refresh a credential, follow a redirect or retry a mutation here.
func taskCreateExchange(ctx context.Context, transport *operationTransport, method, path, body, key string, expectedStatus int) ([]byte, http.Header, error) {
	uncertain := func() error {
		return newExitError(ExitOperation, "Task creation outcome is uncertain; retain the original host, credential, Workspace, Project, key and identical input; do not create a replacement command")
	}
	req, err := http.NewRequestWithContext(ctx, method, transport.host+path, strings.NewReader(body))
	if err != nil {
		return nil, nil, newExitError(ExitArguments, "prepare canonical Task request")
	}
	req.GetBody = nil
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Authorization", "Bearer "+transport.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := transport.client.Do(req)
	if err != nil {
		if method == http.MethodPost {
			return nil, nil, fmt.Errorf("%w: %v", uncertain(), err)
		}
		return nil, nil, newExitError(ExitOperation, "canonical Project discovery failed")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, nil, newExitError(ExitAuthentication, "canonical Task command access denied (HTTP %d); recovery stopped without changing credentials", response.StatusCode)
	case http.StatusNotFound:
		return nil, nil, newExitError(ExitContext, "Workspace or Project is unavailable to this credential; absence does not authorize a replacement command")
	case http.StatusConflict:
		return nil, nil, newExitError(ExitConflict, "Task creation conflict; retain the original key, target and input for reconciliation")
	case http.StatusGone:
		return nil, nil, newExitError(ExitContext, "Task creation recovery expired; do not replace it with a new key")
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusNotAcceptable, http.StatusUnsupportedMediaType, http.StatusRequestEntityTooLarge:
		return nil, nil, newExitError(ExitArguments, "canonical Task request rejected (HTTP %d); no automatic retry", response.StatusCode)
	}
	if response.StatusCode != expectedStatus {
		if method == http.MethodPost {
			return nil, nil, uncertain()
		}
		return nil, nil, newExitError(ExitOperation, "canonical Project discovery failed with HTTP %d", response.StatusCode)
	}
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || media != "application/json" || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") {
		if method == http.MethodPost {
			return nil, nil, uncertain()
		}
		return nil, nil, newExitError(ExitOperation, "canonical discovery representation is invalid")
	}
	responseLimit := taskCreateDiscoveryResponseLimit
	if method == http.MethodPost {
		responseLimit = taskCreateResponseLimit
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(responseLimit)+1))
	if err != nil || len(data) > responseLimit || !json.Valid(data) {
		if method == http.MethodPost {
			return nil, nil, uncertain()
		}
		return nil, nil, newExitError(ExitOperation, "canonical discovery response is incomplete or exceeds its limit")
	}
	return data, response.Header.Clone(), nil
}

func resolveCreateProject(ctx context.Context, transport *operationTransport, workspace, code string) (string, error) {
	var cursor string
	seen := map[string]bool{}
	// Exact-key discovery is unique and indexed. Retain finite continuation for
	// sparse authorized pages; never substitute the first Project or an alias.
	for page := 0; page < 16; page++ {
		query := url.Values{"code": {code}, "limit": {"100"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		data, _, err := taskCreateExchange(ctx, transport, http.MethodGet, "/api/v1/workspaces/"+workspace+"/projects?"+query.Encode(), "", "", http.StatusOK)
		if err != nil {
			return "", err
		}
		var response struct {
			Data []struct {
				ID          string `json:"id"`
				WorkspaceID string `json:"workspace_id"`
				Key         string `json:"key"`
				Revision    string `json:"revision"`
			} `json:"data"`
			NextCursor *string `json:"next_cursor"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || fields["data"] == nil || fields["next_cursor"] == nil || string(fields["data"]) == "null" || json.Unmarshal(data, &response) != nil || len(response.Data) > 1 {
			return "", newExitError(ExitOperation, "canonical Project page is incomplete or ambiguous")
		}
		if len(response.Data) == 1 {
			item := response.Data[0]
			if item.WorkspaceID != workspace || item.Key != code || !canonicalIdentifier(item.ID) || !canonicalPositiveDecimal(item.Revision) {
				return "", newExitError(ExitOperation, "canonical Project identity is inconsistent")
			}
			return item.ID, nil
		}
		if response.NextCursor == nil {
			return "", newExitError(ExitContext, "the exact Project is unavailable in the credential Workspace")
		}
		cursor = *response.NextCursor
		if cursor == "" || len(cursor) > 8192 || seen[cursor] {
			return "", newExitError(ExitOperation, "canonical Project continuation is invalid")
		}
		seen[cursor] = true
	}
	return "", newExitError(ExitOperation, "exact Project discovery exceeded its bounded page budget; no Task was submitted")
}

func newTaskCreateCommand(opts *options, client *http.Client) *cobra.Command {
	var project, description, priority, key, field, workspaceFence, projectFence string
	var jsonOutput bool
	command := &cobra.Command{Use: "create <title>", Short: "Create a Task using canonical Workspace defaults", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		title := args[0]
		if !utf8.ValidString(title) || strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 128 || safeInline(title) != title {
			return newExitError(ExitArguments, "title must contain 1–128 characters without control characters")
		}
		if priority != "" && priority != "none" && priority != "low" && priority != "medium" && priority != "high" && priority != "urgent" {
			return newExitError(ExitArguments, "priority must be none, low, medium, high, or urgent")
		}
		if field != "" && field != "code" {
			return newExitError(ExitArguments, "--field must be code")
		}
		if field != "" && jsonOutput {
			return newExitError(ExitArguments, "--json and --field are mutually exclusive")
		}
		if workspaceFence != "" && !canonicalIdentifier(workspaceFence) {
			return newExitError(ExitArguments, "--workspace-id must be a canonical Workspace UUID")
		}
		if projectFence != "" && (workspaceFence == "" || !canonicalIdentifier(projectFence) || key == "") {
			return newExitError(ExitArguments, "recovery requires canonical --workspace-id, --project-id and the original --idempotency-key together")
		}
		body := struct {
			Title       string          `json:"title"`
			Description json.RawMessage `json:"description,omitempty"`
			Priority    string          `json:"priority,omitempty"`
		}{Title: strings.TrimSpace(title), Priority: priority}
		if command.Flags().Changed("description") {
			text, err := taskText(description, command.InOrStdin())
			if err != nil {
				return err
			}
			body.Description, err = canonicalrichtext.FromMarkdown([]byte(text))
			if err != nil {
				return newExitError(ExitArguments, "description is outside the supported canonical Markdown profile")
			}
		}
		encoded, err := json.Marshal(body)
		if err != nil || len(encoded) > taskCreateRequestLimit {
			return newExitError(ExitArguments, "encoded Task request exceeds the canonical payload limit")
		}
		requestKey, err := mutationKey(key)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(command.Context(), operationDeadline)
		defer cancel()
		transport, err := newOperationTransport(ctx, opts, client)
		if err != nil {
			return err
		}
		defer transport.client.CloseIdleConnections()
		workspace := cliWorkspace{ID: workspaceFence}
		// The retained developer Workspace ceremony is exclusively CLI OAuth.
		// Personal/service bearer users supply the bound Workspace explicitly;
		// never try alternate endpoints or infer tenancy from an opaque token.
		if workspace.ID == "" {
			data, _, readErr := taskCreateExchange(ctx, transport, http.MethodGet, "/api/v1/dev/workspace", "", "", http.StatusOK)
			if readErr != nil {
				return readErr
			}
			if json.Unmarshal(data, &workspace) != nil || !canonicalIdentifier(workspace.ID) || workspace.Slug == "" {
				return newExitError(ExitOperation, "credential Workspace identity is incomplete")
			}
		}
		projectID := projectFence
		if projectID == "" {
			code := strings.ToUpper(strings.TrimSpace(project))
			if code == "" {
				code, err = opts.contexts.LoadProject(transport.host, workspace.Slug)
				if err != nil {
					return newExitError(ExitContext, "no selected Project; supply --project CODE")
				}
				code = strings.ToUpper(strings.TrimSpace(code))
			}
			if !projectCreateCode.MatchString(code) {
				return newExitError(ExitArguments, "Project code must contain 2–8 ASCII letters/digits and begin with a letter")
			}
			projectID, err = resolveCreateProject(ctx, transport, workspace.ID, code)
			if err != nil {
				return err
			}
		}
		// Publish recovery identity before dispatch. Recovery pins the original target
		// instead of re-resolving mutable local selection or switching credentials.
		if _, err := fmt.Fprintf(command.ErrOrStderr(), "Retain --host %s --workspace-id %s --project-id %s --idempotency-key %s with the same credential and identical title/description/priority for Task creation recovery.\n", transport.host, workspace.ID, projectID, requestKey); err != nil {
			return err
		}
		data, headers, err := taskCreateExchange(ctx, transport, http.MethodPost, "/api/v1/projects/"+projectID+"/tasks", string(encoded), requestKey, http.StatusCreated)
		if err != nil {
			return err
		}
		var result canonicalCreatedTask
		if json.Unmarshal(data, &result) != nil || !canonicalIdentifier(result.ID) || result.WorkspaceID != workspace.ID || result.ProjectID != projectID || result.Code == "" || result.Title == "" || !canonicalPositiveDecimal(result.Revision) || !canonicalPositiveDecimal(result.TaskNumber) || !strongTaskValidator(headers.Get("ETag")) || headers.Get("Location") != "/api/v1/tasks/"+result.ID {
			return newExitError(ExitOperation, "Task receipt is incomplete; recover only the original target/key and identical input")
		}
		if jsonOutput {
			// Raw canonical JSON retains decimal strings and nullable fields; never
			// round-trip a representation through interface{} and float64.
			var output any = struct {
				Data     json.RawMessage `json:"data"`
				ETag     string          `json:"etag"`
				Location string          `json:"location"`
			}{json.RawMessage(data), headers.Get("ETag"), headers.Get("Location")}
			return json.NewEncoder(command.OutOrStdout()).Encode(output)
		}
		if field == "code" {
			_, err = fmt.Fprintln(command.OutOrStdout(), safeInline(result.Code))
		} else {
			_, err = fmt.Fprintf(command.OutOrStdout(), "%s  %s\n", safeInline(result.Code), safeInline(result.Title))
		}
		return err
	}}
	command.Flags().StringVar(&project, "project", "", "Exact Project code; defaults to the selected Project")
	command.Flags().StringVar(&description, "description", "", "Description Markdown, or - for bounded UTF-8 stdin")
	command.Flags().StringVar(&priority, "priority", "", "Priority; omitted uses the Workspace creation default")
	command.Flags().StringVar(&key, "idempotency-key", "", "Original key for identical recovery; generated when omitted")
	command.Flags().StringVar(&field, "field", "", "Print only code")
	command.Flags().BoolVar(&jsonOutput, "json", false, "Print the canonical Task and opaque receipt metadata")
	command.Flags().StringVar(&workspaceFence, "workspace-id", "", "Credential Workspace UUID; required for personal/service tokens and original recovery")
	command.Flags().StringVar(&projectFence, "project-id", "", "Original Project UUID printed before an uncertain create")
	return command
}

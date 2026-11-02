package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	canonicalrichtext "github.com/assignso/assign-cli/internal/generated/richtext"
)

// One command owns its credential, deadline, continuation and original mutation.
// Reads share bounded recovery; no request follows redirects or changes credentials.
func taskResourceExchange(ctx context.Context, transport *operationTransport, method, path, body, key string, status int) ([]byte, http.Header, error) {
	uncertain := func() error {
		return newExitError(ExitOperation, "Comment outcome is uncertain; retain the original host, credential, Workspace, Task ID, key and identical input; do not issue a replacement command")
	}
	req, err := http.NewRequestWithContext(ctx, method, transport.host+path, strings.NewReader(body))
	if err != nil {
		return nil, nil, newExitError(ExitArguments, "prepare canonical Task resource request")
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
	response, err := transport.canonicalExchange(req)
	if err != nil {
		if method == http.MethodPost {
			return nil, nil, uncertain()
		}
		return nil, nil, newExitError(ExitOperation, "canonical Task resource read failed: %v", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 401, 403:
		return nil, nil, newExitError(ExitAuthentication, "Task resource access denied (HTTP %d); recovery stopped without changing credentials", response.StatusCode)
	case 404:
		return nil, nil, newExitError(ExitContext, "Task resource unavailable; absence does not authorize a replacement command")
	case 409, 412:
		return nil, nil, newExitError(ExitConflict, "Task resource conflict; retain the original target, key and input")
	case 410:
		return nil, nil, newExitError(ExitContext, "Task resource recovery expired; do not replace the original key")
	case 400, 406, 413, 415, 422:
		return nil, nil, newExitError(ExitArguments, "canonical Task resource rejected (HTTP %d); no automatic retry", response.StatusCode)
	}
	if response.StatusCode != status {
		if method == http.MethodPost {
			return nil, nil, uncertain()
		}
		return nil, nil, newExitError(ExitOperation, "Task resource read failed with HTTP %d", response.StatusCode)
	}
	media, _, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 262144+1))
	if e != nil || media != "application/json" || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") || readErr != nil || len(data) > 262144 || !json.Valid(data) {
		if method == http.MethodPost {
			return nil, nil, uncertain()
		}
		return nil, nil, newExitError(ExitOperation, "Task resource representation is incomplete or exceeds its limit")
	}
	return data, response.Header.Clone(), nil
}

type canonicalReadTask struct {
	WebURL      string  `json:"web_url"`
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	StatusID    string  `json:"status_id"`
	Code        string  `json:"code"`
	Title       string  `json:"title"`
	Revision    string  `json:"revision"`
	Category    string  `json:"-"`
	MilestoneID *string `json:"milestone_id"`
	TaskNumber  string  `json:"task_number"`
	Lifecycle   string  `json:"lifecycle"`
	ArchivedAt  *string `json:"archived_at"`
	TrashedAt   *string `json:"trashed_at"`
}

func resourceWorkspace(ctx context.Context, transport *operationTransport, explicit string) (cliWorkspace, error) {
	if explicit != "" {
		if !canonicalIdentifier(explicit) {
			return cliWorkspace{}, newExitError(ExitArguments, "--workspace-id must be a canonical Workspace UUID")
		}
		return cliWorkspace{ID: explicit}, nil
	}
	data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/dev/workspace", "", "", http.StatusOK)
	if err != nil {
		return cliWorkspace{}, err
	}
	var workspace cliWorkspace
	if json.Unmarshal(data, &workspace) != nil || !canonicalIdentifier(workspace.ID) || workspace.Slug == "" {
		return cliWorkspace{}, newExitError(ExitOperation, "credential Workspace identity is incomplete")
	}
	return workspace, nil
}

// Resolve only an exact code inside the credential's Workspace. A partial scan,
// exhausted work budget or repeated cursor never establishes absence. Keep one
// page in memory and pass the acquired opaque continuation back unchanged.
func resolveResourceTask(ctx context.Context, transport *operationTransport, workspace, code string) (canonicalReadTask, error) {
	cursor := ""
	seen := map[string]bool{}
	for count := 0; count < 1000; count++ {
		query := url.Values{"limit": {"100"}, "state": {"all"}, "include_archived": {"true"}, "code": {code}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/workspaces/"+workspace+"/tasks?"+query.Encode(), "", "", http.StatusOK)
		if err != nil {
			return canonicalReadTask{}, err
		}
		var page struct {
			Data        []canonicalReadTask `json:"data"`
			NextCursor  *string             `json:"next_cursor"`
			Coverage    string              `json:"coverage"`
			LimitReason string              `json:"limit_reason"`
			Included    struct {
				Statuses []struct {
					ID       string `json:"id"`
					Category string `json:"category"`
				} `json:"statuses"`
			} `json:"included"`
		}
		if json.Unmarshal(data, &page) != nil || page.Data == nil || len(page.Data) > 100 {
			return canonicalReadTask{}, newExitError(ExitOperation, "Task discovery page is incomplete")
		}
		for _, item := range page.Data {
			if !canonicalIdentifier(item.ID) || item.WorkspaceID != workspace || !canonicalIdentifier(item.ProjectID) || !canonicalPositiveDecimal(item.Revision) || item.Code == "" {
				return canonicalReadTask{}, newExitError(ExitOperation, "Task discovery identity is incomplete")
			}
			if item.Code == code {
				for _, status := range page.Included.Statuses {
					if status.ID == item.StatusID {
						item.Category = status.Category
					}
				}
				return item, nil
			}
		}
		if page.NextCursor == nil {
			if page.Coverage != "" && page.Coverage != "complete" {
				return canonicalReadTask{}, newExitError(ExitOperation, "Task discovery is partial; absence is unknown")
			}
			return canonicalReadTask{}, newExitError(ExitContext, "Task code is unavailable in this Workspace")
		}
		cursor = *page.NextCursor
		if cursor == "" || len(cursor) > 8192 || safeInline(cursor) != cursor || seen[cursor] {
			return canonicalReadTask{}, newExitError(ExitOperation, "Task discovery continuation is invalid")
		}
		seen[cursor] = true
	}
	return canonicalReadTask{}, newExitError(ExitOperation, "Task discovery work budget exhausted; absence is unknown")
}

func readResourceTask(ctx context.Context, opts *options, client *http.Client, code, workspaceID string) (*operationTransport, cliWorkspace, canonicalReadTask, error) {
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return nil, cliWorkspace{}, canonicalReadTask{}, err
	}
	workspace, err := resourceWorkspace(ctx, transport, workspaceID)
	if err != nil {
		transport.client.CloseIdleConnections()
		return nil, workspace, canonicalReadTask{}, err
	}
	item, err := resolveResourceTask(ctx, transport, workspace.ID, code)
	if err != nil {
		transport.client.CloseIdleConnections()
		return nil, workspace, item, err
	}
	return transport, workspace, item, nil
}

// Task show shares the same credential-pinned exact-code discovery used by
// context/comments. Navigation is authoritative response metadata, never inferred
// from the API host or supplied through a second client origin setting.
func readResourceTaskShow(ctx context.Context, opts *options, client *http.Client, code string) (cliTask, error) {
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, workspace, resource, err := readResourceTask(ctx, opts, client, code, "")
	if err != nil {
		return cliTask{}, err
	}
	defer transport.client.CloseIdleConnections()
	return canonicalTaskNavigation(resource, workspace)
}

func canonicalTaskNavigation(resource canonicalReadTask, workspace cliWorkspace) (cliTask, error) {
	projectCode, _, found := strings.Cut(resource.Code, "-")
	revision, err := strconv.ParseInt(resource.Revision, 10, 64)
	if !found || projectCode == "" || resource.Title == "" || err != nil || revision < 1 {
		return cliTask{}, newExitError(ExitOperation, "Task response is incomplete")
	}
	switch resource.Category {
	case "backlog", "todo", "in_progress", "in_review", "done":
	default:
		return cliTask{}, newExitError(ExitOperation, "Task Status category is incomplete")
	}
	navigation, err := url.Parse(resource.WebURL)
	if err != nil || resource.WebURL == "" || navigation.Hostname() == "" || navigation.User != nil || navigation.RawQuery != "" || navigation.ForceQuery || navigation.Fragment != "" || navigation.Opaque != "" || safeInline(resource.WebURL) != resource.WebURL {
		return cliTask{}, newExitError(ExitOperation, "Task web_url representation/configuration is invalid")
	}
	local := navigation.Hostname() == "localhost" || navigation.Hostname() == "127.0.0.1" || navigation.Hostname() == "::1"
	expected := "/app/" + url.PathEscape(workspace.Slug) + "/tasks/" + url.PathEscape(resource.Code)
	if (navigation.Scheme != "https" && !(navigation.Scheme == "http" && local)) || workspace.Slug == "" || navigation.EscapedPath() != expected {
		return cliTask{}, newExitError(ExitOperation, "Task web_url representation/configuration is invalid")
	}
	return cliTask{Code: resource.Code, ProjectCode: projectCode, Title: resource.Title, StatusCategory: resource.Category, Revision: revision, URL: resource.WebURL}, nil
}

type canonicalCLIComment struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	TaskID      string          `json:"task_id"`
	Body        json.RawMessage `json:"body"`
	Actor       string          `json:"actor"`
	ActorName   *string         `json:"actor_name"`
	Revision    json.Number     `json:"revision"`
	CreatedAt   string          `json:"created_at"`
	DeletedAt   *string         `json:"deleted_at"`
}

func (item canonicalCLIComment) legacy(workspace, task string) (cliComment, error) {
	if !canonicalIdentifier(item.ID) || item.WorkspaceID != workspace || item.TaskID != task || !canonicalPositiveDecimal(string(item.Revision)) || item.Actor == "" || item.CreatedAt == "" {
		return cliComment{}, newExitError(ExitOperation, "Comment response identity is incomplete")
	}
	revision, _ := item.Revision.Int64()
	out := cliComment{Actor: item.Actor, ActorName: item.ActorName, Revision: revision, CreatedAt: item.CreatedAt}
	if item.DeletedAt != nil {
		if len(item.Body) > 0 {
			return out, newExitError(ExitOperation, "Comment tombstone contains content")
		}
		return out, nil
	}
	if len(item.Body) == 0 {
		return out, newExitError(ExitOperation, "Comment body is incomplete")
	}
	markdown, err := canonicalrichtext.ToMarkdown(item.Body)
	if err != nil {
		return out, newExitError(ExitOperation, "Comment body cannot be rendered completely")
	}
	out.BodyMarkdown = strings.TrimSuffix(string(markdown), "\n")
	return out, nil
}

func createResourceComment(ctx context.Context, output io.Writer, opts *options, client *http.Client, code, text, key, workspaceID, taskID string) error {
	body, err := canonicalrichtext.FromMarkdown([]byte(text))
	if err != nil {
		return newExitError(ExitArguments, "comment is outside the supported canonical Markdown profile")
	}
	encoded, err := json.Marshal(struct {
		Body json.RawMessage `json:"body"`
	}{body})
	if err != nil || len(encoded) > canonicalrichtext.MaxCommentBytes+8*1024 {
		return newExitError(ExitArguments, "comment exceeds the canonical payload limit")
	}
	if taskID != "" && (workspaceID == "" || !canonicalIdentifier(taskID)) {
		return newExitError(ExitArguments, "recovery requires canonical --workspace-id and --task-id with the original key and input")
	}
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return err
	}
	defer transport.client.CloseIdleConnections()
	workspace, err := resourceWorkspace(ctx, transport, workspaceID)
	if err != nil {
		return err
	}
	if taskID == "" {
		item, e := resolveResourceTask(ctx, transport, workspace.ID, code)
		if e != nil {
			return e
		}
		taskID = item.ID
	}
	if _, err = fmt.Fprintf(output, "assign: retain original host %s, credential, --workspace-id %s --task-id %s --idempotency-key %s and identical input for recovery\n", transport.host, workspace.ID, taskID, key); err != nil {
		return err
	}
	data, headers, err := taskResourceExchange(ctx, transport, http.MethodPost, "/api/v1/tasks/"+taskID+"/comments", string(encoded), key, http.StatusCreated)
	if err != nil {
		return err
	}
	var result canonicalCLIComment
	if json.Unmarshal(data, &result) != nil || !strongTaskValidator(headers.Get("ETag")) {
		return newExitError(ExitOperation, "Comment receipt is incomplete; retain original target, key and input")
	}
	if _, err = result.legacy(workspace.ID, taskID); err != nil {
		return err
	}
	return nil
}

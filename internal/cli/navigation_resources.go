package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type navigationProject struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Revision    string `json:"revision"`
	WebURL      string `json:"web_url"`
}

func unmarshalResourcePage(data []byte, target any) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields["data"] == nil || string(fields["data"]) == "null" || fields["next_cursor"] == nil {
		return newExitError(ExitOperation, "canonical resource page is incomplete")
	}
	return json.Unmarshal(data, target)
}

type navigationDocument struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
	Title       string `json:"title"`
	Visibility  string `json:"visibility"`
	Revision    string `json:"revision"`
	WebURL      string `json:"web_url"`
}

func (progress *cliMilestoneProgress) UnmarshalJSON(data []byte) error {
	var raw struct {
		Total      json.RawMessage            `json:"total_tasks"`
		Completed  json.RawMessage            `json:"completed_tasks"`
		Incomplete json.RawMessage            `json:"incomplete_tasks"`
		Categories map[string]json.RawMessage `json:"tasks_by_category"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parse := func(raw json.RawMessage) (*int64, error) {
		value := string(raw)
		if strings.HasPrefix(value, "\"") {
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
		}
		if value != "0" && !canonicalPositiveDecimal(value) {
			return nil, newExitError(ExitOperation, "Milestone count is invalid")
		}
		number, err := strconv.ParseInt(value, 10, 64)
		return &number, err
	}
	var err error
	if progress.TotalTasks, err = parse(raw.Total); err != nil {
		return err
	}
	if progress.CompletedTasks, err = parse(raw.Completed); err != nil {
		return err
	}
	if progress.IncompleteTasks, err = parse(raw.Incomplete); err != nil {
		return err
	}
	if raw.Categories == nil {
		return newExitError(ExitOperation, "Milestone category counts are absent")
	}
	progress.TasksByCategory = map[string]int64{}
	for key, value := range raw.Categories {
		number, e := parse(value)
		if e != nil {
			return e
		}
		progress.TasksByCategory[key] = *number
	}
	return nil
}

func navigationURL(raw string, workspace cliWorkspace, family, path string) error {
	u, err := url.Parse(raw)
	local := err == nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
	if err != nil || workspace.Slug == "" || raw == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || safeInline(raw) != raw || (u.Scheme != "https" && !(u.Scheme == "http" && local)) || u.EscapedPath() != "/app/"+url.PathEscape(workspace.Slug)+"/"+family+"/"+url.PathEscape(path) {
		return newExitError(ExitOperation, "resource web_url representation/configuration is invalid")
	}
	return nil
}

func projectResourcePage(ctx context.Context, transport *operationTransport, workspace, code, cursor string) ([]navigationProject, *string, error) {
	query := url.Values{"limit": {"100"}}
	if code != "" {
		query.Set("code", code)
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/workspaces/"+workspace+"/projects?"+query.Encode(), "", "", http.StatusOK)
	if err != nil {
		return nil, nil, err
	}
	var page struct {
		Data []navigationProject `json:"data"`
		Next *string             `json:"next_cursor"`
	}
	if unmarshalResourcePage(data, &page) != nil || page.Data == nil || len(page.Data) > 100 {
		return nil, nil, newExitError(ExitOperation, "Project page is incomplete")
	}
	for _, item := range page.Data {
		if !canonicalIdentifier(item.ID) || item.WorkspaceID != workspace || item.Key == "" || item.Name == "" || !canonicalPositiveDecimal(item.Revision) {
			return nil, nil, newExitError(ExitOperation, "Project identity is incomplete")
		}
	}
	return page.Data, page.Next, nil
}

func findProjectResource(ctx context.Context, transport *operationTransport, workspace, code string) (navigationProject, error) {
	cursor := ""
	seen := map[string]bool{}
	for count := 0; count < 16; count++ {
		items, next, err := projectResourcePage(ctx, transport, workspace, code, cursor)
		if err != nil {
			return navigationProject{}, err
		}
		if len(items) > 1 {
			return navigationProject{}, newExitError(ExitOperation, "exact Project response is ambiguous")
		}
		if len(items) == 1 {
			if items[0].Key != code {
				return navigationProject{}, newExitError(ExitOperation, "Project code does not match")
			}
			return items[0], nil
		}
		if next == nil {
			return navigationProject{}, newExitError(ExitContext, "Project is unavailable in this Workspace")
		}
		cursor = *next
		if cursor == "" || len(cursor) > 8192 || safeInline(cursor) != cursor || seen[cursor] {
			return navigationProject{}, newExitError(ExitOperation, "Project continuation is invalid")
		}
		seen[cursor] = true
	}
	return navigationProject{}, newExitError(ExitOperation, "Project discovery work budget exhausted; absence is unknown")
}

func projectNavigation(item navigationProject, workspace cliWorkspace) (cliProject, error) {
	revision, err := strconv.ParseInt(item.Revision, 10, 64)
	if err != nil || revision < 1 {
		return cliProject{}, newExitError(ExitOperation, "Project revision is invalid")
	}
	if err = navigationURL(item.WebURL, workspace, "projects", item.Path); err != nil {
		return cliProject{}, err
	}
	return cliProject{Code: item.Key, Name: item.Name, Path: item.Path, Revision: revision, URL: item.WebURL}, nil
}

func documentResourcePage(ctx context.Context, transport *operationTransport, workspace string, query url.Values) ([]navigationDocument, *string, error) {
	data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/workspaces/"+workspace+"/documents?"+query.Encode(), "", "", http.StatusOK)
	if err != nil {
		return nil, nil, err
	}
	var page struct {
		Data []navigationDocument `json:"data"`
		Next *string              `json:"next_cursor"`
	}
	if unmarshalResourcePage(data, &page) != nil || page.Data == nil || len(page.Data) > 100 {
		return nil, nil, newExitError(ExitOperation, "Document page is incomplete")
	}
	for _, item := range page.Data {
		if !canonicalIdentifier(item.ID) || item.WorkspaceID != workspace || item.Path == "" || item.Title == "" || !canonicalPositiveDecimal(item.Revision) {
			return nil, nil, newExitError(ExitOperation, "Document identity is incomplete")
		}
	}
	return page.Data, page.Next, nil
}

// The existing pinned transport also reads the canonical Markdown media type.
// Bound the same visible excerpt as before; do not parse it as a JSON wrapper.
func documentMarkdown(ctx context.Context, transport *operationTransport, id string) (string, bool, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, transport.host+"/api/v1/documents/"+id+"/content?format=markdown", nil)
	if err != nil {
		return "", false, err
	}
	r.Header.Set("Authorization", "Bearer "+transport.token)
	r.Header.Set("Accept", "text/markdown")
	r.Header.Set("Accept-Encoding", "identity")
	response, err := transport.canonicalExchange(r)
	if err != nil {
		return "", false, newExitError(ExitOperation, "Document content read failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", false, productReadError(response, "Document")
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "text/markdown" || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") {
		return "", false, newExitError(ExitOperation, "Document Markdown response is invalid")
	}
	reader := bufio.NewReader(io.LimitReader(response.Body, 4*65537+1))
	var text strings.Builder
	for count := 0; count <= 65536; count++ {
		rune, _, e := reader.ReadRune()
		if e == io.EOF {
			return text.String(), false, nil
		}
		if e != nil {
			return "", false, newExitError(ExitOperation, "Document content is incomplete")
		}
		if count == 65536 {
			return text.String(), true, nil
		}
		text.WriteRune(rune)
	}
	return text.String(), true, nil
}

func readMilestoneResource(ctx context.Context, opts *options, client *http.Client, code string) (cliMilestone, error) {
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return cliMilestone{}, err
	}
	defer transport.client.CloseIdleConnections()
	workspace, err := resourceWorkspace(ctx, transport, "")
	if err != nil {
		return cliMilestone{}, err
	}
	return milestoneResource(ctx, transport, workspace, code)
}

func milestoneResource(ctx context.Context, transport *operationTransport, workspace cliWorkspace, code string) (cliMilestone, error) {
	parts := milestoneCodePattern.FindStringSubmatch(code)
	if len(parts) != 3 {
		return cliMilestone{}, newExitError(ExitArguments, "invalid Milestone code")
	}
	project, err := resolveCreateProject(ctx, transport, workspace.ID, parts[1])
	if err != nil {
		return cliMilestone{}, err
	}
	query := url.Values{"project_id": {project}, "code": {code}, "limit": {"1"}, "include_archived": {"true"}}
	data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/workspaces/"+workspace.ID+"/milestones?"+query.Encode(), "", "", http.StatusOK)
	if err != nil {
		return cliMilestone{}, err
	}
	var page struct {
		Data []struct {
			cliMilestone
			Revision string `json:"revision"`
		} `json:"data"`
		Next *string `json:"next_cursor"`
	}
	if unmarshalResourcePage(data, &page) != nil || len(page.Data) != 1 || page.Next != nil {
		return cliMilestone{}, newExitError(ExitContext, "exact Milestone is unavailable or incomplete")
	}
	item := page.Data[0].cliMilestone
	item.Revision, err = strconv.ParseInt(page.Data[0].Revision, 10, 64)
	if err != nil || item.Revision < 1 || item.WorkspaceID != workspace.ID || item.ProjectID != project || !canonicalIdentifier(item.ID) || item.Code == nil || *item.Code != code {
		return cliMilestone{}, newExitError(ExitOperation, "Milestone identity is inconsistent")
	}
	return item, nil
}

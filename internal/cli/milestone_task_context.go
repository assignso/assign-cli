package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// These independent authorized reads fence the context fetch, not a write.
// Comments, relations and future changes still require canonical reconciliation.
type milestoneContextFence struct {
	milestone cliMilestone
	task      canonicalReadTask
}

func readMilestoneContextFence(ctx context.Context, opts *options, client *http.Client, taskCode, scopeInput string) (*milestoneContextFence, error) {
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return nil, err
	}
	defer transport.client.CloseIdleConnections()
	workspace, err := resourceWorkspace(ctx, transport, "")
	if err != nil {
		return nil, err
	}
	return readResourceMilestoneContextFence(ctx, transport, workspace.ID, taskCode, scopeInput)
}

func readResourceMilestoneContextFence(ctx context.Context, transport *operationTransport, workspace, taskCode, scopeInput string) (*milestoneContextFence, error) {
	code, err := canonicalMilestoneCode(scopeInput)
	if err != nil {
		return nil, err
	}
	item, err := resolveResourceTask(ctx, transport, workspace, taskCode)
	if err != nil {
		return nil, err
	}
	var scope cliMilestone
	cursor := ""
	seen := map[string]bool{}
	found := false
	for page := 0; page < 10; page++ {
		query := url.Values{"project_id": {item.ProjectID}, "code": {code}, "include_archived": {"true"}, "limit": {"100"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, "/api/v1/workspaces/"+workspace+"/milestones?"+query.Encode(), "", "", http.StatusOK)
		if err != nil {
			return nil, err
		}
		var result struct {
			Data []struct {
				cliMilestone
				DecimalRevision string          `json:"revision"`
				Progress        json.RawMessage `json:"progress"`
			} `json:"data"`
			NextCursor *string `json:"next_cursor"`
		}
		if json.Unmarshal(data, &result) != nil || result.Data == nil || len(result.Data) > 100 {
			return nil, newExitError(ExitOperation, "Milestone page is incomplete")
		}
		for _, row := range result.Data {
			if row.Code != nil && *row.Code == code {
				if !canonicalPositiveDecimal(row.DecimalRevision) {
					return nil, newExitError(ExitOperation, "Milestone revision is incomplete")
				}
				scope = row.cliMilestone
				scope.Revision, _ = strconv.ParseInt(row.DecimalRevision, 10, 64)
				found = true
				break
			}
		}
		if found {
			break
		}
		if result.NextCursor == nil {
			return nil, newExitError(ExitContext, "Milestone is unavailable in this Project")
		}
		cursor = *result.NextCursor
		if cursor == "" || len(cursor) > 4096 || safeInline(cursor) != cursor || seen[cursor] {
			return nil, newExitError(ExitOperation, "Milestone continuation is invalid")
		}
		seen[cursor] = true
	}
	if !found {
		return nil, newExitError(ExitOperation, "Milestone discovery is partial; absence is unknown")
	}
	if scope.WorkspaceID != workspace {
		return nil, newExitError(ExitOperation, "Milestone Workspace identity is inconsistent")
	}
	parts := milestoneCodePattern.FindStringSubmatch(code)
	if !validMilestoneScopeID(scope.ID) || !validMilestoneScopeID(scope.WorkspaceID) || !validMilestoneScopeID(scope.ProjectID) || scope.Code == nil || *scope.Code != code || scope.MilestoneNumber == nil || *scope.MilestoneNumber != parts[2] || scope.Revision < 1 || scope.Status == "" {
		return nil, newExitError(ExitOperation, "Milestone scope response is incomplete or inconsistent")
	}
	if scope.ArchivedAt != nil {
		return nil, newExitError(ExitOperation, "Milestone is archived; refresh selection before requesting scoped context")
	}
	if item.Code != taskCode || item.ProjectID != scope.ProjectID || item.MilestoneID == nil || *item.MilestoneID != scope.ID || item.Title == "" || !canonicalPositiveDecimal(item.TaskNumber) || item.Code != parts[1]+"-"+item.TaskNumber || item.Lifecycle != "active" || item.ArchivedAt != nil || item.TrashedAt != nil {
		return nil, newExitError(ExitOperation, "Task does not have the expected active Milestone membership; refresh selection before requesting scoped context")
	}
	return &milestoneContextFence{milestone: scope, task: item}, nil
}

func (before *milestoneContextFence) sameObservation(after *milestoneContextFence) bool {
	return before.milestone.ID == after.milestone.ID && before.milestone.WorkspaceID == after.milestone.WorkspaceID && before.milestone.ProjectID == after.milestone.ProjectID && before.milestone.Revision == after.milestone.Revision && before.milestone.Status == after.milestone.Status && before.task.ID == after.task.ID && before.task.Revision == after.task.Revision
}

func (fence *milestoneContextFence) markdownHeader() string {
	return fmt.Sprintf("> Milestone scope: %s (revision %d); Task %s (revision %s).\n> These are read observations, not an atomic claim or a complete dependency snapshot.\n\n", *fence.milestone.Code, fence.milestone.Revision, fence.task.Code, fence.task.Revision)
}

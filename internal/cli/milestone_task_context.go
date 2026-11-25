package cli

import (
	"context"
	"fmt"
)

// These independent authorized reads fence the context fetch, not a write.
// Comments, relations and future changes still require canonical reconciliation.
type milestoneContextFence struct {
	milestone cliMilestone
	task      canonicalReadTask
}

func readResourceMilestoneContextFence(ctx context.Context, transport *operationTransport, item canonicalReadTask, code string) (*milestoneContextFence, error) {
	scope, err := readExactMilestone(ctx, transport, item.WorkspaceID, item.ProjectID, code)
	if err != nil {
		return nil, err
	}
	parts := milestoneCodePattern.FindStringSubmatch(code)
	if !validMilestoneScopeID(scope.ID) || !validMilestoneScopeID(scope.WorkspaceID) || !validMilestoneScopeID(scope.ProjectID) || scope.Code == nil || *scope.Code != code || scope.MilestoneNumber == nil || *scope.MilestoneNumber != parts[2] || scope.Revision < 1 || scope.Status == "" {
		return nil, newExitError(ExitOperation, "Milestone scope response is incomplete or inconsistent")
	}
	if scope.ArchivedAt != nil {
		return nil, newExitError(ExitOperation, "Milestone is archived; refresh selection before requesting scoped context")
	}
	if item.MilestoneID == nil || *item.MilestoneID != scope.ID || item.Title == "" || !canonicalPositiveDecimal(item.TaskNumber) || item.Code != parts[1]+"-"+item.TaskNumber || item.Lifecycle != "active" || item.ArchivedAt != nil || item.TrashedAt != nil {
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

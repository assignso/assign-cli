package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type taskBranchPlan struct {
	WorkspaceID     string `json:"workspace_id,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	TaskID          string `json:"task_id"`
	BindingID       string `json:"binding_id"`
	Repository      string `json:"repository"`
	Branch          string `json:"branch"`
	ProjectRevision string `json:"project_revision"`
	TaskRevision    string `json:"task_revision"`
	BindingRevision string `json:"binding_revision"`
}

type taskBranchJournal struct {
	Version   int            `json:"version"`
	Host      string         `json:"host"`
	Code      string         `json:"code"`
	Condition string         `json:"condition"`
	Checkout  branchCheckout `json:"checkout"`
	Plan      taskBranchPlan `json:"plan"`
}

func taskBranchDigest(parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func taskBranchJournalPath(ctx context.Context, g branchGit, key string) (string, error) {
	dir, err := g.run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil || !filepath.IsAbs(dir) {
		return "", newExitError(ExitContext, "cannot resolve the worktree's Git metadata")
	}
	return filepath.Join(dir, "assign-task-start-"+taskBranchDigest(key)+".json"), nil
}

func writeTaskBranchJournal(path string, journal taskBranchJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".assign-task-start-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	// Publishing a complete file exclusively makes another invocation using
	// the same key see a whole record or lose the race before server effects.
	return os.Link(file.Name(), path)
}

func readTaskBranchJournal(path string) (taskBranchJournal, error) {
	var journal taskBranchJournal
	info, err := os.Lstat(path)
	if err != nil {
		return journal, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return journal, errors.New("invalid branch recovery record")
	}
	file, err := os.Open(path)
	if err != nil {
		return journal, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &journal) != nil || journal.Version != 1 {
		return journal, errors.New("invalid branch recovery record")
	}
	return journal, nil
}

func readTaskBranchPlan(ctx context.Context, transport *operationTransport, task canonicalCreatedTask, repository string) (taskBranchPlan, error) {
	var plan taskBranchPlan
	path := "/api/v1/projects/" + task.ProjectID + "/task-branch-start?" + url.Values{"task_id": {task.ID}, "repository": {repository}}.Encode()
	data, _, err := taskResourceExchange(ctx, transport, http.MethodGet, path, "", "", http.StatusOK)
	if err != nil {
		return plan, fmt.Errorf("Project repository or Task authority could not be confirmed: %w", err)
	}
	if json.Unmarshal(data, &plan) != nil || !validTaskBranchPlan(plan) ||
		plan.TaskID != task.ID || plan.ProjectID != task.ProjectID || plan.WorkspaceID != task.WorkspaceID ||
		(task.Revision != "" && plan.TaskRevision != task.Revision) || plan.Repository != repository {
		return taskBranchPlan{}, newExitError(ExitConflict, "branch plan or Task revision changed")
	}
	return plan, nil
}

func validTaskBranchPlan(plan taskBranchPlan) bool {
	parts := strings.Split(plan.BindingRevision, ":")
	return canonicalIdentifier(plan.TaskID) && canonicalIdentifier(plan.ProjectID) && canonicalIdentifier(plan.WorkspaceID) &&
		canonicalIdentifier(plan.BindingID) && len(parts) == 3 && canonicalPositiveDecimal(parts[0]) &&
		canonicalPositiveDecimal(parts[1]) && canonicalPositiveDecimal(parts[2]) &&
		canonicalPositiveDecimal(plan.ProjectRevision) && canonicalPositiveDecimal(plan.TaskRevision) &&
		len(plan.Repository) <= 512 && branchRepositoryOrigin.MatchString(plan.Repository) && validTaskBranchRef(plan.Branch)
}

func associateTaskBranch(ctx context.Context, transport *operationTransport, plan taskBranchPlan, key string) error {
	project := plan.ProjectID
	plan.ProjectID, plan.WorkspaceID = "", ""
	body, _ := json.Marshal(plan)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, transport.host+"/api/v1/projects/"+project+"/task-branch-start", bytes.NewReader(body))
	if err != nil {
		return newExitError(ExitOperation, "could not prepare the branch association")
	}
	req.GetBody = nil
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+transport.token)
	req.Header.Set("Idempotency-Key", key)
	res, err := transport.canonicalExchange(req)
	if err != nil {
		return newExitError(ExitOperation, "branch association response was lost")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return newExitError(ExitOperation, "branch association could not be confirmed (HTTP %d)", res.StatusCode)
	}
	var receipt struct {
		ProjectID string `json:"project_id"`
		TaskID    string `json:"task_id"`
		BindingID string `json:"binding_id"`
		Branch    string `json:"branch"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 2049))
	if err != nil || len(data) > 2048 || json.Unmarshal(data, &receipt) != nil ||
		receipt.ProjectID != project || receipt.TaskID != plan.TaskID || receipt.BindingID != plan.BindingID || receipt.Branch != plan.Branch {
		return newExitError(ExitOperation, "branch association receipt is incomplete")
	}
	return nil
}

func runTaskBranchStart(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, code string, revision int64, key, taskID, condition string) error {
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	git, err := branchGitAtCurrentDirectory()
	if err != nil {
		return err
	}
	checkout, err := git.preflight(ctx)
	if err != nil {
		return err
	}
	journalPath, err := taskBranchJournalPath(ctx, git, key)
	if err != nil {
		return err
	}
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return err
	}
	defer transport.client.CloseIdleConnections()

	recovery := taskID != ""
	var journal taskBranchJournal
	if !recovery {
		observed, tag, err := observeTaskAction(ctx, transport, code, revision)
		if err != nil {
			return err
		}
		plan, err := readTaskBranchPlan(ctx, transport, observed, checkout.Repository)
		if err != nil {
			return err
		}
		if err = git.validateBranch(ctx, plan.Branch); err != nil {
			return err
		}
		if err = git.branchCollision(ctx, plan.Branch); err != nil {
			return err
		}
		taskID, condition = observed.ID, tag
		journal = taskBranchJournal{1, transport.host, code, condition, checkout, plan}
		if err = writeTaskBranchJournal(journalPath, journal); err != nil {
			return newExitError(ExitConflict, "could not reserve the recovery record; no Task action sent")
		}
	} else {
		journal, err = readTaskBranchJournal(journalPath)
		if err != nil || journal.Host != transport.host || journal.Code != code || journal.Condition != condition ||
			journal.Plan.TaskID != taskID || !validTaskBranchPlan(journal.Plan) || journal.Plan.Repository != checkout.Repository ||
			journal.Checkout.Repository != checkout.Repository || !gitObjectID.MatchString(journal.Checkout.Base) ||
			!strings.HasPrefix(journal.Checkout.Head, "refs/heads/") {
			return newExitError(ExitConflict, "this worktree has no matching branch recovery record; keep the original host, credential, Task and worktree")
		}
		if err = git.validateBranch(ctx, journal.Plan.Branch); err != nil {
			return err
		}
	}

	plan := journal.Plan
	ref := "refs/heads/" + plan.Branch
	marker := "assign task start " + taskBranchDigest(transport.host, key, taskID, plan.Branch)
	retry := taskActionRetry(transport.host, "start", code, taskID, condition, key, "") + " --branch"
	owned, err := git.ownedBranch(ctx, plan.Branch, journal.Checkout.Base, marker)
	if err != nil {
		return fmt.Errorf("%w; retain the recovery command: %s", err, retry)
	}
	if !owned && (checkout != journal.Checkout || git.branchCollision(ctx, plan.Branch) != nil) ||
		owned && (checkout.Base != journal.Checkout.Base || (checkout.Head != journal.Checkout.Head && checkout.Head != ref)) {
		return newExitError(ExitConflict, "worktree or branch changed; restore the original clean checkout and retry: %s", retry)
	}
	state := "local branch absent"
	if owned {
		state = "local branch created"
		if checkout.Head == ref {
			state = "local branch created and switched"
		}
	}
	if recovery && checkout.Head != ref {
		pinnedTask := canonicalCreatedTask{ID: plan.TaskID, ProjectID: plan.ProjectID, WorkspaceID: plan.WorkspaceID}
		current, err := readTaskBranchPlan(ctx, transport, pinnedTask, checkout.Repository)
		if err != nil {
			return fmt.Errorf("%s; repository or Task authority unavailable for retry: %w; retain: %s", state, err, retry)
		}
		current.TaskRevision = plan.TaskRevision
		if current != plan {
			return newExitError(ExitConflict, "%s; preparation changed; retain the original operation for reconciliation: %s", state, retry)
		}
	}
	result, reassigned, err := sendTaskAction(ctx, diagnostics, transport, "start", taskID, condition, key, "", retry)
	if err != nil {
		return fmt.Errorf("Task start unconfirmed; %s; association step not attempted: %w", state, err)
	}
	partial := func(err error) error {
		return fmt.Errorf("Task started; %s; association unconfirmed: %w; exact retry in the same worktree: %s", state, err, retry)
	}
	if result.ProjectID != plan.ProjectID || result.WorkspaceID != plan.WorkspaceID {
		return partial(newExitError(ExitConflict, "Task receipt escaped the prepared Project"))
	}
	plan.TaskRevision = result.Revision
	if checkout.Head != ref {
		current, err := readTaskBranchPlan(ctx, transport, result, checkout.Repository)
		if err != nil {
			return partial(err)
		}
		if current != plan {
			return partial(newExitError(ExitConflict, "Project template or repository binding changed"))
		}
		latest, err := git.preflight(ctx)
		if err != nil {
			return partial(err)
		}
		if latest != checkout {
			return partial(newExitError(ExitConflict, "worktree changed after preflight"))
		}
		if !owned {
			zero := strings.Repeat("0", len(checkout.Base))
			if _, err := git.run(ctx, "update-ref", "--create-reflog", "-m", marker, ref, checkout.Base, zero); err != nil {
				state = "local branch creation unconfirmed"
				return partial(newExitError(ExitConflict, "Git refused to reserve the branch"))
			}
			state = "local branch created; switch not confirmed"
		}
		confirmed, err := git.ownedBranch(ctx, plan.Branch, journal.Checkout.Base, marker)
		if err != nil || !confirmed {
			return partial(newExitError(ExitConflict, "the reserved branch changed before switch"))
		}
		if _, err := git.run(ctx, "switch", "--no-guess", "--no-recurse-submodules", "--", plan.Branch); err != nil {
			return partial(newExitError(ExitConflict, "Git refused to switch to the owned branch"))
		}
	}
	state = "local branch created and switched"
	head, err := git.run(ctx, "symbolic-ref", "-q", "HEAD")
	confirmed, refErr := git.ownedBranch(ctx, plan.Branch, journal.Checkout.Base, marker)
	if err != nil || head != ref || refErr != nil || !confirmed {
		state = "local branch switch unconfirmed"
		return partial(newExitError(ExitConflict, "the checked-out branch could not be confirmed"))
	}
	if err := associateTaskBranch(ctx, transport, plan, taskBranchDigest("association", transport.host, key, taskID)); err != nil {
		return partial(err)
	}
	if reassigned {
		_, _ = fmt.Fprintln(diagnostics, "assign: Task was reassigned to you")
	}
	if _, err := fmt.Fprintf(diagnostics, "assign: switched to %s and recorded the Task branch association\n", plan.Branch); err != nil {
		return newExitError(ExitOperation, "Task started, local branch switched and association recorded; could not write diagnostics")
	}
	_, err = fmt.Fprintf(output, "%s  %s\n", safeInline(code), safeInline(result.Title))
	return err
}

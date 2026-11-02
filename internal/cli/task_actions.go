package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Conditions remain opaque. A quoted numeric revision is never an item
// observation, even though it satisfies the general HTTP entity-tag grammar.
func taskActionValidator(value string) bool {
	return strongTaskValidator(value) && strings.Trim(value[1:len(value)-1], "0123456789") != ""
}

// This is printed as a shell argument, never evaluated by the CLI. Quote every
// value, including the server's opaque validator, before offering a retry command.
func taskActionArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// One command owns one credential and at most one POST. Reads never refresh the
// validator for an explicit recovery: the original immutable ID/condition/key
// must reach the canonical replay boundary unchanged, even after a code rename.
func runTaskAction(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, action, code string, revision int64, key, targetStatusID, taskID, condition string) error {
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	transport, err := newOperationTransport(ctx, opts, client)
	if err != nil {
		return err
	}
	defer transport.client.CloseIdleConnections()
	if taskID == "" {
		workspace, err := resourceWorkspace(ctx, transport, "")
		if err != nil {
			return err
		}
		discovered, err := resolveResourceTask(ctx, transport, workspace.ID, code)
		if err != nil {
			return err
		}
		data, headers, err := taskActionExchange(ctx, transport, http.MethodGet, "/api/v1/tasks/"+discovered.ID+"?view=canonical", "", "")
		if err != nil {
			return err
		}
		var observed canonicalCreatedTask
		// Canonical items exclude the Project-derived display code. Bind the
		// exact discovery result through its immutable ID, Project and number.
		if json.Unmarshal(data, &observed) != nil || observed.ID != discovered.ID || observed.WorkspaceID != workspace.ID || observed.ProjectID != discovered.ProjectID || !canonicalIdentifier(observed.ProjectID) || observed.TaskNumber != discovered.TaskNumber || !canonicalPositiveDecimal(observed.TaskNumber) || observed.Title == "" || !canonicalPositiveDecimal(observed.Revision) || !taskActionValidator(headers.Get("ETag")) {
			return newExitError(ExitOperation, "canonical Task observation is incomplete; no action sent")
		}
		if observed.Revision != strconv.FormatInt(revision, 10) {
			return newExitError(ExitConflict, "Task revision changed; run assign task show %s before a new operation; no action sent", code)
		}
		taskID, condition = observed.ID, headers.Get("ETag")
	}
	canonicalAction := action
	if action == "done" {
		canonicalAction = "complete"
	}
	path := "/api/v1/tasks/" + taskID + "/" + canonicalAction
	if targetStatusID != "" {
		path += "?target_status_id=" + url.QueryEscape(targetStatusID)
	}
	retry := "assign --host " + taskActionArgument(transport.host) + " task " + action + " " + taskActionArgument(code) + " --task-id " + taskActionArgument(taskID) + " --if-match " + taskActionArgument(condition) + " --idempotency-key " + taskActionArgument(key)
	if targetStatusID != "" {
		retry += " --target-status-id " + taskActionArgument(targetStatusID)
	}
	// Persist the recovery tuple to stderr before the effect, including when
	// stdout is piped. Failure to expose it prevents the write.
	if _, err := fmt.Fprintf(diagnostics, "assign: retain the same credential; identical retry: %s\n", retry); err != nil {
		return newExitError(ExitOperation, "could not write Task recovery inputs; no action sent")
	}
	data, headers, err := taskActionExchange(ctx, transport, http.MethodPost, path, key, condition)
	if err != nil {
		return fmt.Errorf("%w; identical retry with the same credential: %s", err, retry)
	}
	var result struct {
		Task          canonicalCreatedTask `json:"task"`
		Reassigned    *bool                `json:"reassigned"`
		ItemValidator *struct {
			ETag           string `json:"etag"`
			Encoding       string `json:"encoding"`
			Representation string `json:"representation"`
		} `json:"item_validator"`
	}
	invalid := json.Unmarshal(data, &result) != nil || result.Task.ID != taskID || !canonicalIdentifier(result.Task.WorkspaceID) || !canonicalIdentifier(result.Task.ProjectID) || !canonicalPositiveDecimal(result.Task.TaskNumber) || result.Task.Title == "" || !canonicalPositiveDecimal(result.Task.Revision) || result.Reassigned == nil
	if result.Reassigned != nil && *result.Reassigned && action != "start" {
		invalid = true
	}
	if validator := result.ItemValidator; validator != nil {
		invalid = invalid || !taskActionValidator(validator.ETag) || validator.Encoding != "identity" || validator.Representation != "canonical-item" || !strongTaskValidator(headers.Get("ETag"))
	} else {
		// Historical acknowledgements deliberately carry neither validator.
		invalid = invalid || headers.Get("ETag") != ""
	}
	if invalid {
		return newExitError(ExitOperation, "Task action response is incomplete; retain the same credential and reconcile with identical retry: %s", retry)
	}
	if *result.Reassigned {
		if _, err := fmt.Fprintln(diagnostics, "assign: Task was reassigned to you"); err != nil {
			return newExitError(ExitOperation, "Task started, but the reassignment warning could not be written; retain the original recovery inputs")
		}
	}
	// Keep the selected code even for an original acknowledgement after a
	// rename. Recovery must not rediscover a mutable code or invent a new one.
	_, err = fmt.Fprintf(output, "%s  %s\n", safeInline(code), safeInline(result.Task.Title))
	return err
}

// Canonical Task representations can contain the complete description. Keep the
// existing Task mutation response budget; the smaller collection/Comment limit
// must not reject a legal full Task or mistake a truncated receipt for success.
func taskActionExchange(ctx context.Context, transport *operationTransport, method, path, key, condition string) ([]byte, http.Header, error) {
	uncertain := func() error {
		return newExitError(ExitOperation, "Task action outcome is uncertain; no automatic retry or replacement command")
	}
	request, err := http.NewRequestWithContext(ctx, method, transport.host+path, nil)
	if err != nil {
		return nil, nil, newExitError(ExitOperation, "prepare canonical Task action")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Authorization", "Bearer "+transport.token)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("If-Match", condition)
	}
	response, err := transport.canonicalExchange(request)
	if err != nil {
		if method == http.MethodPost {
			return nil, nil, uncertain()
		}
		return nil, nil, newExitError(ExitOperation, "canonical Task observation failed; no action sent")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case 401, 403:
			return nil, nil, newExitError(ExitAuthentication, "Task action access denied (HTTP %d); recovery stopped without changing credentials", response.StatusCode)
		case 404, 410:
			return nil, nil, newExitError(ExitContext, "Task action unavailable (HTTP %d); absence does not authorize a replacement command", response.StatusCode)
		case 409:
			if err := lifecycleConflict(response.Body); err != nil {
				return nil, nil, err
			}
			return nil, nil, newExitError(ExitConflict, "Task action conflict; reconcile current state before a new operation")
		case 412:
			return nil, nil, newExitError(ExitConflict, "Task observation is stale; reconcile current state before a new operation")
		case 400, 406, 413, 415, 422, 428:
			return nil, nil, newExitError(ExitArguments, "canonical Task action rejected (HTTP %d); no automatic retry", response.StatusCode)
		case 429:
			return nil, nil, newExitError(ExitOperation, "Task action rate limited; retain the original inputs and obey Retry-After")
		}
		if method == http.MethodPost {
			return nil, nil, uncertain()
		}
		return nil, nil, newExitError(ExitOperation, "canonical Task observation failed with HTTP %d; no action sent", response.StatusCode)
	}
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	data, readErr := io.ReadAll(io.LimitReader(response.Body, int64(taskCreateResponseLimit)+1))
	if mediaErr != nil || media != "application/json" || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") || readErr != nil || len(data) > taskCreateResponseLimit || !json.Valid(bytes.TrimSpace(data)) {
		if method == http.MethodPost {
			return nil, nil, uncertain()
		}
		return nil, nil, newExitError(ExitOperation, "canonical Task observation is incomplete; no action sent")
	}
	return data, response.Header.Clone(), nil
}

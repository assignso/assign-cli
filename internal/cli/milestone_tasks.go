package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

var milestoneScopeIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var milestoneQueryDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type milestoneTaskSelection struct {
	ID          string  `json:"id"`
	Code        string  `json:"code"`
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	MilestoneID *string `json:"milestone_id"`
	Title       string  `json:"title"`
	StatusID    string  `json:"status_id"`
	Priority    string  `json:"priority"`
	Revision    int64   `json:"revision"`
	Resolution  *string `json:"resolution"`
}

// The selection projection deliberately excludes descriptions and dependency
// claims. A checked membership scan is neither execution readiness nor export.
type milestoneTaskQueryItem struct {
	milestoneTaskSelection
	TaskNumber *int64  `json:"task_number"`
	Lifecycle  string  `json:"lifecycle"`
	ArchivedAt *string `json:"archived_at"`
	TrashedAt  *string `json:"trashed_at"`
}

type milestoneTaskQueryPage struct {
	Items              []milestoneTaskQueryItem `json:"items"`
	NextCursor         *string                  `json:"next_cursor"`
	HasMore            *bool                    `json:"has_more"`
	QueryDigest        string                   `json:"query_digest"`
	CollectionVersion  string                   `json:"collection_version"`
	VisibilityRevision string                   `json:"visibility_revision"`
	ObservedAt         string                   `json:"observed_at"`
	ReturnedCount      *int                     `json:"returned_count"`
	Coverage           string                   `json:"coverage"`
	LimitReason        string                   `json:"limit_reason,omitempty"`
}

func newMilestoneTasksCommand(opts *options, client *http.Client) *cobra.Command {
	page := taskPageOptions{limit: 100}
	command := &cobra.Command{Use: "tasks <milestone-code>", Short: "Read one checked page of a Milestone's non-archived Tasks", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return runMilestoneTaskPage(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts, client, args[0], page)
	}}
	command.Flags().StringVar(&page.format, "format", "text", "Page output: text or JSON with scope, Tasks and checked coverage")
	command.Flags().IntVar(&page.limit, "limit", 100, "Tasks per page (1–100); continuation remains explicit")
	command.Flags().StringVar(&page.cursor, "cursor", "", "Opaque checked continuation cursor from the same Milestone query")
	command.Flags().StringVar(&page.field, "field", "", "Print code values, next_cursor, or coverage")
	_ = command.RegisterFlagCompletionFunc("field", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"code", "next_cursor", "coverage"}, cobra.ShellCompDirectiveNoFileComp
	})
	return command
}

func runMilestoneTaskPage(ctx context.Context, output, diagnostics io.Writer, opts *options, client *http.Client, input string, options taskPageOptions) error {
	// Reuse bounded page/format/cursor validation while giving this checked
	// selector its own scalar fields: has_more alone cannot prove completeness.
	field := options.field
	if field != "" && field != "code" && field != "next_cursor" && field != "coverage" {
		return newExitError(ExitArguments, "--field must be code, next_cursor, or coverage")
	}
	if field == "coverage" {
		options.field = "code"
	}
	if err := options.validate(); err != nil {
		return err
	}
	code, err := canonicalMilestoneCode(input)
	if err != nil {
		return err
	}
	var scope cliMilestone
	if err := authorizedJSON(ctx, opts, client, http.MethodGet, "/api/v1/cli/milestones/"+url.PathEscape(code), nil, &scope); err != nil {
		return err
	}
	parts := milestoneCodePattern.FindStringSubmatch(code)
	if !validMilestoneScopeID(scope.ID) || !validMilestoneScopeID(scope.WorkspaceID) || !validMilestoneScopeID(scope.ProjectID) || scope.Code == nil || *scope.Code != code || scope.MilestoneNumber == nil || *scope.MilestoneNumber != parts[2] || scope.Revision < 1 {
		return newExitError(ExitOperation, "Milestone scope response is incomplete or inconsistent")
	}
	query := url.Values{"project_id": {scope.ProjectID}, "milestone_id": {scope.ID}, "consistency": {"checked"}, "state": {"all"}, "include_archived": {"false"}, "limit": {strconv.Itoa(options.limit)}}
	if options.cursor != "" {
		query.Set("cursor", options.cursor)
	}
	var page milestoneTaskQueryPage
	if err := authorizedJSON(ctx, opts, client, http.MethodGet, "/api/v1/workspaces/"+url.PathEscape(scope.WorkspaceID)+"/tasks?"+query.Encode(), nil, &page); err != nil {
		return err
	}
	if err := page.validate(scope, parts[1], options); err != nil {
		return err
	}
	items := make([]milestoneTaskSelection, len(page.Items))
	for index, item := range page.Items {
		items[index] = item.milestoneTaskSelection
	}
	stopped := page.Coverage == "stale" || page.LimitReason == "scan_limit"
	if options.format == "json" {
		err = json.NewEncoder(output).Encode(struct {
			Schema             string                   `json:"schema"`
			MilestoneCode      string                   `json:"milestone_code"`
			MilestoneID        string                   `json:"milestone_id"`
			WorkspaceID        string                   `json:"workspace_id"`
			ProjectID          string                   `json:"project_id"`
			MilestoneRevision  int64                    `json:"milestone_revision"`
			Items              []milestoneTaskSelection `json:"items"`
			NextCursor         *string                  `json:"next_cursor"`
			HasMore            bool                     `json:"has_more"`
			QueryDigest        string                   `json:"query_digest"`
			CollectionVersion  string                   `json:"collection_version"`
			VisibilityRevision string                   `json:"visibility_revision"`
			ObservedAt         string                   `json:"observed_at"`
			ReturnedCount      int                      `json:"returned_count"`
			Coverage           string                   `json:"coverage"`
			LimitReason        string                   `json:"limit_reason,omitempty"`
		}{"assign.cli.milestone-task-page.v1", code, scope.ID, scope.WorkspaceID, scope.ProjectID, scope.Revision, items, page.NextCursor, *page.HasMore, page.QueryDigest, page.CollectionVersion, page.VisibilityRevision, page.ObservedAt, *page.ReturnedCount, page.Coverage, page.LimitReason})
	} else {
		switch field {
		case "coverage":
			_, err = fmt.Fprintln(output, page.Coverage)
		case "next_cursor":
			if !stopped {
				cursor := ""
				if page.NextCursor != nil {
					cursor = *page.NextCursor
				}
				_, err = fmt.Fprintln(output, cursor)
			}
		default:
			for _, item := range items {
				if field == "code" {
					_, err = fmt.Fprintln(output, safeInline(item.Code))
				} else {
					_, err = fmt.Fprintf(output, "%s  %s\n", safeInline(item.Code), safeInline(item.Title))
				}
				if err != nil {
					return err
				}
			}
		}
	}
	if err != nil {
		return err
	}
	if stopped {
		return newExitError(ExitOperation, "Milestone Task selection is %s (%s); discard this scan before using a fresh selection", page.Coverage, page.LimitReason)
	}
	if *page.HasMore && options.format != "json" {
		_, err = fmt.Fprintf(diagnostics, "assign: Milestone selection is partial; continue with --cursor %s\n", *page.NextCursor)
	}
	return err
}

func validMilestoneScopeID(id string) bool {
	return milestoneScopeIDPattern.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}

func (page milestoneTaskQueryPage) validate(scope cliMilestone, projectKey string, options taskPageOptions) error {
	invalid := func() error {
		return newExitError(ExitOperation, "Milestone Task page response is incomplete or inconsistent")
	}
	observed, err := time.Parse(time.RFC3339Nano, page.ObservedAt)
	if err != nil || observed.IsZero() || page.Items == nil || page.HasMore == nil || page.ReturnedCount == nil || !milestoneQueryDigestPattern.MatchString(page.QueryDigest) || !milestoneQueryDigestPattern.MatchString(page.VisibilityRevision) || !validMilestoneScopeID(page.CollectionVersion) {
		return invalid()
	}
	if len(page.Items) > options.limit || *page.ReturnedCount < len(page.Items) || *page.ReturnedCount > 1000 || (options.cursor == "" && *page.ReturnedCount != len(page.Items)) {
		return invalid()
	}
	if (*page.HasMore && (page.NextCursor == nil || *page.NextCursor == "" || len(page.Items) == 0)) || (!*page.HasMore && page.NextCursor != nil && *page.NextCursor != "") {
		return invalid()
	}
	if page.NextCursor != nil && (len(*page.NextCursor) > 2048 || safeInline(*page.NextCursor) != *page.NextCursor) {
		return invalid()
	}
	switch page.Coverage {
	case "complete":
		if *page.HasMore || page.LimitReason != "" {
			return invalid()
		}
	case "partial":
		if (*page.HasMore && page.LimitReason != "more_pages") || (!*page.HasMore && page.LimitReason != "scan_limit") {
			return invalid()
		}
	case "stale":
		if *page.HasMore || len(page.Items) != 0 || *page.ReturnedCount != 0 || page.LimitReason != "collection_or_visibility_changed" {
			return invalid()
		}
	default:
		return invalid()
	}
	seen := make(map[string]bool, len(page.Items))
	seenCodes := make(map[string]bool, len(page.Items))
	for _, item := range page.Items {
		if !validMilestoneTaskSelection(item, scope, projectKey) || seen[item.ID] || seenCodes[item.Code] {
			return invalid()
		}
		seen[item.ID] = true
		seenCodes[item.Code] = true
	}
	return nil
}

func validMilestoneTaskSelection(item milestoneTaskQueryItem, scope cliMilestone, projectKey string) bool {
	return validMilestoneScopeID(item.ID) && validMilestoneScopeID(item.StatusID) && item.WorkspaceID == scope.WorkspaceID && item.ProjectID == scope.ProjectID && item.MilestoneID != nil && *item.MilestoneID == scope.ID && item.Title != "" && item.Priority != "" && item.Revision > 0 && item.TaskNumber != nil && *item.TaskNumber > 0 && item.Code == projectKey+"-"+strconv.FormatInt(*item.TaskNumber, 10) && item.Lifecycle == "active" && item.ArchivedAt == nil && item.TrashedAt == nil
}

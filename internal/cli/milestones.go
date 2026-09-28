package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var milestoneCodePattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]{1,7})-[Mm]([1-9][0-9]{0,18})$`)
var milestoneFields = []string{"id", "code", "number", "name", "project_id", "status", "revision", "due_on", "total_tasks", "completed_tasks", "incomplete_tasks"}

type cliMilestone struct {
	ID              string                `json:"id"`
	WorkspaceID     string                `json:"workspace_id"`
	ProjectID       string                `json:"project_id"`
	Name            string                `json:"name"`
	Description     *string               `json:"description"`
	DueOn           *string               `json:"due_on"`
	Status          string                `json:"status"`
	Revision        int64                 `json:"revision"`
	CreatedAt       string                `json:"created_at"`
	UpdatedAt       string                `json:"updated_at"`
	ArchivedAt      *string               `json:"archived_at"`
	MilestoneNumber *string               `json:"milestone_number"`
	Code            *string               `json:"code"`
	Progress        *cliMilestoneProgress `json:"progress"`
}

type cliMilestoneProgress struct {
	TotalTasks      *int64           `json:"total_tasks"`
	CompletedTasks  *int64           `json:"completed_tasks"`
	IncompleteTasks *int64           `json:"incomplete_tasks"`
	TasksByCategory map[string]int64 `json:"tasks_by_category"`
}

func newMilestoneCommand(opts *options, client *http.Client) *cobra.Command {
	group := &cobra.Command{Use: "milestone", Short: "Read Milestones by Project code", Args: noArgs}
	var field string
	var asJSON bool
	show := &cobra.Command{Use: "show <milestone-code>", Short: "Show a Milestone and canonical Task progress", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		field = strings.ToLower(strings.TrimSpace(field))
		if (field != "" && !slices.Contains(milestoneFields, field)) || (field != "" && asJSON) {
			return newExitError(ExitArguments, "use --json or one supported --field: %s", strings.Join(milestoneFields, ", "))
		}
		code, err := canonicalMilestoneCode(args[0])
		if err != nil {
			return err
		}
		parts := milestoneCodePattern.FindStringSubmatch(code)
		var item cliMilestone
		if err := authorizedJSON(command.Context(), opts, client, http.MethodGet, "/api/v1/cli/milestones/"+url.PathEscape(code), nil, &item); err != nil {
			return err
		}
		if item.ID == "" || item.WorkspaceID == "" || item.ProjectID == "" || item.Name == "" || item.Status == "" || item.Revision < 1 || item.Code == nil || *item.Code != code || item.MilestoneNumber == nil || *item.MilestoneNumber != parts[2] || item.Progress == nil {
			return newExitError(ExitOperation, "Milestone response is incomplete")
		}
		progress := item.Progress
		if progress.TotalTasks == nil || progress.CompletedTasks == nil || progress.IncompleteTasks == nil || progress.TasksByCategory == nil || *progress.TotalTasks < 0 || *progress.CompletedTasks < 0 || *progress.CompletedTasks > *progress.TotalTasks || *progress.IncompleteTasks != *progress.TotalTasks-*progress.CompletedTasks {
			return newExitError(ExitOperation, "Milestone progress response is incomplete or inconsistent")
		}
		var categoryTotal int64
		for _, count := range progress.TasksByCategory {
			if count < 0 || count > *progress.TotalTasks-categoryTotal {
				return newExitError(ExitOperation, "Milestone category counts are inconsistent")
			}
			categoryTotal += count
		}
		if categoryTotal != *progress.TotalTasks {
			return newExitError(ExitOperation, "Milestone category counts are incomplete")
		}
		if asJSON {
			return json.NewEncoder(command.OutOrStdout()).Encode(item)
		}
		value := ""
		switch field {
		case "id":
			value = item.ID
		case "code":
			value = *item.Code
		case "number":
			value = *item.MilestoneNumber
		case "name":
			value = item.Name
		case "project_id":
			value = item.ProjectID
		case "status":
			value = item.Status
		case "revision":
			value = strconv.FormatInt(item.Revision, 10)
		case "due_on":
			if item.DueOn != nil {
				value = *item.DueOn
			}
		case "total_tasks":
			value = strconv.FormatInt(*item.Progress.TotalTasks, 10)
		case "completed_tasks":
			value = strconv.FormatInt(*item.Progress.CompletedTasks, 10)
		case "incomplete_tasks":
			value = strconv.FormatInt(*item.Progress.IncompleteTasks, 10)
		}
		if field != "" {
			_, err := fmt.Fprintln(command.OutOrStdout(), safeInline(value))
			return err
		}
		_, err = fmt.Fprintf(command.OutOrStdout(), "# %s  %s\n\nProject: %s\nStatus: %s\nTasks: %d completed, %d incomplete, %d total\nRevision: %d\n", safeInline(code), safeInline(item.Name), safeInline(strings.ToUpper(parts[1])), safeInline(item.Status), *item.Progress.CompletedTasks, *item.Progress.IncompleteTasks, *item.Progress.TotalTasks, item.Revision)
		return err
	}}
	show.Flags().StringVar(&field, "field", "", "Print one field: "+strings.Join(milestoneFields, ", "))
	show.Flags().BoolVar(&asJSON, "json", false, "Print the canonical Milestone/progress projection as JSON")
	_ = show.RegisterFlagCompletionFunc("field", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return milestoneFields, cobra.ShellCompDirectiveNoFileComp
	})
	group.AddCommand(show, newMilestoneTasksCommand(opts, client))
	return group
}

func canonicalMilestoneCode(input string) (string, error) {
	parts := milestoneCodePattern.FindStringSubmatch(input)
	if parts == nil {
		return "", newExitError(ExitArguments, "use a Milestone code such as FRS-M1 without whitespace or leading zeros")
	}
	number, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || number < 1 {
		return "", newExitError(ExitArguments, "Milestone number must be a positive signed-64-bit value")
	}
	return strings.ToUpper(parts[1]) + "-M" + strconv.FormatInt(number, 10), nil
}

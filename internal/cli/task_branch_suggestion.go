package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

func newTaskBranchSuggestionCommand(opts *options, client *http.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "branch-suggestion <task-code>",
		Short: "Print the suggested Git branch name for a Task",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(command.Context(), operationDeadline)
			defer cancel()
			code := strings.ToUpper(strings.TrimSpace(args[0]))
			if code == "" {
				return newExitError(ExitArguments, "Task code must not be empty")
			}
			transport, _, task, err := readResourceTask(ctx, opts, client, code, "")
			if err != nil {
				return err
			}
			defer transport.client.CloseIdleConnections()
			query := url.Values{"view": {"task-branch-template"}, "task_id": {task.ID}}
			var suggestion struct {
				TaskID string `json:"task_id"`
				Branch string `json:"branch"`
			}
			path := "/api/v1/projects/" + task.ProjectID + "?" + query.Encode()
			if err := transport.exchangeJSON(ctx, http.MethodGet, path, "read Task branch suggestion", nil, &suggestion); err != nil {
				return err
			}
			if suggestion.TaskID != task.ID || suggestion.Branch == "" || len(suggestion.Branch) > 200 ||
				strings.IndexFunc(suggestion.Branch, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
				return newExitError(ExitOperation, "Task branch suggestion did not match its authorized Task")
			}
			if _, err := fmt.Fprintln(command.OutOrStdout(), suggestion.Branch); err != nil {
				return newExitError(ExitOperation, "could not write the Task branch suggestion")
			}
			return nil
		},
	}
}

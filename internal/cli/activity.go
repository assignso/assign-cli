package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// Activity remains a thin, explicitly paged read. The server owns grouping,
// counts, current authority and stale-cursor decisions.
func newActivityCommand(opts *options, client *http.Client) *cobra.Command {
	group := &cobra.Command{Use: "activity", Short: "Read authorized Activity groups", Args: noArgs}
	for _, children := range []bool{false, true} {
		children := children
		var workspace, scope, resource, day, from, before, zone, cursor, revision, snapshot string
		var actors, projects, types []string
		limit := 10
		if children {
			limit = 25
		}
		previews := true
		use, short := "groups", "Read one page of daily Activity groups"
		argsCheck := noArgs
		if children {
			use, short = "children <group-id>", "Expand one current Activity group"
			argsCheck = exactArgs(1)
		}
		command := &cobra.Command{Use: use, Short: short, Args: argsCheck, RunE: func(command *cobra.Command, args []string) error {
			maxLimit := 50
			if children {
				maxLimit = 25
			}
			if workspace == "" || !slices.Contains([]string{"workspace", "account", "person", "project", "task"}, scope) || limit < 1 || limit > maxLimit {
				return newExitError(ExitArguments, "supply --workspace, a valid --scope and a bounded --limit")
			}
			needsResource := scope == "person" || scope == "project" || scope == "task"
			if needsResource != (resource != "") || (day != "" && (from != "" || before != "")) || ((from == "") != (before == "")) {
				return newExitError(ExitArguments, "resource and day selectors do not match the selected scope")
			}
			if len(cursor) > 4096 || len(snapshot) > 4096 || len(actors) > 50 || len(projects) > 50 || len(types) > 50 {
				return newExitError(ExitArguments, "Activity selectors exceed their bounds")
			}
			query := url.Values{"scope": {scope}, "limit": {strconv.Itoa(limit)}, "include_previews": {strconv.FormatBool(previews)}}
			for key, value := range map[string]string{"resource_id": resource, "day": day, "from_day": from, "before_day": before, "time_zone": zone, "cursor": cursor} {
				if value != "" {
					query.Set(key, value)
				}
			}
			for key, values := range map[string][]string{"actor_ids": actors, "project_ids": projects, "activity_types": types} {
				if len(values) > 0 {
					query.Set(key, strings.Join(values, ","))
				}
			}
			path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/activity/groups"
			if children {
				if revision == "" || snapshot == "" || len(args[0]) > 192 {
					return newExitError(ExitArguments, "expansion requires --group-revision and --snapshot from the parent page")
				}
				path += "/" + url.PathEscape(args[0]) + "/children"
				query.Set("group_revision", revision)
				query.Set("snapshot", snapshot)
			}
			var output json.RawMessage
			if err := authorizedJSON(command.Context(), opts, client, http.MethodGet, path+"?"+query.Encode(), nil, &output); err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(output)
		}}
		flags := command.Flags()
		flags.StringVar(&workspace, "workspace", "", "Credential-bound Workspace UUID")
		flags.StringVar(&scope, "scope", "", "workspace, account, person, project or task")
		flags.StringVar(&resource, "resource-id", "", "Person, Project or Task UUID")
		flags.StringVar(&day, "day", "", "Civil day (YYYY-MM-DD)")
		flags.StringVar(&from, "from-day", "", "Inclusive civil day")
		flags.StringVar(&before, "before-day", "", "Exclusive civil day, at most 90 days after from-day")
		flags.StringVar(&zone, "time-zone", "", "IANA timezone; defaults to effective account/Workspace setting")
		flags.StringSliceVar(&actors, "actor-id", nil, "Actor UUID filter, at most 50")
		flags.StringSliceVar(&projects, "project-id", nil, "Project UUID filter, at most 50")
		flags.StringSliceVar(&types, "type", nil, "Activity type filter, at most 50")
		flags.IntVar(&limit, "limit", limit, "Page size")
		flags.StringVar(&cursor, "cursor", "", "Opaque continuation; preserve every selector")
		flags.BoolVar(&previews, "include-previews", true, "Include authorized current preview observations")
		if children {
			flags.StringVar(&revision, "group-revision", "", "Current parent revision")
			flags.StringVar(&snapshot, "snapshot", "", "Current parent snapshot")
		}
		group.AddCommand(command)
	}
	return group
}

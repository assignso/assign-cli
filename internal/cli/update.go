package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const releaseAPIURL = "https://api.github.com/repos/assignso/assign-cli/releases?per_page=100"

var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?$`)

type release struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type semver struct {
	major, minor, patch int
	pre                 string
}

func newUpdateCommand(client *http.Client) *cobra.Command {
	if client == nil {
		client = http.DefaultClient
	}
	command := &cobra.Command{
		Use:   "update",
		Short: "Check for Assign CLI updates",
		Args:  noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runUpdateCheck(command.Context(), command.OutOrStdout(), client, releaseAPIURL, version)
		},
	}
	command.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Check whether a newer Assign CLI release is available",
		Args:  noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runUpdateCheck(command.Context(), command.OutOrStdout(), client, releaseAPIURL, version)
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Show the verified platform installer command",
		Args:  noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runUpdateInstall(command.OutOrStdout())
		},
	})
	return command
}

func runUpdateInstall(output io.Writer) error {
	command := "curl https://assign.so/install.sh | sh"
	if runtime.GOOS == "windows" {
		command = "irm https://github.com/assignso/assign-cli/releases/latest/download/install.ps1 | iex"
	}
	_, err := fmt.Fprintln(output, command)
	return err
}

func runUpdateCheck(ctx context.Context, output io.Writer, client *http.Client, endpoint, installed string) error {
	current, ok := parseSemver(installed)
	if !ok {
		return newExitError(ExitOperation, "update checks require a released semantic version; installed version is %q", installed)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return newExitError(ExitOperation, "prepare update check")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "assign-cli/"+installed)
	response, err := client.Do(request)
	if err != nil {
		return newExitError(ExitOperation, "check for updates: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return newExitError(ExitOperation, "check for updates: release service returned HTTP %d", response.StatusCode)
	}
	var releases []release
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1024*1024))
	if err := decoder.Decode(&releases); err != nil {
		return newExitError(ExitOperation, "decode update information")
	}
	latest, found := newestRelease(releases, current.pre != "")
	if !found || compareSemver(latest, current) <= 0 {
		_, err := fmt.Fprintf(output, "assign %s is up to date\n", installed)
		return err
	}
	_, err = fmt.Fprintf(output, "assign %s is available (installed %s)\nRun: assign update install\n", formatSemver(latest), installed)
	return err
}

func newestRelease(releases []release, includePrereleases bool) (semver, bool) {
	versions := make([]semver, 0, len(releases))
	for _, item := range releases {
		if item.Draft || (!includePrereleases && item.Prerelease) {
			continue
		}
		parsed, ok := parseSemver(strings.TrimPrefix(item.TagName, "v"))
		if ok {
			versions = append(versions, parsed)
		}
	}
	if len(versions) == 0 {
		return semver{}, false
	}
	sort.Slice(versions, func(i, j int) bool { return compareSemver(versions[i], versions[j]) > 0 })
	return versions[0], true
}

func parseSemver(raw string) (semver, bool) {
	match := semverPattern.FindStringSubmatch(raw)
	if match == nil {
		return semver{}, false
	}
	var result semver
	if _, err := fmt.Sscanf(strings.Join(match[1:4], "."), "%d.%d.%d", &result.major, &result.minor, &result.patch); err != nil {
		return semver{}, false
	}
	result.pre = match[4]
	return result, true
}

func compareSemver(left, right semver) int {
	for _, pair := range [][2]int{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] > pair[1] {
			return 1
		}
		if pair[0] < pair[1] {
			return -1
		}
	}
	if left.pre == right.pre {
		return 0
	}
	if left.pre == "" {
		return 1
	}
	if right.pre == "" {
		return -1
	}
	leftParts, rightParts := strings.Split(left.pre, "."), strings.Split(right.pre, ".")
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		if leftParts[index] == rightParts[index] {
			continue
		}
		leftNumber, leftErr := strconv.Atoi(leftParts[index])
		rightNumber, rightErr := strconv.Atoi(rightParts[index])
		leftNumeric, rightNumeric := leftErr == nil, rightErr == nil
		switch {
		case leftNumeric && rightNumeric:
			if leftNumber > rightNumber {
				return 1
			}
			return -1
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case leftParts[index] > rightParts[index]:
			return 1
		default:
			return -1
		}
	}
	if len(leftParts) > len(rightParts) {
		return 1
	}
	return -1
}

func formatSemver(value semver) string {
	base := fmt.Sprintf("%d.%d.%d", value.major, value.minor, value.patch)
	if value.pre != "" {
		return base + "-" + value.pre
	}
	return base
}

func releaseHTTPClient() *http.Client {
	return &http.Client{Timeout: 8 * time.Second}
}

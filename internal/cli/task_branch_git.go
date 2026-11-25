package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	gitObjectID            = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	branchRepositoryOrigin = regexp.MustCompile(`^https://[a-z0-9][a-z0-9.-]*(?::[1-9][0-9]{0,4})?/(?:[A-Za-z0-9_][A-Za-z0-9_.-]*/)+[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
)

type branchGit struct {
	command string
	dir     string
	env     []string
}

func branchGitAtCurrentDirectory() (branchGit, error) {
	binary, err := exec.LookPath("git")
	if err != nil {
		return branchGit{}, newExitError(ExitContext, "Git is required for branch start")
	}
	dir, err := os.Getwd()
	if err != nil {
		return branchGit{}, newExitError(ExitContext, "cannot identify the current worktree")
	}
	var env []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(item), "GIT_") {
			env = append(env, item)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	return branchGit{binary, dir, env}, nil
}

// Bound every command's retained output and avoid surfacing Git's raw errors,
// which may contain worktree paths, file names, or credential-bearing URLs.
type gitOutput struct{ data bytes.Buffer }

func (w *gitOutput) Write(p []byte) (int, error) {
	if w.data.Len()+len(p) > 8192 {
		return 0, io.ErrShortWrite
	}
	return w.data.Write(p)
}

func (g branchGit) run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, g.command, append([]string{"--no-optional-locks", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}, args...)...)
	command.Dir, command.Env = g.dir, g.env
	var out gitOutput
	command.Stdout = &out
	command.Stderr = io.Discard
	err := command.Run()
	return strings.TrimSuffix(out.data.String(), "\n"), err
}

type branchCheckout struct {
	Repository string `json:"repository"`
	Head       string `json:"head"`
	Base       string `json:"base"`
}

func (g branchGit) preflight(ctx context.Context) (branchCheckout, error) {
	if inside, err := g.run(ctx, "rev-parse", "--is-inside-work-tree"); err != nil || inside != "true" {
		return branchCheckout{}, newExitError(ExitContext, "run from a non-bare Git worktree")
	}
	head, err := g.run(ctx, "symbolic-ref", "-q", "HEAD")
	if err != nil || !strings.HasPrefix(head, "refs/heads/") {
		return branchCheckout{}, newExitError(ExitConflict, "branch start requires an attached Git HEAD")
	}
	base, err := g.run(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !gitObjectID.MatchString(base) {
		return branchCheckout{}, newExitError(ExitConflict, "branch start requires an existing Git commit")
	}
	for _, one := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "rebase-merge", "rebase-apply", "sequencer", "BISECT_START", "index.lock", "HEAD.lock"} {
		path, err := g.run(ctx, "rev-parse", "--path-format=absolute", "--git-path", one)
		if err != nil || path == "" {
			return branchCheckout{}, newExitError(ExitConflict, "cannot verify Git operation state")
		}
		if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return branchCheckout{}, newExitError(ExitConflict, "finish the active Git operation before branch start")
		}
	}
	if status, err := g.run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignore-submodules=none"); err != nil || status != "" {
		return branchCheckout{}, newExitError(ExitConflict, "branch start requires a clean Git worktree and index")
	}
	raw, err := g.run(ctx, "remote", "get-url", "--all", "origin")
	repository := canonicalBranchRepository(raw)
	if err != nil || repository == "" {
		return branchCheckout{}, newExitError(ExitContext, "origin must identify the Project's bound repository using a supported HTTPS or Git SSH URL")
	}
	return branchCheckout{repository, head, base}, nil
}

// Resolve only the Git transport spelling. No remote is contacted, and raw
// credentials, query strings, custom transports and file paths are rejected.
func canonicalBranchRepository(raw string) string {
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok {
			return ""
		}
		raw = "ssh://git@" + host + "/" + path
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return ""
	}
	switch parsed.Scheme {
	case "https":
		if parsed.User != nil {
			return ""
		}
	case "ssh":
		if parsed.User == nil || parsed.User.String() != "git" || (parsed.Port() != "" && parsed.Port() != "22") {
			return ""
		}
		parsed.Host = parsed.Hostname()
	default:
		return ""
	}
	host := strings.ToLower(parsed.Host)
	if parsed.Scheme == "https" && parsed.Port() == "443" {
		host = strings.ToLower(parsed.Hostname())
	}
	path := strings.TrimSuffix(strings.TrimSuffix(parsed.Path, "/"), ".git")
	result := "https://" + host + path
	if len(result) > 512 || !branchRepositoryOrigin.MatchString(result) {
		return ""
	}
	return result
}

func validTaskBranchRef(branch string) bool {
	if branch == "" || len(branch) > 200 || !utf8.ValidString(branch) || branch == "HEAD" || branch == "@" ||
		strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") ||
		strings.ContainsAny(branch, `~^:?*[\<>"|`) ||
		strings.IndexFunc(branch, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (g branchGit) validateBranch(ctx context.Context, branch string) error {
	if !validTaskBranchRef(branch) {
		return newExitError(ExitConflict, "Core returned an invalid branch name")
	}
	if result, err := g.run(ctx, "check-ref-format", "--branch", branch); err != nil || result != branch {
		return newExitError(ExitConflict, "Core returned an invalid Git branch ref")
	}
	return nil
}

func (g branchGit) refExists(ctx context.Context, ref string) (bool, error) {
	_, err := g.run(ctx, "show-ref", "--verify", "--quiet", ref)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

func (g branchGit) branchCollision(ctx context.Context, branch string) error {
	ref := "refs/heads/" + branch
	matches, err := g.run(ctx, "for-each-ref", "--count=1", "--format=%(refname)", ref)
	if err != nil || matches != "" {
		return newExitError(ExitConflict, "the local Task branch already exists or collides with another ref")
	}
	for prefix := branch; strings.Contains(prefix, "/"); {
		prefix = prefix[:strings.LastIndexByte(prefix, '/')]
		if exists, err := g.refExists(ctx, "refs/heads/"+prefix); err != nil || exists {
			return newExitError(ExitConflict, "the local Task branch collides with a parent ref")
		}
	}
	return nil
}

func (g branchGit) ownedBranch(ctx context.Context, branch, base, marker string) (bool, error) {
	ref := "refs/heads/" + branch
	exists, err := g.refExists(ctx, ref)
	if err != nil || !exists {
		return false, err
	}
	tip, tipErr := g.run(ctx, "rev-parse", "--verify", ref)
	origin, logErr := g.run(ctx, "reflog", "show", "-1", "--format=%gs", ref)
	if tipErr != nil || logErr != nil || tip != base || origin != marker {
		return false, newExitError(ExitConflict, "an existing local branch cannot be verified as belonging to this operation")
	}
	return true, nil
}

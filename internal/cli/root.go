package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

const defaultHost = "https://api.assign.so"

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type options struct {
	host string
}

func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCommand(stdin, stdout, stderr)
	root.SetArgs(args)
	if _, _, err := root.Find(args); err != nil {
		fmt.Fprintf(stderr, "assign: %s\n", err)
		return ExitArguments
	}

	err := root.ExecuteContext(ctx)
	if err == nil || isBrokenPipe(err) {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return ExitInterrupt
	}

	fmt.Fprintf(stderr, "assign: %s\n", err)
	return exitCode(err)
}

func newRootCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:           "assign",
		Short:         "Work with Assign from the terminal",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          noArgs,
		RunE: func(*cobra.Command, []string) error {
			return newExitError(ExitOperation, "Home is not available in this foundation build; the bounded My Work API contract is still pending")
		},
		PersistentPreRunE: func(*cobra.Command, []string) error {
			if _, err := validatedHost(opts.host); err != nil {
				return newExitError(ExitArguments, "%s", err)
			}
			return nil
		},
	}
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return newExitError(ExitArguments, "%s", err)
	})
	root.CompletionOptions.DisableDefaultCmd = true
	root.CompletionOptions.SetDefaultShellCompDirective(cobra.ShellCompDirectiveNoFileComp)
	root.PersistentFlags().StringVar(&opts.host, "host", defaultHost, "Assign API host (HTTPS only, for this command)")

	root.AddCommand(newVersionCommand())
	root.AddCommand(newCompletionCommand(root))
	root.AddCommand(newDoctorCommand(opts))

	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "assign %s\ncommit %s\nbuilt %s\n", version, commit, date)
			return err
		},
	}
}

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	completion := &cobra.Command{
		Use:       "completion [bash|zsh|fish]",
		Short:     "Generate shell completion",
		Args:      exactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(cmd.OutOrStdout(), true)
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			default:
				return newExitError(ExitArguments, "unsupported shell %q; expected bash, zsh, or fish", args[0])
			}
		},
	}
	completion.CompletionOptions.DisableDefaultCmd = true
	return completion
}

func newDoctorCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run redacted local diagnostics",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			host, err := validatedHost(opts.host)
			if err != nil {
				return newExitError(ExitArguments, "%s", err)
			}

			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "host ok %s\n", host); err != nil {
				return err
			}

			authenticated := strings.TrimSpace(os.Getenv("ASSIGN_TOKEN")) != ""
			if authenticated {
				if _, err := fmt.Fprintln(out, "credential ok ASSIGN_TOKEN"); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(out, "credential failed not configured"); err != nil {
				return err
			}

			if inGitRepository(cmd.Context()) {
				if _, err := fmt.Fprintln(out, "repository ok detected"); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(out, "repository optional unavailable"); err != nil {
				return err
			}

			if _, err := fmt.Fprintf(out, "runtime ok %s/%s\n", runtime.GOOS, runtime.GOARCH); err != nil {
				return err
			}

			if !authenticated {
				return newExitError(ExitAuthentication, "authentication is not configured; set ASSIGN_TOKEN for automation or use login when browser authentication ships")
			}
			return nil
		},
	}
}

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return newExitError(ExitArguments, "unknown argument %q for %q", args[0], cmd.CommandPath())
}

func exactArgs(expected int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == expected {
			return nil
		}
		return newExitError(ExitArguments, "%q requires exactly %d argument(s)", cmd.CommandPath(), expected)
	}
}

func validatedHost(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("--host must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("--host must not include credentials, a path, query, or fragment")
	}
	parsed.Path = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func inGitRepository(ctx context.Context) bool {
	command := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	return command.Run() == nil
}

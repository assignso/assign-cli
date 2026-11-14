package cli

import (
	"bytes"
	"encoding/json"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"os"
	"strings"
	"testing"
)

// TestShellSnapshot captures constructors only, never authenticated actions.
func TestShellSnapshot(t *testing.T) {
	target := os.Getenv("ASSIGN_SHELL_SNAPSHOT")
	if target == "" {
		t.Skip("explicit snapshot generation only")
	}
	root := newRootCommand(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root.InitDefaultHelpCmd()
	root.InitDefaultVersionFlag()
	type flag struct {
		Name        string              `json:"name"`
		Short       string              `json:"short"`
		Type        string              `json:"type"`
		Default     string              `json:"default"`
		Annotations map[string][]string `json:"annotations"`
	}
	type node struct {
		Path     string   `json:"path"`
		Aliases  []string `json:"aliases"`
		Help     string   `json:"help"`
		Flags    []flag   `json:"flags"`
		Args     []string `json:"args"`
		Runnable bool     `json:"runnable"`
	}
	nodes := []node{}
	var visit func(*cobra.Command)
	visit = func(c *cobra.Command) {
		c.InitDefaultHelpFlag()
		var out bytes.Buffer
		c.SetOut(&out)
		if err := c.Help(); err != nil {
			t.Fatal(err)
		}
		n := node{Path: c.CommandPath(), Aliases: c.Aliases, Help: out.String(), Runnable: c.Runnable(), Args: []string{}}
		flags := pflag.NewFlagSet("capture", pflag.ContinueOnError)
		flags.AddFlagSet(c.InheritedFlags())
		flags.AddFlagSet(c.Flags())
		flags.VisitAll(func(f *pflag.Flag) {
			n.Flags = append(n.Flags, flag{f.Name, f.Shorthand, f.Value.Type(), f.DefValue, f.Annotations})
		})
		for count := 0; count < 16; count++ {
			args := make([]string, count)
			for i := range args {
				args[i] = "argument"
			}
			message := ""
			if c.Args != nil {
				if err := c.Args(c, args); err != nil {
					message = err.Error()
				}
			}
			n.Args = append(n.Args, message)
		}
		nodes = append(nodes, n)
		for _, child := range c.Commands() {
			visit(child)
		}
	}
	visit(root)
	scripts := map[string]string{}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var out bytes.Buffer
		var err error
		switch shell {
		case "bash":
			err = root.GenBashCompletionV2(&out, true)
		case "zsh":
			err = root.GenZshCompletion(&out)
		case "fish":
			err = root.GenFishCompletion(&out, true)
		}
		if err != nil {
			t.Fatal(err)
		}
		scripts[shell] = out.String()
	}
	var aliases bytes.Buffer
	if code := Execute(t.Context(), []string{"aliases"}, strings.NewReader(""), &aliases, &bytes.Buffer{}); code != 0 {
		t.Fatal(code)
	}
	type result struct {
		Argv   []string `json:"argv"`
		Code   int      `json:"code"`
		Stdout string   `json:"stdout"`
		Stderr string   `json:"stderr"`
	}
	cases := []result{}
	for _, argv := range [][]string{{"activity", "groups", "--actor-id", "\"unterminated"}, {"missing"}, {"versoin"}, {"ta"}, {"task", "wat"}, {"--unknown"}, {"version", "extra"}, {"task", "show"}, {"project", "tasks", "PRO", "--limit", "oops"}, {"operation", "wait", "abc", "--timeout", "oops"}, {"task", "context", "ABC-1", "--compact", "--full"}, {"--help=false", "version"}, {"--version"}, {"-v"}} {
		var stdout, stderr bytes.Buffer
		code := Execute(t.Context(), argv, strings.NewReader(""), &stdout, &stderr)
		cases = append(cases, result{argv, code, stdout.String(), stderr.String()})
	}
	data, err := json.MarshalIndent(map[string]any{"cases": cases, "nodes": nodes, "scripts": scripts, "aliases": aliases.String()}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

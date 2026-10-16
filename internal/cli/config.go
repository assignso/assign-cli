package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"
)

type cliSettingsStore interface {
	LoadHost() (string, error)
	SaveHost(string) error
	ResetHost() error
}

type fileCLISettingsStore struct {
	path    string
	initErr error
}

type cliSettings struct {
	Schema int    `json:"schema"`
	Host   string `json:"host"`
}

func defaultCLISettingsStore() cliSettingsStore {
	if path := os.Getenv("ASSIGN_CONFIG_FILE"); path != "" {
		return &fileCLISettingsStore{path: path}
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return &fileCLISettingsStore{initErr: errors.New("user configuration directory is unavailable; set ASSIGN_CONFIG_FILE")}
	}
	return &fileCLISettingsStore{path: filepath.Join(directory, "assign", "settings.json")}
}

func (store *fileCLISettingsStore) checkFile() error {
	if store.initErr != nil {
		return store.initErr
	}
	info, err := os.Lstat(store.path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
		return errors.New("CLI settings must be a regular, private file of at most 4 KiB; fix or remove the file named by ASSIGN_CONFIG_FILE (default: assign/settings.json in your user configuration directory)")
	}
	return nil
}

func (store *fileCLISettingsStore) LoadHost() (string, error) {
	if err := store.checkFile(); err != nil {
		return "", err
	}
	file, err := os.Open(store.path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	var settings cliSettings
	if err := decoder.Decode(&settings); err != nil || settings.Schema != 1 {
		return "", errors.New("CLI settings are invalid; run assign config set host <https-origin> to replace them")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", errors.New("CLI settings contain trailing data")
	}
	host, err := validatedHost(settings.Host)
	if err != nil {
		return "", errors.New("stored API host is invalid; run assign config set host <https-origin>")
	}
	return host, nil
}

func (store *fileCLISettingsStore) SaveHost(raw string) error {
	host, err := validatedHost(raw)
	if err != nil {
		return err
	}
	if err := store.checkFile(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name()) //nolint:errcheck
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if err := json.NewEncoder(file).Encode(cliSettings{Schema: 1, Host: host}); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), store.path)
}

func (store *fileCLISettingsStore) ResetHost() error {
	if err := store.checkFile(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.Remove(store.path)
}

func resolveConfiguredHost(command *cobra.Command, opts *options) error {
	top := command
	for top.Parent() != nil && top.Parent() != command.Root() {
		top = top.Parent()
	}
	localOnly := slices.Contains([]string{"version", "completion", "update", "aliases"}, top.Name())
	configWrite := top.Name() == "config" && (command.Name() == "set" || command.Name() == "unset")
	opts.hostSource = "default"
	if command.Flags().Changed("host") {
		opts.hostSource = "flag"
	} else if opts.settings != nil && !configWrite && !localOnly {
		host, err := opts.settings.LoadHost()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return newExitError(ExitArguments, "load CLI settings: %v", err)
		}
		if err == nil {
			opts.host, opts.hostSource = host, "stored"
		}
	}
	host, err := validatedHost(opts.host)
	if err != nil {
		return newExitError(ExitArguments, "%s", err)
	}
	opts.host = host
	return nil
}

func newConfigCommand(opts *options) *cobra.Command {
	show := func(command *cobra.Command, _ []string) error {
		_, err := fmt.Fprintf(command.OutOrStdout(), "API host: %s\nHost source: %s\n", safeInline(opts.host), opts.hostSource)
		return err
	}
	command := &cobra.Command{Use: "config", Short: "Inspect and change local CLI settings", Args: noArgs, RunE: show}
	command.AddCommand(&cobra.Command{Use: "show", Short: "Show the effective API host without credentials", Args: noArgs, RunE: show})
	command.AddCommand(&cobra.Command{Use: "set host <https-origin>", Short: "Persist the API host for future commands", Args: exactArgs(2), RunE: func(command *cobra.Command, args []string) error {
		if args[0] != "host" {
			return newExitError(ExitArguments, "only the host setting is supported")
		}
		if _, err := validatedHost(args[1]); err != nil {
			return newExitError(ExitArguments, "%s", err)
		}
		if err := opts.settings.SaveHost(args[1]); err != nil {
			return newExitError(ExitOperation, "save CLI settings: %v", err)
		}
		_, err := fmt.Fprintln(command.OutOrStdout(), "API host saved")
		return err
	}})
	command.AddCommand(&cobra.Command{Use: "unset host", Short: "Restore the default API host", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		if args[0] != "host" {
			return newExitError(ExitArguments, "only the host setting is supported")
		}
		if err := opts.settings.ResetHost(); err != nil {
			return newExitError(ExitOperation, "reset CLI settings: %v", err)
		}
		_, err := fmt.Fprintln(command.OutOrStdout(), "API host reset")
		return err
	}})
	return command
}

func newInitCommand(opts *options, client *http.Client) *cobra.Command {
	return &cobra.Command{Use: "init <project-code>", Short: "Validate and select a Project for the authenticated Workspace", Long: "Initialize CLI Project selection for the current authenticated Workspace and API host. Run assign login first. This stores a local hint in user configuration; every later Project read rechecks access.", Args: exactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return runProjectSwitch(command.Context(), command.OutOrStdout(), opts, client, args[0])
	}}
}

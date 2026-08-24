package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const contextSchema = 1

type projectContextStore interface {
	LoadProject(host, workspace string) (string, error)
	SaveProject(host, workspace, project string) error
}

type fileProjectContextStore struct {
	path    string
	initErr error
}

type contextFile struct {
	Schema   int               `json:"schema"`
	Projects map[string]string `json:"projects"`
}

func defaultProjectContextStore() projectContextStore {
	if override := os.Getenv("ASSIGN_CONTEXT_FILE"); override != "" {
		return &fileProjectContextStore{path: override}
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return &fileProjectContextStore{initErr: errors.New("user configuration directory is unavailable")}
		}
		directory = filepath.Join(home, ".config")
	}
	return &fileProjectContextStore{path: filepath.Join(directory, "assign", "context.json")}
}

func contextKey(host, workspace string) string { return host + "\n" + workspace }

func (store *fileProjectContextStore) LoadProject(host, workspace string) (string, error) {
	if store.initErr != nil {
		return "", store.initErr
	}
	file, err := store.read()
	if err != nil {
		return "", err
	}
	project := file.Projects[contextKey(host, workspace)]
	if project == "" {
		return "", os.ErrNotExist
	}
	return project, nil
}

func (store *fileProjectContextStore) SaveProject(host, workspace, project string) error {
	if store.initErr != nil {
		return store.initErr
	}
	file, err := store.read()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if file.Projects == nil {
		file = contextFile{Schema: contextSchema, Projects: make(map[string]string)}
	}
	file.Projects[contextKey(host, workspace)] = project
	return store.write(file)
}

func (store *fileProjectContextStore) read() (contextFile, error) {
	info, err := os.Stat(store.path)
	if err != nil {
		return contextFile{}, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return contextFile{}, fmt.Errorf("context file permissions are too broad; run chmod 600 %s", store.path)
	}
	file, err := os.Open(store.path)
	if err != nil {
		return contextFile{}, err
	}
	defer file.Close()
	var value contextFile
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024))
	if err := decoder.Decode(&value); err != nil || value.Schema != contextSchema || value.Projects == nil {
		return contextFile{}, errors.New("stored CLI context is invalid")
	}
	return value, nil
}

func (store *fileProjectContextStore) write(value contextFile) error {
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create context directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("restrict context directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".context-*")
	if err != nil {
		return fmt.Errorf("create context file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if err := json.NewEncoder(temporary).Encode(value); err != nil {
		temporary.Close()
		return fmt.Errorf("encode context file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync context file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close context file: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("replace context file: %w", err)
	}
	return nil
}

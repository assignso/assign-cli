package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
)

const (
	cliOAuthClientID     = "assign-cli"
	cliCredentialService = "Assign CLI"
	cliCredentialSchema  = 1
)

var errCredentialCorrupt = errors.New("stored CLI credential is invalid")

type storedCredential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type credentialStore interface {
	Load(string) (storedCredential, error)
	Save(string, storedCredential) error
	Delete(string) error
}

type fileCredentialStore struct{ path string }

type keyringCredentialStore struct{}

type fallbackCredentialStore struct {
	primary  credentialStore
	fallback credentialStore
}

func defaultCredentialStore() credentialStore {
	if override := os.Getenv("ASSIGN_CREDENTIALS_FILE"); override != "" {
		return &fileCredentialStore{path: override}
	}
	fileStore := defaultFileCredentialStore()
	return &fallbackCredentialStore{primary: &keyringCredentialStore{}, fallback: fileStore}
}

func defaultFileCredentialStore() *fileCredentialStore {
	directory, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return &fileCredentialStore{path: filepath.Join(string(filepath.Separator), "invalid-assign-credentials-path")}
		}
		directory = filepath.Join(home, ".config")
	}
	return &fileCredentialStore{path: filepath.Join(directory, "assign", "credentials.json")}
}

func (store *keyringCredentialStore) Load(host string) (storedCredential, error) {
	encoded, err := keyring.Get(cliCredentialService, host)
	if errors.Is(err, keyring.ErrNotFound) {
		return storedCredential{}, os.ErrNotExist
	}
	if err != nil {
		return storedCredential{}, fmt.Errorf("read OS credential vault: %w", err)
	}
	credential, err := decodeStoredCredential([]byte(encoded))
	if err != nil {
		return storedCredential{}, err
	}
	return credential, nil
}

func (store *keyringCredentialStore) Save(host string, credential storedCredential) error {
	encoded, err := encodeStoredCredential(credential)
	if err != nil {
		return err
	}
	if err := keyring.Set(cliCredentialService, host, string(encoded)); err != nil {
		return fmt.Errorf("write OS credential vault: %w", err)
	}
	return nil
}

func (store *keyringCredentialStore) Delete(host string) error {
	if err := keyring.Delete(cliCredentialService, host); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete OS credential vault entry: %w", err)
	}
	return nil
}

func (store *fallbackCredentialStore) Load(host string) (storedCredential, error) {
	credential, primaryErr := store.primary.Load(host)
	if primaryErr == nil {
		return credential, nil
	}
	if errors.Is(primaryErr, errCredentialCorrupt) {
		return storedCredential{}, primaryErr
	}
	credential, fallbackErr := store.fallback.Load(host)
	if fallbackErr != nil {
		if errors.Is(primaryErr, os.ErrNotExist) && errors.Is(fallbackErr, os.ErrNotExist) {
			return storedCredential{}, os.ErrNotExist
		}
		return storedCredential{}, errors.Join(primaryErr, fallbackErr)
	}
	// Transparently migrate a file fallback once the native vault becomes available.
	if err := store.primary.Save(host, credential); err == nil {
		_ = store.fallback.Delete(host)
	}
	return credential, nil
}

func (store *fallbackCredentialStore) Save(host string, credential storedCredential) error {
	if err := store.primary.Save(host, credential); err == nil {
		if err := store.fallback.Delete(host); err != nil {
			return fmt.Errorf("remove superseded credential fallback: %w", err)
		}
		return nil
	}
	return store.fallback.Save(host, credential)
}

func (store *fallbackCredentialStore) Delete(host string) error {
	return errors.Join(store.primary.Delete(host), store.fallback.Delete(host))
}

type encodedCredential struct {
	Schema       int       `json:"schema"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func encodeStoredCredential(credential storedCredential) ([]byte, error) {
	if credential.AccessToken == "" || credential.RefreshToken == "" || credential.ExpiresAt.IsZero() {
		return nil, errCredentialCorrupt
	}
	return json.Marshal(encodedCredential{
		Schema: cliCredentialSchema, AccessToken: credential.AccessToken,
		RefreshToken: credential.RefreshToken, ExpiresAt: credential.ExpiresAt,
	})
}

func decodeStoredCredential(value []byte) (storedCredential, error) {
	var encoded encodedCredential
	if err := json.Unmarshal(value, &encoded); err != nil || encoded.Schema != cliCredentialSchema || encoded.AccessToken == "" || encoded.RefreshToken == "" || encoded.ExpiresAt.IsZero() {
		return storedCredential{}, errCredentialCorrupt
	}
	return storedCredential{AccessToken: encoded.AccessToken, RefreshToken: encoded.RefreshToken, ExpiresAt: encoded.ExpiresAt}, nil
}

func (store *fileCredentialStore) Load(host string) (storedCredential, error) {
	items, err := store.read()
	if err != nil {
		return storedCredential{}, err
	}
	credential, ok := items[host]
	if !ok {
		return storedCredential{}, os.ErrNotExist
	}
	return credential, nil
}

func (store *fileCredentialStore) Save(host string, credential storedCredential) error {
	items, err := store.read()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if items == nil {
		items = make(map[string]storedCredential)
	}
	items[host] = credential
	return store.write(items)
}

func (store *fileCredentialStore) Delete(host string) error {
	items, err := store.read()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	delete(items, host)
	return store.write(items)
}

func (store *fileCredentialStore) read() (map[string]storedCredential, error) {
	info, err := os.Stat(store.path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("credential file permissions are too broad; run chmod 600 %s", store.path)
	}
	file, err := os.Open(store.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var items map[string]storedCredential
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024))
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf("decode credential store: %w", err)
	}
	return items, nil
}

func (store *fileCredentialStore) write(items map[string]storedCredential) error {
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("restrict credential directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".credentials-*")
	if err != nil {
		return fmt.Errorf("create credential file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(items); err != nil {
		temporary.Close()
		return fmt.Errorf("encode credential store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync credential store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close credential store: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("replace credential store: %w", err)
	}
	return nil
}

func newLoginCommand(opts *options, client *http.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Sign in through the system browser",
		Args:  noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runLogin(command.Context(), command.OutOrStdout(), command.ErrOrStderr(), opts.host, client, opts.credentials, openBrowser)
		},
	}
}

func newLogoutCommand(opts *options, client *http.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Revoke and remove the interactive CLI credential",
		Args:  noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runLogout(command.Context(), command.ErrOrStderr(), opts.host, client, opts.credentials)
		},
	}
}

func runLogin(ctx context.Context, output, diagnostic io.Writer, rawHost string, client *http.Client, store credentialStore, opener func(string) error) error {
	host, err := validatedHost(rawHost)
	if err != nil {
		return newExitError(ExitArguments, "%s", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return newExitError(ExitOperation, "bind loopback callback: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	expectedCallbackHost := fmt.Sprintf("127.0.0.1:%d", port)
	state, err := randomURLToken(32)
	if err != nil {
		return newExitError(ExitOperation, "generate OAuth state")
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		return newExitError(ExitOperation, "generate PKCE verifier")
	}
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	authorizationURL := host + "/api/v1/cli/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {cliOAuthClientID}, "redirect_uri": {redirectURI},
		"state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}.Encode()
	if err := opener(authorizationURL); err != nil {
		if _, writeErr := fmt.Fprintf(diagnostic, "Open this URL to sign in:\n%s\n", authorizationURL); writeErr != nil {
			return writeErr
		}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	callback := make(chan callbackResult, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Host != expectedCallbackHost || request.URL.Path != "/callback" || request.URL.Query().Get("state") != state || request.URL.Query().Get("code") == "" {
			http.Error(response, "Invalid Assign CLI callback.", http.StatusBadRequest)
			return
		}
		select {
		case callback <- callbackResult{code: request.URL.Query().Get("code")}:
		default:
		}
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(response, "<!doctype html><title>Assign CLI</title><p>Sign-in complete. You can close this window.</p>")
	})
	go func() { _ = server.Serve(listener) }()
	var result callbackResult
	select {
	case <-timeoutCtx.Done():
		_ = server.Close()
		return newExitError(ExitAuthentication, "browser sign-in timed out")
	case result = <-callback:
		_ = server.Close()
	}
	tokens, err := exchangeCode(timeoutCtx, client, host, redirectURI, result.code, verifier)
	if err != nil {
		return err
	}
	if err := store.Save(host, tokens); err != nil {
		return newExitError(ExitOperation, "store CLI credential: %v", err)
	}
	_, err = fmt.Fprintln(output, "Signed in to Assign.")
	return err
}

type callbackResult struct{ code string }

func exchangeCode(ctx context.Context, client *http.Client, host, redirectURI, code, verifier string) (storedCredential, error) {
	values := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {cliOAuthClientID}, "redirect_uri": {redirectURI},
		"code": {code}, "code_verifier": {verifier},
	}
	return requestTokens(ctx, client, host, values)
}

func refreshCredential(ctx context.Context, client *http.Client, host string, credential storedCredential) (storedCredential, error) {
	values := url.Values{"grant_type": {"refresh_token"}, "client_id": {cliOAuthClientID}, "refresh_token": {credential.RefreshToken}}
	return requestTokens(ctx, client, host, values)
}

func requestTokens(ctx context.Context, client *http.Client, host string, values url.Values) (storedCredential, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/v1/cli/oauth/token", strings.NewReader(values.Encode()))
	if err != nil {
		return storedCredential{}, newExitError(ExitOperation, "prepare token request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return storedCredential{}, newExitError(ExitOperation, "request CLI token: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return storedCredential{}, newExitError(ExitAuthentication, "CLI sign-in was rejected")
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&body); err != nil || body.AccessToken == "" || body.RefreshToken == "" || body.ExpiresIn < 1 || body.TokenType != "Bearer" {
		return storedCredential{}, newExitError(ExitOperation, "decode CLI token response")
	}
	return storedCredential{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken, ExpiresAt: time.Now().UTC().Add(time.Duration(body.ExpiresIn) * time.Second)}, nil
}

func authorizationToken(ctx context.Context, host string, client *http.Client, store credentialStore) (string, error) {
	if token := strings.TrimSpace(os.Getenv("ASSIGN_TOKEN")); token != "" {
		return token, nil
	}
	credential, err := store.Load(host)
	if err != nil {
		return "", newExitError(ExitAuthentication, "authentication is not configured; run assign login or set ASSIGN_TOKEN")
	}
	if time.Now().UTC().Add(30 * time.Second).Before(credential.ExpiresAt) {
		return credential.AccessToken, nil
	}
	credential, err = refreshCredential(ctx, client, host, credential)
	if err != nil {
		return "", err
	}
	if err := store.Save(host, credential); err != nil {
		return "", newExitError(ExitOperation, "store refreshed CLI credential: %v", err)
	}
	return credential.AccessToken, nil
}

func runLogout(ctx context.Context, diagnostic io.Writer, rawHost string, client *http.Client, store credentialStore) error {
	host, err := validatedHost(rawHost)
	if err != nil {
		return newExitError(ExitArguments, "%s", err)
	}
	credential, loadErr := store.Load(host)
	var remoteErr error
	if loadErr == nil && credential.AccessToken != "" {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/v1/cli/oauth/revoke", nil)
		if requestErr != nil {
			remoteErr = requestErr
		} else {
			request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
			response, doErr := client.Do(request)
			if doErr != nil {
				remoteErr = doErr
			} else {
				response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					remoteErr = fmt.Errorf("HTTP %d", response.StatusCode)
				}
			}
		}
	}
	if err := store.Delete(host); err != nil {
		return newExitError(ExitOperation, "remove local CLI credential: %v", err)
	}
	if remoteErr != nil {
		_, _ = fmt.Fprintln(diagnostic, "Local credential removed; server revocation could not be confirmed.")
		return newExitError(ExitOperation, "revoke CLI credential: %v", remoteErr)
	}
	return nil
}

func randomURLToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

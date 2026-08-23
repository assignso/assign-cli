package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunLoginBindsLoopbackAndCompletesPKCEExchange(t *testing.T) {
	var challenge, redirectURI string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/cli/oauth/token" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(request.PostForm.Get("code_verifier")))
		if request.PostForm.Get("code") != "test-code" || request.PostForm.Get("redirect_uri") != redirectURI || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
			t.Fatalf("invalid token exchange: %#v", request.PostForm)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":900}`))
	}))
	defer server.Close()

	callbackErr := make(chan error, 1)
	opener := func(target string) error {
		authorizationURL, err := url.Parse(target)
		if err != nil {
			return err
		}
		values := authorizationURL.Query()
		challenge = values.Get("code_challenge")
		redirectURI = values.Get("redirect_uri")
		if values.Get("client_id") != cliOAuthClientID || values.Get("code_challenge_method") != "S256" || values.Get("state") == "" || redirectURI == "" {
			return errors.New("invalid authorization URL")
		}
		go func() {
			callback, err := url.Parse(redirectURI)
			if err != nil {
				callbackErr <- err
				return
			}
			query := callback.Query()
			query.Set("code", "test-code")
			query.Set("state", values.Get("state"))
			callback.RawQuery = query.Encode()
			response, err := http.Get(callback.String()) //nolint:gosec -- exact loopback URI created by runLogin.
			if err == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				err = response.Body.Close()
			}
			callbackErr <- err
		}()
		return nil
	}
	store := &memoryCredentialStore{}
	var output strings.Builder
	if err := runLogin(context.Background(), &output, io.Discard, server.URL, server.Client(), store, opener); err != nil {
		t.Fatal(err)
	}
	if err := <-callbackErr; err != nil {
		t.Fatal(err)
	}
	if store.value.AccessToken != "access" || store.value.RefreshToken != "refresh" || output.String() != "Signed in to Assign.\n" {
		t.Fatalf("credential/output = %#v / %q", store.value, output.String())
	}
}

func TestAuthorizationTokenRefreshesAndRotatesStoredCredential(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "")
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/cli/oauth/token" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if request.PostForm.Get("grant_type") != "refresh_token" || request.PostForm.Get("client_id") != cliOAuthClientID || request.PostForm.Get("refresh_token") != "old-refresh" {
			t.Fatalf("form = %#v", request.PostForm)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":900}`))
	}))
	defer server.Close()
	store := &memoryCredentialStore{value: storedCredential{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute)}}
	token, err := authorizationToken(context.Background(), server.URL, server.Client(), store)
	if err != nil {
		t.Fatal(err)
	}
	if token != "new-access" || store.value.RefreshToken != "new-refresh" {
		t.Fatalf("token/store = %q / %#v", token, store.value)
	}
}

func TestAssignTokenTakesPrecedenceWithoutReadingStore(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "apt_automation")
	store := &memoryCredentialStore{loadErr: os.ErrPermission}
	token, err := authorizationToken(context.Background(), "https://api.assign.so", http.DefaultClient, store)
	if err != nil || token != "apt_automation" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}

func TestFileCredentialStoreUsesRestrictedAtomicFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credentials.json")
	store := &fileCredentialStore{path: path}
	want := storedCredential{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Second)}
	if err := store.Save("https://api.assign.so", want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory permissions = %o", directoryInfo.Mode().Perm())
	}
	got, err := store.Load("https://api.assign.so")
	if err != nil || got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("got = %#v, err = %v", got, err)
	}
	contents, _ := os.ReadFile(path)
	if strings.Contains(string(contents), "https://other.example") {
		t.Fatal("unexpected host credential")
	}
}

func TestFallbackCredentialStorePrefersNativeVault(t *testing.T) {
	want := storedCredential{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	primary := &memoryCredentialStore{}
	fallback := &memoryCredentialStore{saveErr: errors.New("fallback must not be written")}
	store := &fallbackCredentialStore{primary: primary, fallback: fallback}

	if err := store.Save("https://api.assign.so", want); err != nil {
		t.Fatal(err)
	}
	if primary.value.AccessToken != want.AccessToken || fallback.saveCalls != 0 {
		t.Fatalf("primary = %#v, fallback saves = %d", primary.value, fallback.saveCalls)
	}
}

func TestFallbackCredentialStoreUsesFileWhenVaultUnavailable(t *testing.T) {
	want := storedCredential{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	primary := &memoryCredentialStore{loadErr: errors.New("vault unavailable"), saveErr: errors.New("vault unavailable")}
	fallback := &memoryCredentialStore{}
	store := &fallbackCredentialStore{primary: primary, fallback: fallback}

	if err := store.Save("https://api.assign.so", want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("https://api.assign.so")
	if err != nil || got.AccessToken != want.AccessToken || fallback.saveCalls != 1 {
		t.Fatalf("got = %#v, err = %v, fallback saves = %d", got, err, fallback.saveCalls)
	}
}

func TestStoredCredentialCodecRejectsIncompleteValues(t *testing.T) {
	if _, err := decodeStoredCredential([]byte(`{"schema":1,"access_token":"access"}`)); !errors.Is(err, errCredentialCorrupt) {
		t.Fatalf("err = %v", err)
	}
}

type memoryCredentialStore struct {
	value     storedCredential
	loadErr   error
	saveErr   error
	deleteErr error
	saveCalls int
}

func (store *memoryCredentialStore) Load(string) (storedCredential, error) {
	return store.value, store.loadErr
}
func (store *memoryCredentialStore) Save(_ string, value storedCredential) error {
	store.saveCalls++
	if store.saveErr != nil {
		return store.saveErr
	}
	store.value = value
	return nil
}
func (store *memoryCredentialStore) Delete(string) error {
	if store.deleteErr != nil {
		return store.deleteErr
	}
	store.value = storedCredential{}
	return nil
}

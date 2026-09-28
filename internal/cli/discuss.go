package cli

import (
	"context"
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
)

const discussSessionHeader = "X-Assign-Discuss-Session"

func newDiscussCommand(opts *options, client *http.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "discuss",
		Short: "Open Discuss in the terminal",
		Args:  noArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runDiscuss(command.Context(), command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr(), opts, client)
		},
	}
}

func runDiscuss(ctx context.Context, input io.Reader, output, diagnostics io.Writer, opts *options, client *http.Client) error {
	if opts.isTerminal == nil || !opts.isTerminal(input, output) {
		return newExitError(ExitArguments, "discuss requires an interactive terminal")
	}
	workspace, err := currentWorkspace(ctx, opts, client)
	if err != nil {
		return err
	}
	if workspace.ID == "" {
		return newExitError(ExitOperation, "Workspace response does not support terminal Discuss; update Assign Core")
	}
	host, err := validatedHost(opts.host)
	if err != nil {
		return newExitError(ExitArguments, "%s", err)
	}
	secret, err := randomURLToken(32)
	if err != nil {
		return newExitError(ExitOperation, "create terminal Discuss session")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return newExitError(ExitOperation, "bind terminal Discuss broker: %v", err)
	}
	defer listener.Close()

	broker := &discussBroker{host: host, workspaceID: workspace.ID, secret: secret, client: discussNoRedirectClient(client), credentials: opts.credentials}
	server := &http.Server{Handler: broker, ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serverErrors <- serveErr
		}
		close(serverErrors)
	}()
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()

	binary, err := discussBinaryPath()
	if err != nil {
		return newExitError(ExitOperation, "%v", err)
	}
	environment := []string{
		"ASSIGN_DISCUSS_BROKER=http://" + listener.Addr().String(),
		"ASSIGN_DISCUSS_SESSION=" + secret,
		"ASSIGN_DISCUSS_WORKSPACE_ID=" + workspace.ID,
		"ASSIGN_DISCUSS_WORKSPACE_NAME=" + workspace.Name,
		"ASSIGN_DISCUSS_WORKSPACE_SLUG=" + workspace.Slug,
	}
	if host == "https://api.assign.so" {
		environment = append(environment, "ASSIGN_DISCUSS_WEB_ORIGIN=https://app.assign.so")
	}
	runner := opts.runDiscuss
	if runner == nil {
		runner = runDiscussSidecar
	}
	if err := runner(ctx, binary, environment, input, output, diagnostics); err != nil {
		return newExitError(ExitOperation, "terminal Discuss exited: %v", err)
	}
	select {
	case serveErr := <-serverErrors:
		if serveErr != nil {
			return newExitError(ExitOperation, "terminal Discuss broker stopped: %v", serveErr)
		}
	default:
	}
	return nil
}

type discussBroker struct {
	host        string
	workspaceID string
	secret      string
	client      *http.Client
	credentials credentialStore
}

func (broker *discussBroker) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.Header.Get(discussSessionHeader) != broker.secret {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return
	}
	prefix := "/api/v1/workspaces/" + url.PathEscape(broker.workspaceID) + "/discuss"
	if !allowedDiscussBrokerRequest(request.Method, request.URL.Path, prefix) {
		http.Error(response, "route not available to terminal Discuss", http.StatusNotFound)
		return
	}
	token, err := authorizationToken(request.Context(), broker.host, broker.client, broker.credentials)
	if err != nil {
		http.Error(response, "CLI authentication expired; run assign login", http.StatusUnauthorized)
		return
	}
	upstreamURL := broker.host + request.URL.RequestURI()
	var body io.Reader
	if request.Body != nil {
		body = http.MaxBytesReader(response, request.Body, 128<<10)
	}
	upstream, err := http.NewRequestWithContext(request.Context(), request.Method, upstreamURL, body)
	if err != nil {
		http.Error(response, "invalid Discuss request", http.StatusBadRequest)
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+token)
	for _, header := range []string{"Accept", "Content-Type", "Idempotency-Key", "Last-Event-ID", "X-Assign-Events-Version"} {
		if value := request.Header.Get(header); value != "" {
			upstream.Header.Set(header, value)
		}
	}
	result, err := broker.client.Do(upstream)
	if err != nil {
		http.Error(response, "Discuss is unavailable", http.StatusBadGateway)
		return
	}
	defer result.Body.Close()
	for _, header := range []string{"Content-Type", "Cache-Control", "Retry-After"} {
		if value := result.Header.Get(header); value != "" {
			response.Header().Set(header, value)
		}
	}
	if strings.HasPrefix(result.Header.Get("Content-Type"), "text/event-stream") {
		flusher, ok := response.(http.Flusher)
		if !ok {
			http.Error(response, "Discuss streaming is unavailable", http.StatusInternalServerError)
			return
		}
		response.WriteHeader(result.StatusCode)
		buffer := make([]byte, 32<<10)
		for {
			count, readErr := result.Body.Read(buffer)
			if count > 0 {
				if _, writeErr := response.Write(buffer[:count]); writeErr != nil {
					return
				}
				flusher.Flush()
			}
			if readErr != nil {
				return
			}
		}
	}
	response.WriteHeader(result.StatusCode)
	_, _ = io.Copy(response, io.LimitReader(result.Body, 2<<20))
}

func allowedDiscussBrokerRequest(method, path, prefix string) bool {
	relative := strings.TrimPrefix(path, prefix)
	if relative == path {
		return false
	}
	if method == http.MethodGet && (relative == "/availability" || relative == "/messages" || relative == "/events/stream") {
		return true
	}
	if method == http.MethodPost && relative == "/messages" {
		return true
	}
	if method == http.MethodPost && relative == "/turns" {
		return true
	}
	if method == http.MethodPost && strings.HasPrefix(relative, "/messages/") && strings.HasSuffix(relative, "/cancel") && strings.Count(relative, "/") == 3 {
		return true
	}
	if method == http.MethodPost && strings.HasPrefix(relative, "/interactions/") && strings.HasSuffix(relative, "/decision") && strings.Count(relative, "/") == 3 {
		return true
	}
	return false
}

func discussNoRedirectClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func discussBinaryPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("ASSIGN_DISCUSS_BINARY")); override != "" {
		return override, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate terminal Discuss client")
	}
	name := "assign-discuss"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(filepath.Dir(executable), name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("terminal Discuss client is missing beside assign; reinstall or update the CLI")
	}
	return path, nil
}

func runDiscussSidecar(ctx context.Context, binary string, environment []string, input io.Reader, output, diagnostics io.Writer) error {
	command := exec.CommandContext(ctx, binary)
	command.Env = discussChildEnvironment(os.Environ(), environment)
	command.Stdin, command.Stdout, command.Stderr = input, output, diagnostics
	return command.Run()
}

func discussChildEnvironment(base, session []string) []string {
	result := make([]string, 0, len(base)+len(session))
	for _, item := range base {
		name, _, _ := strings.Cut(item, "=")
		if name == "ASSIGN_TOKEN" || strings.HasPrefix(name, "ASSIGN_DISCUSS_") {
			continue
		}
		result = append(result, item)
	}
	return append(result, session...)
}

func isInteractiveTerminal(input io.Reader, output io.Writer) bool {
	in, inputOK := input.(*os.File)
	out, outputOK := output.(*os.File)
	if !inputOK || !outputOK {
		return false
	}
	inInfo, inErr := in.Stat()
	outInfo, outErr := out.Stat()
	return inErr == nil && outErr == nil && inInfo.Mode()&os.ModeCharDevice != 0 && outInfo.Mode()&os.ModeCharDevice != 0
}

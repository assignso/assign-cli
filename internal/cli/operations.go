package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

const operationStatusLimit = 16 * 1024
const operationDeadline = 60 * time.Second
const operationAttemptLimit = 30

var operationUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// This is a compact CLI projection of the canonical Operation union, not the
// source run or result. Result references are displayed, never automatically
// fetched or interpreted as commands, capabilities or authority.
type cliOperation struct {
	Outcome   string              `json:"outcome"`
	ID        string              `json:"id"`
	State     string              `json:"state"`
	ExpiresAt time.Time           `json:"expires_at"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
	Progress  json.RawMessage     `json:"progress,omitempty"`
	Result    *cliOperationResult `json:"result,omitempty"`
	Error     *cliOperationError  `json:"error,omitempty"`
}
type cliOperationResult struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
	Href        string `json:"href"`
	MediaType   string `json:"media_type"`
}
type cliOperationError struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
}

func operationID(value string) (string, error) {
	value = strings.ToLower(value)
	if !operationUUID.MatchString(value) || value == "00000000-0000-0000-0000-000000000000" {
		return "", newExitError(ExitArguments, "use a nonzero UUID for the Operation, Workspace or Agent identifier")
	}
	return value, nil
}

func newHostedAgentCommand(opts *options, client *http.Client) *cobra.Command {
	command := &cobra.Command{Use: "agent", Short: "Submit an existing Assign-hosted Agent"}
	var workspace, key string
	run := &cobra.Command{Use: "run <agent-id>", Short: "Submit one hosted run and print its compact Operation receipt", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		agentID, err := operationID(args[0])
		if err != nil {
			return err
		}
		workspaceID, err := operationID(workspace)
		if err != nil {
			return err
		}
		if key == "" {
			return newExitError(ExitArguments, "--idempotency-key is required; retain it and identical arguments to recover an unknown outcome")
		}
		key, err = mutationKey(key)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), operationDeadline)
		defer cancel()
		transport, err := newOperationTransport(ctx, opts, client)
		if err != nil {
			return err
		}
		defer transport.client.CloseIdleConnections()
		// Emit the original identity before any effect. A lost response never
		// becomes permission to choose a new key or infer absence from history.
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Retain --idempotency-key %s with Workspace %s and Agent %s for identical receipt recovery.\n", key, workspaceID, agentID); err != nil {
			return err
		}
		result, _, err := transport.request(ctx, http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/agents/"+agentID+"/runs", "{}", key, "", "submit")
		if err != nil {
			return err
		}
		return printOperation(cmd.OutOrStdout(), result)
	}}
	run.Flags().StringVar(&workspace, "workspace", "", "Workspace UUID (required)")
	run.Flags().StringVar(&key, "idempotency-key", "", "Original submission key, retained for identical recovery (required)")
	command.AddCommand(run)
	return command
}

func newOperationCommand(opts *options, client *http.Client) *cobra.Command {
	command := &cobra.Command{Use: "operation", Short: "Recover compact status for an existing hosted Operation"}
	for _, action := range []string{"show", "cancel", "wait"} {
		var key string
		timeout := operationDeadline
		child := &cobra.Command{Use: action + " <operation-id>", Short: map[string]string{"show": "Read current authorized compact status once", "cancel": "Request best-effort cancellation with the original command key", "wait": "Wait for external recovery within the bounded Operation policy"}[action], Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			id, err := operationID(args[0])
			if err != nil {
				return err
			}
			if timeout <= 0 || timeout > operationDeadline {
				return newExitError(ExitArguments, "--timeout must be greater than zero and at most 60s")
			}
			if action == "cancel" {
				if key == "" {
					return newExitError(ExitArguments, "--idempotency-key is required; retain the original cancellation key")
				}
				key, err = mutationKey(key)
				if err != nil {
					return err
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			transport, err := newOperationTransport(ctx, opts, client)
			if err != nil {
				return err
			}
			defer transport.client.CloseIdleConnections()
			if action == "wait" {
				result, err := transport.wait(ctx, id)
				if err != nil {
					return err
				}
				if err := printOperation(cmd.OutOrStdout(), result); err != nil {
					return err
				}
				switch result.State {
				case "failed":
					return newExitError(ExitOperation, "Operation %s failed; its compact status is on stdout", id)
				case "cancelled":
					return newExitError(ExitCancellation, "Operation %s was cancelled", id)
				}
				return nil
			}
			method, path := http.MethodGet, "/api/v1/operations/"+id
			if action == "cancel" {
				method, path = http.MethodPost, path+"/cancel"
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Retain cancellation key %s for Operation %s; cancellation is an intent until the source acknowledges it.\n", key, id); err != nil {
					return err
				}
			}
			result, _, err := transport.request(ctx, method, path, "", key, id, action)
			if err != nil {
				return err
			}
			return printOperation(cmd.OutOrStdout(), result)
		}}
		if action == "cancel" {
			child.Flags().StringVar(&key, "idempotency-key", "", "Original cancellation key (required)")
		}
		if action == "wait" {
			child.Flags().DurationVar(&timeout, "timeout", operationDeadline, "Caller deadline, at most 60s")
		}
		command.AddCommand(child)
	}
	return command
}

type operationTransport struct {
	host, token string
	client      http.Client
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
	jitter      func(time.Duration) time.Duration
}

func newOperationTransport(ctx context.Context, opts *options, client *http.Client) (*operationTransport, error) {
	host, err := validatedHost(opts.host)
	if err != nil {
		return nil, newExitError(ExitArguments, "%s", err)
	}
	// Refresh at most once, before the command. A denial during recovery stops
	// instead of nesting refresh/retry loops or changing grant families.
	token, err := authorizationToken(ctx, host, client, opts.credentials)
	if err != nil {
		return nil, err
	}
	transport := &operationTransport{host: host, token: token, client: *client, now: time.Now, sleep: sleepOperation, jitter: func(cap time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(cap) + 1)) }}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	standard, ok := base.(*http.Transport)
	if !ok {
		return nil, newExitError(ExitOperation, "Operation commands require the standard HTTP transport with implicit retries disabled")
	}
	// A fresh HTTP/1 connection prevents net/http's transparent replay on a
	// reused connection, including its Idempotency-Key heuristic for POST.
	// HTTP/2 owns independent refused-stream retries, so this bounded command
	// transport negotiates only HTTP/1.1 rather than nesting that retry owner.
	// The command owns every explicit attempt and closes its private transport.
	bounded := standard.Clone()
	bounded.DisableKeepAlives = true
	bounded.Protocols = new(http.Protocols)
	bounded.Protocols.SetHTTP1(true)
	bounded.ForceAttemptHTTP2 = false
	bounded.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if bounded.TLSClientConfig == nil {
		bounded.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	bounded.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.client.Transport = bounded
	transport.client.Jar = nil
	transport.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return transport, nil
}

type operationResponseError struct {
	err       error
	retryable bool
}

func (err *operationResponseError) Error() string { return err.err.Error() }
func (err *operationResponseError) Unwrap() error { return err.err }

// request performs exactly one explicit HTTP exchange. Neither writes nor
// individual reads have a retry loop; only wait owns bounded external recovery.
func (transport *operationTransport) request(ctx context.Context, method, path, body, key, expectedID, action string) (cliOperation, time.Duration, error) {
	var result cliOperation
	request, err := http.NewRequestWithContext(ctx, method, transport.host+path, strings.NewReader(body))
	if err != nil {
		return result, 0, newExitError(ExitOperation, "prepare Operation request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Authorization", "Bearer "+transport.token)
	request.GetBody = nil
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	unknown := func(cause error) error {
		message := "Operation request failed; recover the same Operation without resubmitting work"
		if method == http.MethodPost {
			message = "command outcome is uncertain; retain the original key and identical arguments for receipt recovery; never choose a new key"
		}
		if cause != nil {
			return fmt.Errorf("%s: %w", message, cause)
		}
		return newExitError(ExitOperation, "%s", message)
	}
	response, err := transport.client.Do(request)
	if err != nil {
		return result, 0, unknown(err)
	}
	defer response.Body.Close()
	floor, err := operationRetryAfter(response.Header, transport.now())
	if err != nil {
		return result, 0, unknown(nil)
	}
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return result, floor, newExitError(ExitAuthentication, "Operation access was denied (HTTP %d); recovery stopped without changing credentials", response.StatusCode)
	case http.StatusNotFound:
		return result, floor, newExitError(ExitContext, "Operation or Agent is unavailable to this credential; absence does not permit resubmission")
	case http.StatusGone:
		return result, floor, newExitError(ExitContext, "Operation recovery has expired; do not submit the same work with a new key")
	case http.StatusConflict:
		if id, ok := operationLocation(response.Header); ok && (expectedID == "" || id == expectedID) {
			return result, floor, newExitError(ExitConflict, "source reconciliation is required; recover Operation %s with operation show using the same --host; retain the original key", id)
		}
		return result, floor, newExitError(ExitConflict, "Operation conflict requires reconciliation with the original key; no automatic retry or new submission")
	case http.StatusBadRequest, http.StatusNotAcceptable, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return result, floor, newExitError(ExitArguments, "Operation request was rejected (HTTP %d); inspect the input without an automatic retry", response.StatusCode)
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return result, floor, &operationResponseError{err: unknown(nil), retryable: method == http.MethodGet}
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return result, floor, unknown(nil)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") || response.ContentLength > operationStatusLimit {
		return result, floor, unknown(nil)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, operationStatusLimit+1))
	if err != nil || len(data) > operationStatusLimit {
		return result, floor, unknown(err)
	}
	result, err = decodeOperation(data, expectedID)
	if err != nil {
		return cliOperation{}, floor, unknown(nil)
	}
	if action == "submit" {
		id, ok := operationLocation(response.Header)
		if !ok || id != result.ID || response.StatusCode != http.StatusAccepted || result.State != "pending" {
			return cliOperation{}, floor, unknown(nil)
		}
	} else if action == "cancel" {
		if (response.StatusCode == http.StatusAccepted && result.State != "cancel_requested") || (response.StatusCode == http.StatusOK && result.Outcome == "active") {
			return cliOperation{}, floor, unknown(nil)
		}
	} else if response.StatusCode != http.StatusOK {
		return cliOperation{}, floor, unknown(nil)
	}
	return result, floor, nil
}

func operationLocation(header http.Header) (string, bool) {
	values := header.Values("Location")
	if len(values) != 1 || !strings.HasPrefix(values[0], "/api/v1/operations/") {
		return "", false
	}
	id, err := operationID(strings.TrimPrefix(values[0], "/api/v1/operations/"))
	return id, err == nil && values[0] == "/api/v1/operations/"+id
}

func operationRetryAfter(header http.Header, now time.Time) (time.Duration, error) {
	values := header.Values("Retry-After")
	if len(values) == 0 {
		return 2 * time.Second, nil
	}
	if len(values) != 1 {
		return 0, errors.New("ambiguous Retry-After")
	}
	raw := strings.TrimSpace(values[0])
	if seconds, err := strconv.ParseUint(raw, 10, 32); err == nil {
		return max(2*time.Second, time.Duration(seconds)*time.Second), nil
	}
	date, err := http.ParseTime(raw)
	if err != nil {
		return 0, errors.New("invalid Retry-After")
	}
	return max(2*time.Second, date.Sub(now)), nil
}

func decodeOperation(data []byte, expectedID string) (cliOperation, error) {
	var result cliOperation
	if len(data) > operationStatusLimit || !utf8.Valid(data) {
		return result, errors.New("invalid Operation encoding or size")
	}
	if err := unambiguousOperationJSON(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return result, errors.New("trailing Operation data")
	}
	id, err := operationID(result.ID)
	if err != nil || id != result.ID || (expectedID != "" && id != expectedID) || result.CreatedAt.IsZero() || result.UpdatedAt.Before(result.CreatedAt) || !result.ExpiresAt.After(result.CreatedAt) {
		return result, errors.New("invalid Operation identity or timestamps")
	}
	switch result.State {
	case "pending", "running", "cancel_requested":
		if result.Outcome != "active" || len(result.Progress) == 0 || result.Result != nil || result.Error != nil {
			return result, errors.New("invalid active Operation")
		}
		if string(result.Progress) != "null" {
			var progress float64
			if err := json.Unmarshal(result.Progress, &progress); err != nil || progress < 0 || progress > 1 {
				return result, errors.New("invalid Operation progress")
			}
		}
	case "succeeded":
		if result.Outcome != "succeeded" || result.Result == nil || result.Error != nil || len(result.Progress) != 0 || !validOperationResult(*result.Result, result.ID) {
			return result, errors.New("invalid Operation result reference")
		}
	case "failed":
		if result.Outcome != "failed" || result.Result != nil || len(result.Progress) != 0 || result.Error == nil || result.Error.Status < 400 || result.Error.Status > 599 || result.Error.Type == "" || result.Error.Title == "" {
			return result, errors.New("invalid failed Operation")
		}
		result.Error.Type = safeInline(result.Error.Type)
		result.Error.Title = safeInline(result.Error.Title)
		result.Error.Code = safeInline(result.Error.Code)
	case "cancelled":
		if result.Outcome != "cancelled" || result.Result != nil || result.Error != nil || len(result.Progress) != 0 {
			return result, errors.New("invalid cancelled Operation")
		}
	default:
		return result, errors.New("unknown Operation state")
	}
	return result, nil
}

// Bound and disambiguate the received union before Go's case-insensitive field
// matching can choose an alias or silently take the last repeated decoded key.
// Unknown additive fields remain ignored by the compact projection.
func unambiguousOperationJSON(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("Operation nesting exceeds its bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("ambiguous Operation field")
			}
			seen[name] = true
			for _, canonical := range []string{"outcome", "id", "state", "expires_at", "created_at", "updated_at", "progress", "result", "error", "kind", "operation_id", "href", "media_type", "type", "title", "status", "code"} {
				if name != canonical && strings.EqualFold(name, canonical) {
					return errors.New("Operation field alias")
				}
			}
		}
		if err := unambiguousOperationJSON(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func validOperationResult(result cliOperationResult, id string) bool {
	// Only the currently integrated canonical source is supported. New source
	// families add their exact operation/path binding here, never arbitrary URLs.
	parts := strings.Split(result.Href, "/")
	if result.Kind != "authorized-resource" || result.OperationID != "listWorkspaceAgentRunOutputs" || result.MediaType != "application/json" || len(parts) != 10 || parts[0] != "" || parts[1] != "api" || parts[2] != "v1" || parts[3] != "workspaces" || parts[5] != "agents" || parts[7] != "runs" || parts[8] != id || parts[9] != "outputs" {
		return false
	}
	// Only the source owner's canonical output collection is a result reader.
	workspace, err := operationID(parts[4])
	if err != nil || workspace != parts[4] {
		return false
	}
	agent, err := operationID(parts[6])
	return err == nil && agent == parts[6]
}

func printOperation(writer io.Writer, result cliOperation) error {
	return json.NewEncoder(writer).Encode(result)
}

func sleepOperation(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (transport *operationTransport) wait(ctx context.Context, id string) (cliOperation, error) {
	// Own the hard cap even when invoked outside Cobra. Calls are synchronous;
	// no ticker, background request or nested retry survives return/cancellation.
	ctx, cancel := context.WithTimeout(ctx, operationDeadline)
	defer cancel()
	for attempt := 0; attempt < operationAttemptLimit; attempt++ {
		if err := ctx.Err(); err != nil {
			return cliOperation{}, fmt.Errorf("recovery stopped; retain Operation %s: %w", id, err)
		}
		result, floor, err := transport.request(ctx, http.MethodGet, "/api/v1/operations/"+id, "", "", id, "show")
		if err == nil && result.Outcome != "active" {
			return result, nil
		}
		if err != nil {
			var responseError *operationResponseError
			if !errors.As(err, &responseError) || !responseError.retryable {
				return cliOperation{}, err
			}
		}
		if attempt+1 == operationAttemptLimit {
			break
		}
		// A two-second floor plus full jitter over an exponentially increasing
		// window keeps the minimum even when jitter chooses zero. Retry-After
		// is a minimum and can exhaust the caller deadline without another read.
		window := min(8*time.Second, time.Second*time.Duration(1<<min(attempt, 3)))
		delay := max(floor, 2*time.Second)
		if delay <= operationDeadline {
			delay += transport.jitter(window)
		}
		if err := transport.sleep(ctx, delay); err != nil {
			return cliOperation{}, fmt.Errorf("recovery stopped; retain Operation %s: %w", id, err)
		}
	}
	return cliOperation{}, newExitError(ExitOperation, "recovery reached 30 status attempts; retain Operation %s and inspect it later without resubmitting work", id)
}
